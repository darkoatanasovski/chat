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
	MessageID       uuid.UUID  `json:"message_id"`
	ChannelID       uuid.UUID  `json:"channel_id"`
	SenderID        uuid.UUID  `json:"sender_id"`
	ClientMessageID uuid.UUID  `json:"client_message_id"`
	Sequence        int64      `json:"sequence"`
	Body            string     `json:"body"`
	// ParentID is nil for a top-level message, or the message this one
	// replies to (internal/messages.Repo.Send) — carried through so a
	// realtime consumer (internal/realtime/fanout.go) can render/thread a
	// live reply without a follow-up fetch.
	ParentID *uuid.UUID `json:"parent_id,omitempty"`
	// ParentReplyCount is the parent message's *fresh* denormalized
	// reply_count immediately after this reply was recorded (nil for a
	// top-level message) — carried through the same "durable event carries
	// everything needed" way ReactionUpdatedPayload carries a message's
	// fresh reaction_counts, so a realtime consumer that already has the
	// parent bubble on screen can bump its displayed count in place
	// instead of re-fetching the parent.
	ParentReplyCount *int64 `json:"parent_reply_count,omitempty"`
	// PollID is set when this message has a poll attached
	// (internal/polls) — carried through so a realtime consumer can fetch
	// and render the poll without waiting for a follow-up request to
	// discover it exists.
	PollID    *uuid.UUID `json:"poll_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
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

const TopicPollVoteUpdated = "poll.vote_updated"

// PollOptionTally is one option's fresh vote_count — the shape carried on
// the wire in PollVoteUpdatedPayload and the realtime delivery frame, kept
// separate from polls.Option (which also carries label/position, not
// needed on every vote update since the client already has those from its
// initial GET .../polls/{poll_id}).
type PollOptionTally struct {
	OptionID  uuid.UUID `json:"option_id"`
	VoteCount int       `json:"vote_count"`
}

// PollVoteUpdatedPayload carries a poll's *fresh* denormalized tallies
// after one user's vote changed — same "durable event carries everything
// needed" shape as ReactionUpdatedPayload, so a connected client patches
// its local copy of the poll without re-fetching it. EventID exists for
// the same redelivery-dedup reason as ReactionUpdatedPayload's.
type PollVoteUpdatedPayload struct {
	EventID     uuid.UUID          `json:"event_id"`
	ChannelID   uuid.UUID          `json:"channel_id"`
	PollID      uuid.UUID          `json:"poll_id"`
	ActorID     uuid.UUID          `json:"actor_id"`
	Options     []PollOptionTally  `json:"options"`
	TotalVoters int                `json:"total_voters"`
}

const TopicMessageEdited = "message.edited"

// MessageEditedPayload carries a message's fresh body/edited_at after its
// sender edited it — same "durable event carries everything needed" shape
// as ReactionUpdatedPayload, so a connected client patches its local copy
// in place instead of re-fetching. EventID exists for the same
// redelivery-dedup reason as ReactionUpdatedPayload's: a message can
// legitimately be edited more than once, so (channel_id, message_id) alone
// can't dedup a specific edit the way (channel_id, sequence) dedups a send.
type MessageEditedPayload struct {
	EventID   uuid.UUID `json:"event_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	MessageID uuid.UUID `json:"message_id"`
	SenderID  uuid.UUID `json:"sender_id"`
	Body      string    `json:"body"`
	EditedAt  time.Time `json:"edited_at"`
}

const TopicMessagePinUpdated = "message.pin_updated"

// MessagePinUpdatedPayload carries a message's fresh pinned state after a
// pin or unpin — same "durable event carries everything needed" shape as
// ReactionUpdatedPayload/PollVoteUpdatedPayload, with the same Action
// string convention as ReactionUpdatedPayload's ("pinned"/"unpinned"
// instead of "added"/"removed"). PinnedAt/PinnedBy are both nil on an
// "unpinned" event (the row's fresh state, which is now cleared) and both
// set on a "pinned" event; a consumer patches its local copy of the
// message from these two fields directly rather than just toggling a
// boolean, so it never has to guess who pinned it. EventID exists for the
// same redelivery-dedup reason as ReactionUpdatedPayload's: a message can
// legitimately be pinned and unpinned more than once, so (channel_id,
// message_id) alone can't dedup a specific pin/unpin the way (channel_id,
// sequence) dedups a send.
type MessagePinUpdatedPayload struct {
	EventID   uuid.UUID  `json:"event_id"`
	ChannelID uuid.UUID  `json:"channel_id"`
	MessageID uuid.UUID  `json:"message_id"`
	ActorID   uuid.UUID  `json:"actor_id"`
	Action    string     `json:"action"` // "pinned" | "unpinned"
	PinnedAt  *time.Time `json:"pinned_at"`
	PinnedBy  *uuid.UUID `json:"pinned_by"`
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

const TopicMessageReminderDue = "message_reminder.due"

// MessageReminderDuePayload carries one due reminder — the "message_reminders"
// capability. Addressed to exactly one user (UserID), unlike every other
// event in this file which broadcasts to a channel's whole membership; see
// internal/realtime.Delivery.ToUser, the single-recipient delivery path
// this event type is the reason for.
type MessageReminderDuePayload struct {
	ReminderID uuid.UUID `json:"reminder_id"`
	ChannelID  uuid.UUID `json:"channel_id"`
	MessageID  uuid.UUID `json:"message_id"`
	UserID     uuid.UUID `json:"user_id"`
}

const TopicUnreadReminderDue = "unread_reminder.due"

// UnreadReminderDuePayload notifies one member that they have unread
// messages in a channel — the "unread_reminders" capability. Also
// single-recipient, same as MessageReminderDuePayload. LastReadSequence and
// LatestSequence are both carried so a client can render "N unread" without
// a follow-up fetch.
type UnreadReminderDuePayload struct {
	ChannelID       uuid.UUID `json:"channel_id"`
	UserID          uuid.UUID `json:"user_id"`
	LastReadSequence int64    `json:"last_read_sequence"`
	LatestSequence   int64    `json:"latest_sequence"`
}

const TopicCustomEvent = "custom.event"

// CustomEventPayload carries a client-supplied event through the same
// outbox->Kafka->fanout pipeline every other realtime event in this
// codebase uses — the "custom_events" capability
// (migrations/control/0012_channel_capabilities.sql). Unlike
// MessageCreatedPayload/ReactionUpdatedPayload/etc., Data is fully
// caller-defined: this API doesn't interpret or validate it beyond "valid
// JSON," it only authenticates who sent it (SenderID) and which channel it
// belongs to (ChannelID, inherited from the outbox row itself) before
// relaying it to every other member's socket. EventType is a caller-chosen
// string (e.g. "reaction.custom", "game.move") so a client's frame
// dispatcher can tell different custom events apart without inspecting
// Data's shape first.
type CustomEventPayload struct {
	EventID   uuid.UUID       `json:"event_id"`
	ChannelID uuid.UUID       `json:"channel_id"`
	SenderID  uuid.UUID       `json:"sender_id"`
	EventType string          `json:"event_type"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
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
