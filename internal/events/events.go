// Package events defines the durable event types published through the
// transactional outbox (INSTRUCTIONS.md §16) and the helpers to write/poll
// outbox rows. Future features (§44) add new event types here plus a new
// outbox write call at their point of origin; see .claude/skills/new-event.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const TopicMessageCreated = "message.created"

type MessageCreatedPayload struct {
	MessageID       uuid.UUID `json:"message_id"`
	ChannelID       uuid.UUID `json:"channel_id"`
	SenderID        uuid.UUID `json:"sender_id"`
	ClientMessageID uuid.UUID `json:"client_message_id"`
	Sequence        int64     `json:"sequence"`
	Body            string    `json:"body"`
	CreatedAt       time.Time `json:"created_at"`
}

const TopicReactionUpdated = "reaction.updated"

// ReactionSummary is one entry of a message's denormalized latest-reactions
// list (see internal/reactions) — also the shape carried on the wire in
// ReactionUpdatedPayload and the realtime delivery frame, so there's exactly
// one definition of what a "reaction" looks like outside message_reactions
// itself. Reaction is a canonical string key (e.g. "like", "rocket"), never
// a raw emoji glyph — Unicode has multiple byte sequences for the same
// visible emoji (skin-tone modifiers, variation selectors), which makes
// filtering/aggregating on the literal character unreliable. The UI maps
// keys to glyphs for display (see internal/reactions.ValidReactions for the
// allow-listed set).
type ReactionSummary struct {
	Reaction  string    `json:"reaction"`
	UserID    uuid.UUID `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ReactionUpdatedPayload carries the message's *fresh* denormalized state
// alongside what changed, so a consumer never needs to re-query to render
// it — the same "durable event carries everything needed" shape as
// MessageCreatedPayload. EventID is echoed from the outbox row itself
// (see InsertOutboxWithID) because, unlike a message's (channel_id,
// sequence), a given (message, user, reaction) can legitimately be added
// and removed many times, so that triple alone can't dedup a specific event.
type ReactionUpdatedPayload struct {
	EventID         uuid.UUID         `json:"event_id"`
	ChannelID       uuid.UUID         `json:"channel_id"`
	MessageID       uuid.UUID         `json:"message_id"`
	ActorID         uuid.UUID         `json:"actor_id"`
	Reaction        string            `json:"reaction"`
	Action          string            `json:"action"` // "added" | "removed"
	ReactionCounts  map[string]int    `json:"reaction_counts"`
	LatestReactions []ReactionSummary `json:"latest_reactions"`
}

const TopicReadUpdated = "read.updated"

// ReadUpdatedPayload carries a user's fresh read watermark for a channel.
// Unlike a reaction, marking read is monotonic — internal/readstate.Repo
// only ever advances last_read_sequence, never regresses it — so EventID
// dedup exists for the same reason as ReactionUpdatedPayload's: to survive
// an outbox publish/delete crash causing redelivery, not because the same
// (channel, user) pair repeats non-idempotently.
type ReadUpdatedPayload struct {
	EventID          uuid.UUID `json:"event_id"`
	ChannelID        uuid.UUID `json:"channel_id"`
	UserID           uuid.UUID `json:"user_id"`
	LastReadSequence int64     `json:"last_read_sequence"`
}

// InsertOutbox writes an outbox row inside the caller's transaction. It must
// always be called in the same transaction as the domain write it
// accompanies (see internal/messages.Repo.Send) — that's what makes the
// outbox transactional.
func InsertOutbox(ctx context.Context, tx pgx.Tx, eventType string, channelID uuid.UUID, payload any) error {
	eventID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("events: generate event id: %w", err)
	}
	return InsertOutboxWithID(ctx, tx, eventID, eventType, channelID, payload)
}

// InsertOutboxWithID is InsertOutbox with a caller-supplied event_id — for
// event types (like ReactionUpdatedPayload) that need to embed their own
// event_id in the payload itself as a fanout dedup key, since the outbox
// row's event_id never travels with the Kafka message otherwise.
func InsertOutboxWithID(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, eventType string, channelID uuid.UUID, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("events: marshal payload: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (event_id, event_type, channel_id, payload, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, eventID, eventType, channelID, data, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("events: insert outbox: %w", err)
	}
	return nil
}

// OutboxRow is a row read back by the publisher worker.
type OutboxRow struct {
	EventID   uuid.UUID
	EventType string
	ChannelID uuid.UUID
	Payload   []byte
	CreatedAt time.Time
}
