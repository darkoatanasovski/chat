// Package messages owns the per-shard message log: send (with idempotency
// and sequence assignment) and cursor-paginated retrieval. Every call takes
// an explicit *pgxpool.Pool for the physical shard internal/routing resolved
// — this package never decides which shard to talk to.
package messages

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

const pgUniqueViolation = "23505"

var ErrNotFound = errors.New("message not found")

type Message struct {
	ChannelID       uuid.UUID
	Sequence        int64
	MessageID       uuid.UUID
	SenderID        uuid.UUID
	ClientMessageID uuid.UUID
	Body            string
	CreatedAt       time.Time
	// ReactionCounts/LatestReactions are denormalized on the row itself
	// (migrations/shard/0002_reactions.sql) and kept current by
	// internal/reactions on every add/remove — reading a message never
	// joins message_reactions.
	ReactionCounts  map[string]int
	LatestReactions []events.ReactionSummary
}

type Repo struct{}

func NewRepo() *Repo {
	return &Repo{}
}

// Send is idempotent on (channel_id, client_message_id): a retried send
// returns the original message with created=false instead of creating a
// duplicate (INSTRUCTIONS.md §19). Sequence assignment locks only the
// single channel_sequences row for this channel, never the whole table.
func (r *Repo) Send(ctx context.Context, pool *pgxpool.Pool, channelID, senderID, clientMessageID uuid.UUID, body string) (msg Message, created bool, err error) {
	if existing, ok, ferr := r.getByClientMessageID(ctx, pool, channelID, clientMessageID); ferr != nil {
		return Message{}, false, ferr
	} else if ok {
		return existing, false, nil
	}

	messageID, err := uuid.NewV7()
	if err != nil {
		return Message{}, false, fmt.Errorf("messages: generate id: %w", err)
	}
	now := time.Now().UTC()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Message{}, false, fmt.Errorf("messages: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_sequences (channel_id, last_sequence) VALUES ($1, 0)
		ON CONFLICT DO NOTHING
	`, channelID); err != nil {
		return Message{}, false, fmt.Errorf("messages: ensure sequence row: %w", err)
	}

	var sequence int64
	if err := tx.QueryRow(ctx, `
		UPDATE channel_sequences SET last_sequence = last_sequence + 1
		WHERE channel_id = $1
		RETURNING last_sequence
	`, channelID).Scan(&sequence); err != nil {
		return Message{}, false, fmt.Errorf("messages: assign sequence: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (channel_id, sequence, message_id, sender_id, client_message_id, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, channelID, sequence, messageID, senderID, clientMessageID, body, now); err != nil {
		if isUniqueViolation(err) {
			// Lost a race against a concurrent retry with the same
			// client_message_id: someone else already committed it.
			tx.Rollback(ctx)
			existing, ok, ferr := r.getByClientMessageID(ctx, pool, channelID, clientMessageID)
			if ferr != nil {
				return Message{}, false, ferr
			}
			if ok {
				return existing, false, nil
			}
		}
		return Message{}, false, fmt.Errorf("messages: insert: %w", err)
	}

	payload := events.MessageCreatedPayload{
		MessageID:       messageID,
		ChannelID:       channelID,
		SenderID:        senderID,
		ClientMessageID: clientMessageID,
		Sequence:        sequence,
		Body:            body,
		CreatedAt:       now,
	}
	if err := events.InsertOutbox(ctx, tx, events.TopicMessageCreated, channelID, payload); err != nil {
		return Message{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Message{}, false, fmt.Errorf("messages: commit: %w", err)
	}

	return Message{
		ChannelID:       channelID,
		Sequence:        sequence,
		MessageID:       messageID,
		SenderID:        senderID,
		ClientMessageID: clientMessageID,
		Body:            body,
		CreatedAt:       now,
		ReactionCounts:  map[string]int{},
		LatestReactions: []events.ReactionSummary{},
	}, true, nil
}

func (r *Repo) getByClientMessageID(ctx context.Context, pool *pgxpool.Pool, channelID, clientMessageID uuid.UUID) (Message, bool, error) {
	var m Message
	var countsRaw, latestRaw []byte
	err := pool.QueryRow(ctx, `
		SELECT channel_id, sequence, message_id, sender_id, client_message_id, body, created_at, reaction_counts, latest_reactions
		FROM messages WHERE channel_id = $1 AND client_message_id = $2
	`, channelID, clientMessageID).Scan(
		&m.ChannelID, &m.Sequence, &m.MessageID, &m.SenderID, &m.ClientMessageID, &m.Body, &m.CreatedAt, &countsRaw, &latestRaw,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Message{}, false, nil
		}
		return Message{}, false, fmt.Errorf("messages: lookup by client_message_id: %w", err)
	}
	if err := unmarshalReactionState(&m, countsRaw, latestRaw); err != nil {
		return Message{}, false, err
	}
	return m, true, nil
}

// ListBefore returns up to limit messages with sequence < before, newest
// first. before=0 starts from the most recent message. Cursor pagination
// only — never OFFSET (INSTRUCTIONS.md §11).
//
// excludeSenders filters out any message from those sender IDs — the
// caller's blocked set (internal/blocks), applied here rather than after
// the fact in Go so a filtered-out message doesn't quietly shrink a page
// below limit: the WHERE clause and LIMIT are evaluated together, exactly
// like every other predicate on this query. nil/empty excludes nothing —
// `!= ALL(ARRAY[]::uuid[])` is vacuously true for every row — but pgx
// encodes a nil Go slice as SQL NULL, not an empty array, and
// `!= ALL(NULL)` evaluates to NULL (excludes everything) rather than true,
// so a nil excludeSenders is normalized to a non-nil empty slice before
// this ever reaches the query.
func (r *Repo) ListBefore(ctx context.Context, pool *pgxpool.Pool, channelID uuid.UUID, before int64, limit int, excludeSenders []uuid.UUID) ([]Message, error) {
	if excludeSenders == nil {
		excludeSenders = []uuid.UUID{}
	}
	rows, err := pool.Query(ctx, `
		SELECT channel_id, sequence, message_id, sender_id, client_message_id, body, created_at, reaction_counts, latest_reactions
		FROM messages
		WHERE channel_id = $1 AND ($2 = 0 OR sequence < $2) AND sender_id != ALL($4::uuid[])
		ORDER BY sequence DESC
		LIMIT $3
	`, channelID, before, limit, excludeSenders)
	if err != nil {
		return nil, fmt.Errorf("messages: list before: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		var countsRaw, latestRaw []byte
		if err := rows.Scan(&m.ChannelID, &m.Sequence, &m.MessageID, &m.SenderID, &m.ClientMessageID, &m.Body, &m.CreatedAt, &countsRaw, &latestRaw); err != nil {
			return nil, fmt.Errorf("messages: scan: %w", err)
		}
		if err := unmarshalReactionState(&m, countsRaw, latestRaw); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SumSequencesByChannels backs the dashboard's messages-sent view. Every
// message send increments channel_sequences.last_sequence for its channel
// (see the Send transaction below), so that column already IS the exact
// message count per channel — reading it here means this never has to scan
// the messages table itself. Channels absent from the result sent no
// messages; the caller treats them as 0, same convention as
// users.Repo.CountByRegion.
func (r *Repo) SumSequencesByChannels(ctx context.Context, pool *pgxpool.Pool, channelIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	counts := map[uuid.UUID]int64{}
	if len(channelIDs) == 0 {
		return counts, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT channel_id, last_sequence FROM channel_sequences WHERE channel_id = ANY($1)
	`, channelIDs)
	if err != nil {
		return nil, fmt.Errorf("messages: sum sequences by channels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var seq int64
		if err := rows.Scan(&id, &seq); err != nil {
			return nil, fmt.Errorf("messages: sum sequences by channels: %w", err)
		}
		counts[id] = seq
	}
	return counts, rows.Err()
}

func unmarshalReactionState(m *Message, countsRaw, latestRaw []byte) error {
	if err := json.Unmarshal(countsRaw, &m.ReactionCounts); err != nil {
		return fmt.Errorf("messages: unmarshal reaction_counts: %w", err)
	}
	if err := json.Unmarshal(latestRaw, &m.LatestReactions); err != nil {
		return fmt.Errorf("messages: unmarshal latest_reactions: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
