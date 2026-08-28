// Package reactions owns per-shard message reactions and keeping each
// message's denormalized reaction_counts/latest_reactions columns
// (migrations/shard/0002_reactions.sql, migrations/shard/0003_reaction_key.sql)
// in sync with them. Every mutating call recomputes both from
// message_reactions and writes the result back to the message row inside
// the same transaction — the UI never joins message_reactions itself, only
// reads what's already on the message.
package reactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/events"
)

// MaxLatestReactions caps how many of a message's most recent reactions are
// kept denormalized on the message row — enough for a UI to show "Alice,
// Bob +3 others" without ever querying message_reactions directly.
const MaxLatestReactions = 5

// ValidReactions is the closed set of reaction keys the API accepts —
// canonical strings, never a raw emoji glyph. Unicode has multiple byte
// sequences for the same visible emoji (skin-tone modifiers, variation
// selectors), which makes filtering/aggregating on the literal character
// unreliable; a fixed key set also means the value is validated server-side
// instead of accepting arbitrary strings. The demo UI maps each key to a
// glyph purely for display (see demo/lib/reactions.ts).
var ValidReactions = map[string]bool{
	"like":      true,
	"dislike":   true,
	"love":      true,
	"laugh":     true,
	"celebrate": true,
	"eyes":      true,
	"rocket":    true,
	"fire":      true,
}

const pgForeignKeyViolation = "23503"

// ErrMessageNotFound is returned by Add when message_id doesn't exist in
// channel_id — enforced by message_reactions' foreign key
// (migrations/shard/0002_reactions.sql), not a separate existence check, so
// it's race-free against the message being deleted between check and
// insert (there's no delete path today, but the constraint is the actual
// source of truth either way).
var ErrMessageNotFound = errors.New("reactions: message not found")

type Repo struct{}

func NewRepo() *Repo { return &Repo{} }

// Add records userID's reaction on messageID (idempotent: reacting again
// with the same key is a no-op, changed=false, current state unchanged).
// Only a genuinely new reaction recomputes the denormalized state and emits
// a ReactionUpdated event. reaction is trusted to already be validated
// against ValidReactions — callers (cmd/api) own that check so the 400 can
// carry a helpful message.
func (r *Repo) Add(ctx context.Context, pool *pgxpool.Pool, channelID, messageID, userID uuid.UUID, reaction string) (counts map[string]int, latest []events.ReactionSummary, changed bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, nil, false, fmt.Errorf("reactions: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()
	tag, err := tx.Exec(ctx, `
		INSERT INTO message_reactions (channel_id, message_id, user_id, reaction, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING
	`, channelID, messageID, userID, reaction, now)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, nil, false, ErrMessageNotFound
		}
		return nil, nil, false, fmt.Errorf("reactions: insert: %w", err)
	}
	added := tag.RowsAffected() > 0

	if !added {
		counts, latest, err = r.currentState(ctx, tx, channelID, messageID)
		if err != nil {
			return nil, nil, false, err
		}
		return counts, latest, false, tx.Commit(ctx)
	}

	counts, latest, err = r.recompute(ctx, tx, channelID, messageID)
	if err != nil {
		return nil, nil, false, err
	}
	if err := r.updateMessage(ctx, tx, channelID, messageID, counts, latest); err != nil {
		return nil, nil, false, err
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, false, fmt.Errorf("reactions: generate event id: %w", err)
	}
	payload := events.ReactionUpdatedPayload{
		EventID: eventID, ChannelID: channelID, MessageID: messageID, ActorID: userID,
		Reaction: reaction, Action: "added", ReactionCounts: counts, LatestReactions: latest,
	}
	if err := events.InsertOutboxWithID(ctx, tx, eventID, events.TopicReactionUpdated, channelID, payload); err != nil {
		return nil, nil, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, false, fmt.Errorf("reactions: commit: %w", err)
	}
	return counts, latest, true, nil
}

