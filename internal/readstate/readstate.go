// Package readstate owns per-shard read receipts: one row per (channel,
// user) holding how far that user has read, not a log of every read event.
// A message is "seen by" a member when that member's last_read_sequence is
// >= the message's own sequence — the UI computes this itself from the two
// numbers it already has, no per-message denormalization needed (contrast
// with internal/reactions, which does need per-message denormalized state).
package readstate

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/events"
)

type ReadState struct {
	ChannelID        uuid.UUID
	UserID           uuid.UUID
	LastReadSequence int64
	UpdatedAt        time.Time
}

type Repo struct{}

func NewRepo() *Repo { return &Repo{} }

// MarkRead advances userID's watermark for channelID to sequence —
// monotonic, never regresses (a client reporting a stale sequence after a
// network reorder must not undo a more recent read). sequence <= 0 resolves
// to the channel's current latest message sequence first; if the channel
// has no messages yet, that's a no-op (changed=false).
//
// changed=false also covers the ordinary idempotent case (the watermark was
// already at or past sequence) — no write happens and no event is emitted,
// matching internal/reactions.Repo's idempotent-no-op shape.
func (r *Repo) MarkRead(ctx context.Context, pool *pgxpool.Pool, channelID, userID uuid.UUID, sequence int64) (newSequence int64, changed bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("readstate: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if sequence <= 0 {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(sequence), 0) FROM messages WHERE channel_id = $1
		`, channelID).Scan(&sequence); err != nil {
			return 0, false, fmt.Errorf("readstate: resolve latest sequence: %w", err)
		}
		if sequence == 0 {
			return 0, false, tx.Commit(ctx)
		}
	}

	var current int64
	err = tx.QueryRow(ctx, `
		SELECT last_read_sequence FROM channel_read_state
		WHERE channel_id = $1 AND user_id = $2
		FOR UPDATE
	`, channelID, userID).Scan(&current)
	if err != nil && err != pgx.ErrNoRows {
		return 0, false, fmt.Errorf("readstate: read current: %w", err)
	}
	exists := err == nil
	if exists && current >= sequence {
		return current, false, tx.Commit(ctx)
	}

	if exists {
		if _, err := tx.Exec(ctx, `
			UPDATE channel_read_state SET last_read_sequence = $3, updated_at = now()
			WHERE channel_id = $1 AND user_id = $2
		`, channelID, userID, sequence); err != nil {
			return 0, false, fmt.Errorf("readstate: update: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			INSERT INTO channel_read_state (channel_id, user_id, last_read_sequence, updated_at)
			VALUES ($1, $2, $3, now())
		`, channelID, userID, sequence); err != nil {
			return 0, false, fmt.Errorf("readstate: insert: %w", err)
		}
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return 0, false, fmt.Errorf("readstate: generate event id: %w", err)
	}
	payload := events.ReadUpdatedPayload{EventID: eventID, ChannelID: channelID, UserID: userID, LastReadSequence: sequence}
	if err := events.InsertOutboxWithID(ctx, tx, eventID, events.TopicReadUpdated, channelID, payload); err != nil {
		return 0, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("readstate: commit: %w", err)
	}
	return sequence, true, nil
}

// ListState returns every member's current watermark for channelID — the
// initial snapshot a client loads once when opening a channel; after that,
// read.updated realtime events keep it current without re-querying.
func (r *Repo) ListState(ctx context.Context, pool *pgxpool.Pool, channelID uuid.UUID) ([]ReadState, error) {
	rows, err := pool.Query(ctx, `
		SELECT channel_id, user_id, last_read_sequence, updated_at
		FROM channel_read_state WHERE channel_id = $1
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("readstate: list: %w", err)
	}
	defer rows.Close()

	var out []ReadState
	for rows.Next() {
		var s ReadState
		if err := rows.Scan(&s.ChannelID, &s.UserID, &s.LastReadSequence, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("readstate: scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