// Remove removes userID's own reaction from messageID (idempotent: removing
// a reaction that doesn't exist is a no-op, changed=false).
func (r *Repo) Remove(ctx context.Context, pool *pgxpool.Pool, channelID, messageID, userID uuid.UUID, reaction string) (counts map[string]int, latest []events.ReactionSummary, changed bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, nil, false, fmt.Errorf("reactions: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		DELETE FROM message_reactions WHERE channel_id = $1 AND message_id = $2 AND user_id = $3 AND reaction = $4
	`, channelID, messageID, userID, reaction)
	if err != nil {
		return nil, nil, false, fmt.Errorf("reactions: delete: %w", err)
	}
	removed := tag.RowsAffected() > 0

	if !removed {
		counts, latest, err = r.currentState(ctx, tx, channelID, messageID)
		if err != nil {
			return nil, nil, false, err
		}
		return counts, latest, false, tx.Commit(ctx)
	}

	counts, latest, err = r.recompute(ctx, tx, channelID, messageID)
	if err != nil {
		return nil, nil, false, err
	}
	if err := r.updateMessage(ctx, tx, channelID, messageID, counts, latest); err != nil {
		return nil, nil, false, err
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, false, fmt.Errorf("reactions: generate event id: %w", err)
	}
	payload := events.ReactionUpdatedPayload{
		EventID: eventID, ChannelID: channelID, MessageID: messageID, ActorID: userID,
		Reaction: reaction, Action: "removed", ReactionCounts: counts, LatestReactions: latest,
	}
	if err := events.InsertOutboxWithID(ctx, tx, eventID, events.TopicReactionUpdated, channelID, payload); err != nil {
		return nil, nil, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, false, fmt.Errorf("reactions: commit: %w", err)
	}
	return counts, latest, true, nil
}

// recompute derives fresh reaction_counts/latest_reactions straight from
// message_reactions — simpler and less error-prone than incrementally
// patching JSONB in SQL, and cheap: both queries are index-scoped to one
// message (idx_message_reactions_message).
func (r *Repo) recompute(ctx context.Context, tx pgx.Tx, channelID, messageID uuid.UUID) (map[string]int, []events.ReactionSummary, error) {
	counts := map[string]int{}
	rows, err := tx.Query(ctx, `
		SELECT reaction, count(*) FROM message_reactions
		WHERE channel_id = $1 AND message_id = $2
		GROUP BY reaction
	`, channelID, messageID)
	if err != nil {
		return nil, nil, fmt.Errorf("reactions: count by reaction: %w", err)
	}
	for rows.Next() {
		var reaction string
		var c int
		if err := rows.Scan(&reaction, &c); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("reactions: scan count: %w", err)
		}
		counts[reaction] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("reactions: iterate counts: %w", err)
	}

	latest := []events.ReactionSummary{}
	lrows, err := tx.Query(ctx, `
		SELECT reaction, user_id, created_at FROM message_reactions
		WHERE channel_id = $1 AND message_id = $2
		ORDER BY created_at DESC
		LIMIT $3
	`, channelID, messageID, MaxLatestReactions)
	if err != nil {
		return nil, nil, fmt.Errorf("reactions: list latest: %w", err)
	}
	for lrows.Next() {
		var s events.ReactionSummary
		if err := lrows.Scan(&s.Reaction, &s.UserID, &s.CreatedAt); err != nil {
			lrows.Close()
			return nil, nil, fmt.Errorf("reactions: scan latest: %w", err)
		}
		latest = append(latest, s)
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return nil, nil, fmt.Errorf("reactions: iterate latest: %w", err)
	}

	return counts, latest, nil
}

func (r *Repo) updateMessage(ctx context.Context, tx pgx.Tx, channelID, messageID uuid.UUID, counts map[string]int, latest []events.ReactionSummary) error {
	countsJSON, err := json.Marshal(counts)
	if err != nil {
		return fmt.Errorf("reactions: marshal counts: %w", err)
	}
	latestJSON, err := json.Marshal(latest)
	if err != nil {
		return fmt.Errorf("reactions: marshal latest: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE messages SET reaction_counts = $1, latest_reactions = $2
		WHERE channel_id = $3 AND message_id = $4
	`, countsJSON, latestJSON, channelID, messageID); err != nil {
		return fmt.Errorf("reactions: update message: %w", err)
	}
	return nil
}

// currentState reads back a message's already-stored denormalized state —
// used on the idempotent no-op path, where nothing changed so there's
// nothing to recompute.
func (r *Repo) currentState(ctx context.Context, tx pgx.Tx, channelID, messageID uuid.UUID) (map[string]int, []events.ReactionSummary, error) {
	var countsRaw, latestRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT reaction_counts, latest_reactions FROM messages WHERE channel_id = $1 AND message_id = $2
	`, channelID, messageID).Scan(&countsRaw, &latestRaw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil, ErrMessageNotFound
		}
		return nil, nil, fmt.Errorf("reactions: read current state: %w", err)
	}
	var counts map[string]int
	if err := json.Unmarshal(countsRaw, &counts); err != nil {
		return nil, nil, fmt.Errorf("reactions: unmarshal counts: %w", err)
	}
	var latest []events.ReactionSummary
	if err := json.Unmarshal(latestRaw, &latest); err != nil {
		return nil, nil, fmt.Errorf("reactions: unmarshal latest: %w", err)
	}
	return counts, latest, nil
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation
}
