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

// ErrParentNotFound and ErrThreadDepthExceeded are Send's two thread-specific
// failure modes, both checked (checkThreadDepth) before a sequence number is
// ever assigned, so a rejected reply never burns one.
var (
	ErrParentNotFound      = errors.New("parent message not found in this channel")
	ErrThreadDepthExceeded = errors.New("reply would exceed this app's max thread depth")
)

// ErrNotMessageOwner is Edit's authorization failure: messageID exists in
// channelID, but editorID isn't its sender. Checked against the row's
// actual sender_id inside Edit itself (never trusted from the caller) —
// same "the database is the source of truth" discipline as
// checkChannelWriteAccess re-verifying route.AppID against the caller's app.
var ErrNotMessageOwner = errors.New("messages: only the sender may edit this message")

// Status values for Message.Status — see the "pending_messages" capability
// and migrations/shard/0011_channel_capabilities.sql's doc comment. Every
// message sent while an app's pending_messages capability is off is created
// directly as StatusSent; there is no other transition into StatusPending
// today besides Send opting into it at creation time (no separate "submit
// for review" endpoint exists yet — see cmd/api/handlers_moderation.go).
const (
	StatusSent    = "sent"
	StatusPending = "pending"
)

// Attachment is one client-supplied file/media reference — the "uploads"
// capability. This API never hosts files itself; an app integrates its own
// object storage (S3/GCS/CDN) and sends the resulting URL here (see
// migrations/shard/0011's doc comment on the attachments column).
type Attachment struct {
	URL       string `json:"url"`
	Type      string `json:"type,omitempty"`
	Filename  string `json:"filename,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// LinkPreview is the "url_enrichment" capability's best-effort metadata for
// the first URL found in a message's body — filled in asynchronously after
// the message is created (see cmd/api's enrichLinkPreview) by a fire-and-
// forget fetch that never blocks or fails the send itself. Nil until that
// fetch completes, or forever if it never does (disabled, no URL in the
// body, fetch failed/timed out).
type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// Location is the "location_sharing" capability's optional point shared via
// a message send.
type Location struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type Message struct {
	ChannelID       uuid.UUID
	Sequence        int64
	MessageID       uuid.UUID
	SenderID        uuid.UUID
	ClientMessageID uuid.UUID
	Body            string
	CreatedAt       time.Time
	// ParentID is nil for a top-level message, or the message_id this one
	// replies to (migrations/shard's thread migration) — always in the same
	// channel (enforced by the composite FK), never reassigned after
	// creation. Nesting depth isn't stored: it's derived by walking parent_id
	// links (see checkThreadDepth), the same "compute, don't denormalize a
	// value that's cheap to recompute and would otherwise need invalidating"
	// choice as everywhere else in this codebase that could cache but doesn't.
	ParentID *uuid.UUID
	// ReplyCount is how many messages have parent_id = this message's
	// message_id — denormalized on the row itself
	// (migrations/shard/0008_reply_count.sql) and kept current by Send's
	// single atomic increment against the parent every time a reply is
	// created, the same "never join to render it" discipline as
	// ReactionCounts/LatestReactions below. Always 0 for a message with no
	// replies yet, including one that is itself a reply.
	ReplyCount int64
	// PollID is nil unless this message has a poll attached
	// (migrations/shard/0007_polls.sql) — set by cmd/api's handleSendMessage
	// after it's already confirmed the poll exists in this channel
	// (internal/polls.Repo.Exists), so Send itself does no poll-existence
	// check the way it does for ParentID/checkThreadDepth: a poll is a
	// genuinely separate entity (internal/polls), not an intrinsic property
	// of the messages table the way thread nesting is, so that check
	// belongs at the API orchestration layer instead of baked into this
	// package. The messages.poll_id -> polls(channel_id, poll_id) foreign
	// key is still the actual source of truth against a race.
	PollID *uuid.UUID
	// ReactionCounts/LatestReactions are denormalized on the row itself
	// (migrations/shard/0002_reactions.sql) and kept current by
	// internal/reactions on every add/remove — reading a message never
	// joins message_reactions.
	ReactionCounts  map[string]int
	LatestReactions []events.ReactionSummary
	// EditedAt is nil for a message that's never been edited, or the UTC
	// time of its most recent edit (migrations/shard/0009_message_edit.sql)
	// — set by Edit, which overwrites Body and this column together. No
	// edit history is kept, only current state, same as everything else
	// denormalized on this row.
	EditedAt *time.Time
	// PinnedAt is nil for a message that isn't currently pinned, or the
	// UTC time it was last pinned (migrations/shard/0010_message_pins.sql).
	// PinnedBy is who pinned it — meaningful only alongside a non-nil
	// PinnedAt. Unlike reactions/bookmarks, a pin is channel-shared
	// single-state, not per-user: any channel member can pin or unpin (see
	// checkChannelWriteAccess and Pin/Unpin's doc comments), so there's
	// exactly one "is this pinned" answer per message, not one per viewer.
	PinnedAt *time.Time
	PinnedBy *uuid.UUID
	// QuotedMessageID is nil unless this message quotes another message in
	// the same channel — the "quotes" capability
	// (migrations/shard/0011_channel_capabilities.sql). Validated at the
	// application layer (cmd/api, via Exists, same channel only) before
	// Send is ever called; there's no DB FK the way parent_id also lacks
	// one (see that column's own doc comment for why), so Send trusts the
	// caller here exactly like it trusts pollID.
	QuotedMessageID *uuid.UUID
	// Attachments is always a non-nil (possibly empty) slice — the
	// "uploads" capability. See Attachment's doc comment for scope.
	Attachments []Attachment
	// LinkPreview is nil until url_enrichment's async fetch fills it in (or
	// forever, if url_enrichment is off for this app, the body had no URL,
	// or the fetch failed/timed out).
	LinkPreview *LinkPreview
	// Location is nil unless this message shared a location — the
	// "location_sharing" capability.
	Location *Location
	// Status is StatusSent (the default, immediately visible to every
	// member) or StatusPending (visible only to its own sender until an
	// app-side moderator approves it) — see the "pending_messages"
	// capability. Every send while that capability is off is StatusSent.
	Status string
}

type Repo struct{}

func NewRepo() *Repo {
	return &Repo{}
}

// Send is idempotent on (channel_id, client_message_id): a retried send
// returns the original message with created=false instead of creating a
// duplicate (INSTRUCTIONS.md §19). Sequence assignment locks only the
// single channel_sequences row for this channel, never the whole table.
//
// parentID is nil for a top-level message, or the message being replied to.
// When set, checkThreadDepth both confirms it exists in this channel AND
// that replying to it stays within maxDepth (0 = unlimited) — checked
// BEFORE a sequence number is assigned, so a rejected reply never burns one
// the way a rejected insert further down still would.
//
// pollID is nil for a message with no poll attached, or a poll_id the
// caller has already confirmed exists in this channel (see PollID's doc
// comment) — Send trusts it and simply stores it; a bad pollID would
// surface as the messages.poll_id foreign key violation instead of a clean
// application error, which is why the caller checks first.
//
// quotedMessageID follows the exact same caller-validated-first contract as
// pollID (see QuotedMessageID's doc comment) — nil for no quote, or a
// message_id the caller has already confirmed exists in this channel.
//
// attachments is stored as-is (nil is normalized to an empty slice before
// marshaling, never a SQL NULL, matching the column's NOT NULL DEFAULT
// '[]'). location is nil for no shared location. status is StatusSent
// unless the caller (having already checked the app's pending_messages
// capability) passes StatusPending.
func (r *Repo) Send(ctx context.Context, pool *pgxpool.Pool, channelID, senderID, clientMessageID uuid.UUID, body string, parentID *uuid.UUID, maxDepth int, pollID *uuid.UUID, quotedMessageID *uuid.UUID, attachments []Attachment, location *Location, status string) (msg Message, created bool, err error) {
	if attachments == nil {
		attachments = []Attachment{}
	}
	if status == "" {
		status = StatusSent
	}
	attachmentsJSON, err := json.Marshal(attachments)
	if err != nil {
		return Message{}, false, fmt.Errorf("messages: marshal attachments: %w", err)
	}
	var locationJSON []byte
	if location != nil {
		locationJSON, err = json.Marshal(location)
		if err != nil {
			return Message{}, false, fmt.Errorf("messages: marshal location: %w", err)
		}
	}
	if existing, ok, ferr := r.getByClientMessageID(ctx, pool, channelID, clientMessageID); ferr != nil {
		return Message{}, false, ferr
	} else if ok {
		return existing, false, nil
	}

	if parentID != nil {
		if err := r.checkThreadDepth(ctx, pool, channelID, *parentID, maxDepth); err != nil {
			return Message{}, false, err
		}
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
		INSERT INTO messages (channel_id, sequence, message_id, sender_id, client_message_id, body, parent_id, poll_id, created_at, quoted_message_id, attachments, location, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, channelID, sequence, messageID, senderID, clientMessageID, body, parentID, pollID, now, quotedMessageID, attachmentsJSON, locationJSON, status); err != nil {
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

	// A reply bumps its parent's denormalized reply_count by exactly one,
	// atomically, in the same transaction as the reply's own insert — see
	// ReplyCount's doc comment on why this is a plain increment rather than
	// reactions' recompute-from-source pattern. The parent is guaranteed to
	// exist here: checkThreadDepth already confirmed it moments ago, and
	// the INSERT just above only succeeded because fk_messages_parent
	// (migrations/shard/0006_message_threads.sql) validated the same thing
	// inside this very transaction.
	var parentReplyCount *int64
	if parentID != nil {
		var count int64
		if err := tx.QueryRow(ctx, `
			UPDATE messages SET reply_count = reply_count + 1
			WHERE channel_id = $1 AND message_id = $2
			RETURNING reply_count
		`, channelID, *parentID).Scan(&count); err != nil {
			return Message{}, false, fmt.Errorf("messages: increment parent reply count: %w", err)
		}
		parentReplyCount = &count
	}

	// A pending message (the "pending_messages" capability) deliberately
	// does NOT get a message.created event here — it isn't visible to
	// anyone but its own sender yet, so nothing should be delivered to the
	// rest of the channel until a moderator approves it (see Approve,
	// which emits this same event itself once that happens). A sender
	// still sees their own pending message immediately via this call's own
	// return value, exactly as it would for a normal send.
	if status != StatusPending {
		payload := events.MessageCreatedPayload{
			MessageID:        messageID,
			ChannelID:        channelID,
			SenderID:         senderID,
			ClientMessageID:  clientMessageID,
			Sequence:         sequence,
			Body:             body,
			ParentID:         parentID,
			ParentReplyCount: parentReplyCount,
			PollID:           pollID,
			CreatedAt:        now,
		}
		if err := events.InsertOutbox(ctx, tx, events.TopicMessageCreated, channelID, payload); err != nil {
			return Message{}, false, err
		}
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
		ParentID:        parentID,
		ReplyCount:      0,
		PollID:          pollID,
		CreatedAt:       now,
		ReactionCounts:  map[string]int{},
		LatestReactions: []events.ReactionSummary{},
		QuotedMessageID: quotedMessageID,
		Attachments:     attachments,
		Location:        location,
		Status:          status,
	}, true, nil
}

// checkThreadDepth confirms parentID exists in this channel and, unless
// maxDepth<=0 (unlimited), that replying to it wouldn't exceed it. One
// recursive query walking parent_id all the way to the root computes the
// parent's own depth (root=1) in a single round trip, rather than this
// package doing up to maxDepth separate point lookups — the number of rows
// Postgres walks is the same either way, but the round trips aren't, and an
// app is free to configure a large max_thread_depth.
//
// No cycle-termination guard is needed: a message's parent_id can only ever
// reference a message that already existed at send time (this very check),
// so the parent_id graph is a DAG by construction — nothing already written
// can point at something created after it.
func (r *Repo) checkThreadDepth(ctx context.Context, pool *pgxpool.Pool, channelID, parentID uuid.UUID, maxDepth int) error {
	var parentDepth *int
	err := pool.QueryRow(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT message_id, parent_id, 1 AS depth
			FROM messages
			WHERE channel_id = $1 AND message_id = $2
			UNION ALL
			SELECT m.message_id, m.parent_id, a.depth + 1
			FROM messages m
			JOIN ancestors a ON m.message_id = a.parent_id
			WHERE m.channel_id = $1
		)
		SELECT max(depth) FROM ancestors
	`, channelID, parentID).Scan(&parentDepth)
	if err != nil {
		return fmt.Errorf("messages: check thread depth: %w", err)
	}
	// max(depth) over zero rows (no such parent in this channel) is SQL
	// NULL, not "no rows" — QueryRow still returns exactly one row here,
	// so this is the actual "not found" signal, not a pgx.ErrNoRows case.
	if parentDepth == nil {
		return ErrParentNotFound
	}
	if maxDepth > 0 && *parentDepth+1 > maxDepth {
		return ErrThreadDepthExceeded
	}
	return nil
}

// Edit overwrites messageID's body and stamps edited_at, but only when
// editorID is the message's own sender — verified against the row's actual
// sender_id (never trusted from the caller), the same "read back and check
// live state" discipline checkChannelWriteAccess uses for route.AppID.
// Whether editing is allowed at all for this app
// (apps.App.MessageEditEnabled) is a caller concern (cmd/api's
// handleEditMessage), not this package's — same layering as PollID's
// existence check in Send.
func (r *Repo) Edit(ctx context.Context, pool *pgxpool.Pool, channelID, messageID, editorID uuid.UUID, newBody string) (Message, error) {
	var actualSender uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT sender_id FROM messages WHERE channel_id = $1 AND message_id = $2
	`, channelID, messageID).Scan(&actualSender); err != nil {
		if err == pgx.ErrNoRows {
			return Message{}, ErrNotFound
		}
		return Message{}, fmt.Errorf("messages: load for edit: %w", err)
	}
	if actualSender != editorID {
		return Message{}, ErrNotMessageOwner
	}

	now := time.Now().UTC()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Message{}, fmt.Errorf("messages: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var m Message
	var countsRaw, latestRaw, attachmentsRaw, linkPreviewRaw, locationRaw []byte
	// sender_id is filtered again here, not just relied on from the check
	// above — a defense-in-depth guard against a TOCTOU race, even though
	// nothing in this codebase can currently change a message's sender_id
	// or delete it out from under a concurrent edit.
	err = tx.QueryRow(ctx, `
		UPDATE messages SET body = $1, edited_at = $2
		WHERE channel_id = $3 AND message_id = $4 AND sender_id = $5
		RETURNING `+messageColumns+`
	`, newBody, now, channelID, messageID, editorID).Scan(
		&m.ChannelID, &m.Sequence, &m.MessageID, &m.SenderID, &m.ClientMessageID, &m.Body, &m.ParentID, &m.ReplyCount, &m.PollID, &m.CreatedAt, &m.EditedAt, &countsRaw, &latestRaw, &m.PinnedAt, &m.PinnedBy,
		&m.QuotedMessageID, &attachmentsRaw, &linkPreviewRaw, &locationRaw, &m.Status,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Message{}, ErrNotFound
		}
		return Message{}, fmt.Errorf("messages: update: %w", err)
	}
	if err := unmarshalReactionState(&m, countsRaw, latestRaw); err != nil {
		return Message{}, err
	}
	if err := unmarshalMessageExtras(&m, attachmentsRaw, linkPreviewRaw, locationRaw); err != nil {
		return Message{}, err
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("messages: generate event id: %w", err)
	}
	payload := events.MessageEditedPayload{
		EventID:   eventID,
		ChannelID: channelID,
		MessageID: messageID,
		SenderID:  editorID,
		Body:      newBody,
		EditedAt:  now,
	}
	if err := events.InsertOutboxWithID(ctx, tx, eventID, events.TopicMessageEdited, channelID, payload); err != nil {
		return Message{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Message{}, fmt.Errorf("messages: commit: %w", err)
	}

	return m, nil
}

// Search returns up to limit messages in channelID whose body matches query
// (case-insensitive substring), newest first — the "search" capability.
// Deliberately simple: a single ILIKE '%query%' against messages.body, no
// full-text index, ranking, or tokenization. That's an intentional scope
// choice (see migrations/shard/0011's neighboring capabilities for the same
// "honest, minimal implementation" standard) rather than an oversight — a
// production deployment expecting heavy search volume would want a real
// text index (Postgres tsvector, or an external search service), but nothing
// in this codebase's message volume assumptions demands that yet, and
// ILIKE is enough to make the capability genuinely usable. No cursor
// pagination the way ListBefore has, matching ListPinned's precedent: a
// bounded limit is enough for a feature that isn't the primary read path.
func (r *Repo) Search(ctx context.Context, pool *pgxpool.Pool, channelID uuid.UUID, query string, limit int) ([]Message, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+messageColumns+`
		FROM messages
		WHERE channel_id = $1 AND body ILIKE '%' || $2 || '%'
		ORDER BY sequence DESC
		LIMIT $3
	`, channelID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("messages: search: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessageRow(rows)
		if err != nil {
			return nil, fmt.Errorf("messages: scan search result: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListPending returns a channel's currently-pending messages, oldest first —
// the moderation queue for the "pending_messages" capability
// (cmd/api/handlers_moderation.go). Backed by idx_messages_channel_pending
// (migrations/shard/0011_channel_capabilities.sql), never a scan of the
// full message log.
func (r *Repo) ListPending(ctx context.Context, pool *pgxpool.Pool, channelID uuid.UUID, limit int) ([]Message, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+messageColumns+`
		FROM messages
		WHERE channel_id = $1 AND status = 'pending'
		ORDER BY sequence ASC
		LIMIT $2
	`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("messages: list pending: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessageRow(rows)
		if err != nil {
			return nil, fmt.Errorf("messages: scan pending: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Approve flips a pending message to sent and, only now, emits the
// message.created event every other member's client has been waiting on —
// see Send's doc comment on why that event was withheld at creation time.
// ok=false (not an error) means there was nothing to approve: either the
// message doesn't exist, or it's not currently pending (already approved,
// or never was). ParentReplyCount is deliberately left nil on this
// delayed event, unlike Send's — the parent's reply_count may have moved
// since this message was created (other replies could have landed in the
// meantime), and Approve has no fresh read of it to report; a client that
// needs the parent's current count re-fetches it, same as it would for any
// other out-of-band change.
func (r *Repo) Approve(ctx context.Context, pool *pgxpool.Pool, channelID, messageID uuid.UUID) (msg Message, ok bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Message{}, false, fmt.Errorf("messages: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	m, err := scanMessageRow(tx.QueryRow(ctx, `
		UPDATE messages SET status = 'sent'
		WHERE channel_id = $1 AND message_id = $2 AND status = 'pending'
		RETURNING `+messageColumns+`
	`, channelID, messageID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return Message{}, false, nil
		}
		return Message{}, false, fmt.Errorf("messages: approve: %w", err)
	}

	payload := events.MessageCreatedPayload{
		MessageID:       m.MessageID,
		ChannelID:       m.ChannelID,
		SenderID:        m.SenderID,
		ClientMessageID: m.ClientMessageID,
		Sequence:        m.Sequence,
		Body:            m.Body,
		ParentID:        m.ParentID,
		PollID:          m.PollID,
		CreatedAt:       m.CreatedAt,
	}
	if err := events.InsertOutbox(ctx, tx, events.TopicMessageCreated, channelID, payload); err != nil {
		return Message{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Message{}, false, fmt.Errorf("messages: commit: %w", err)
	}
	return m, true, nil
}

// Reject permanently deletes a pending message — since it was never
// delivered to anyone but its own sender, there's nothing to emit and no
// trace left for other members, unlike deleting an already-sent message
// (which this codebase has no endpoint for at all). removed=false is a 404
// signal: either it doesn't exist, or it's no longer pending (e.g. already
// approved). Known, accepted gap: if the rejected message was itself a
// reply, its parent's denormalized reply_count (bumped at Send time, see
// Send's doc comment) is not decremented here — a rejected reply is rare
// enough, and the count drift small enough, that this hasn't been worth
// the extra transactional complexity to correct.
func (r *Repo) Reject(ctx context.Context, pool *pgxpool.Pool, channelID, messageID uuid.UUID) (removed bool, err error) {
	tag, err := pool.Exec(ctx, `
		DELETE FROM messages WHERE channel_id = $1 AND message_id = $2 AND status = 'pending'
	`, channelID, messageID)
	if err != nil {
		return false, fmt.Errorf("messages: reject: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetLinkPreview stores the "url_enrichment" capability's best-effort
// fetched metadata for a message — called once, asynchronously, shortly
// after Send by cmd/api's enrichLinkPreview goroutine (never inside Send's
// own transaction: the fetch itself can take seconds, far too long to hold
// a row lock or delay the send response for). No realtime event is emitted
// for this update — deliberately out of scope for now, see
// migrations/shard/0011's doc comment; a connected client picks it up the
// next time it re-lists/re-fetches the message. A message that no longer
// exists by the time the fetch completes (e.g. already deleted by
// retention) is a silent no-op, not an error — nothing meaningful to do at
// that point.
func (r *Repo) SetLinkPreview(ctx context.Context, pool *pgxpool.Pool, channelID, messageID uuid.UUID, preview *LinkPreview) error {
	data, err := json.Marshal(preview)
	if err != nil {
		return fmt.Errorf("messages: marshal link_preview: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE messages SET link_preview = $1 WHERE channel_id = $2 AND message_id = $3
	`, data, channelID, messageID); err != nil {
		return fmt.Errorf("messages: set link_preview: %w", err)
	}
	return nil
}

// messageColumns is the full column list every "read back a whole message
// row" query in this file selects, in Scan order — factored out once here
// (Pin/Unpin/ListPinned/getByMessageID all need it) rather than duplicated
// a fourth and fifth time the way Edit/getByClientMessageID/ListBefore's
// pre-existing copies already are.
const messageColumns = "channel_id, sequence, message_id, sender_id, client_message_id, body, parent_id, reply_count, poll_id, created_at, edited_at, reaction_counts, latest_reactions, pinned_at, pinned_by, quoted_message_id, attachments, link_preview, location, status"

func scanMessageRow(row pgx.Row) (Message, error) {
	var m Message
	var countsRaw, latestRaw, attachmentsRaw, linkPreviewRaw, locationRaw []byte
	err := row.Scan(
		&m.ChannelID, &m.Sequence, &m.MessageID, &m.SenderID, &m.ClientMessageID, &m.Body, &m.ParentID, &m.ReplyCount, &m.PollID, &m.CreatedAt, &m.EditedAt, &countsRaw, &latestRaw, &m.PinnedAt, &m.PinnedBy,
		&m.QuotedMessageID, &attachmentsRaw, &linkPreviewRaw, &locationRaw, &m.Status,
	)
	if err != nil {
		return Message{}, err
	}
	if err := unmarshalReactionState(&m, countsRaw, latestRaw); err != nil {
		return Message{}, err
	}
	if err := unmarshalMessageExtras(&m, attachmentsRaw, linkPreviewRaw, locationRaw); err != nil {
		return Message{}, err
	}
	return m, nil
}

// getByMessageID reads back a message's full current row by its server-
// assigned message_id — the same shape as getByClientMessageID, keyed
// differently. Used by Pin/Unpin's idempotent no-op path (see their doc
// comments) to tell "already in the requested state" apart from "no such
// message" without a separate existence check.
func (r *Repo) getByMessageID(ctx context.Context, pool *pgxpool.Pool, channelID, messageID uuid.UUID) (Message, bool, error) {
	m, err := scanMessageRow(pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE channel_id = $1 AND message_id = $2`, channelID, messageID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return Message{}, false, nil
		}
		return Message{}, false, fmt.Errorf("messages: lookup by message_id: %w", err)
	}
	return m, true, nil
}

// Exists reports whether messageID is a real message in channelID — used
// by callers outside this package that need to validate a message
// reference against a *different* physical database (internal/bookmarks
// lives in the control-plane DB, so it can't enforce this with a foreign
// key the way message_reactions/polls do within the shard DB itself; see
// internal/bookmarks' package doc comment). Not used by Pin/Unpin, which
// get existence for free from their UPDATE's RowsAffected/getByMessageID
// fallback instead of a separate check.
func (r *Repo) Exists(ctx context.Context, pool *pgxpool.Pool, channelID, messageID uuid.UUID) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE channel_id = $1 AND message_id = $2)`, channelID, messageID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("messages: exists: %w", err)
	}
	return exists, nil
}

// Pin marks messageID as pinned in channelID, recording actorID as who
// pinned it. Any channel member may pin (checkChannelWriteAccess is the
// only gate cmd/api applies) — this codebase has no "channel owner" role
// distinct from "member" for end-user actions (POST
// /channels/{id}/members already grants that to any existing member), so
// pinning follows the same "any member" model rather than inventing a new
// permission tier just for this feature.
//
// Idempotent: pinning an already-pinned message is a no-op (changed=false)
// that leaves the existing pinned_at/pinned_by untouched rather than
// re-stamping a new pinner/time — the same "repeats are free, only a
// genuine state change emits an event" shape as reactions.Repo.Add.
func (r *Repo) Pin(ctx context.Context, pool *pgxpool.Pool, channelID, messageID, actorID uuid.UUID) (Message, bool, error) {
	return r.setPinned(ctx, pool, channelID, messageID, actorID, true)
}

// Unpin clears messageID's pinned state. Like Pin, any channel member may
// unpin — there's no concept of "only the pinner can unpin," the same way
// there's no such restriction on who may remove a member once added.
// Idempotent: unpinning a message that isn't pinned is a no-op
// (changed=false).
func (r *Repo) Unpin(ctx context.Context, pool *pgxpool.Pool, channelID, messageID, actorID uuid.UUID) (Message, bool, error) {
	return r.setPinned(ctx, pool, channelID, messageID, actorID, false)
}

// setPinned backs both Pin and Unpin — same transactional
// update-then-emit-event shape as Edit, just toggling pinned_at/pinned_by
// instead of body/edited_at. actorID is only stored when pinning (Unpin
// clears pinned_by along with pinned_at, since there's no "who unpinned it"
// column to keep) but is always the ActorID on the emitted event either
// way, so a realtime consumer knows who performed *this* action even
// though the row itself doesn't retain an unpin history.
func (r *Repo) setPinned(ctx context.Context, pool *pgxpool.Pool, channelID, messageID, actorID uuid.UUID, pin bool) (Message, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Message{}, false, fmt.Errorf("messages: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var m Message
	if pin {
		now := time.Now().UTC()
		m, err = scanMessageRow(tx.QueryRow(ctx, `
			UPDATE messages SET pinned_at = $1, pinned_by = $2
			WHERE channel_id = $3 AND message_id = $4 AND pinned_at IS NULL
			RETURNING `+messageColumns+`
		`, now, actorID, channelID, messageID))
	} else {
		m, err = scanMessageRow(tx.QueryRow(ctx, `
			UPDATE messages SET pinned_at = NULL, pinned_by = NULL
			WHERE channel_id = $1 AND message_id = $2 AND pinned_at IS NOT NULL
			RETURNING `+messageColumns+`
		`, channelID, messageID))
	}
	if err != nil {
		if err != pgx.ErrNoRows {
			return Message{}, false, fmt.Errorf("messages: update pinned state: %w", err)
		}
		// Zero rows updated: either already in the requested state, or the
		// message doesn't exist at all — read current state (within this
		// same tx, so it sees a consistent snapshot) to tell which, the
		// same "no-op path reads current state instead of assuming" shape
		// as reactions.Repo.currentState.
		m, err = scanMessageRow(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE channel_id = $1 AND message_id = $2`, channelID, messageID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return Message{}, false, ErrNotFound
			}
			return Message{}, false, fmt.Errorf("messages: read current pinned state: %w", err)
		}
		return m, false, tx.Commit(ctx)
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return Message{}, false, fmt.Errorf("messages: generate event id: %w", err)
	}
	action := "unpinned"
	if pin {
		action = "pinned"
	}
	payload := events.MessagePinUpdatedPayload{
		EventID: eventID, ChannelID: channelID, MessageID: messageID, ActorID: actorID,
		Action: action, PinnedAt: m.PinnedAt, PinnedBy: m.PinnedBy,
	}
	if err := events.InsertOutboxWithID(ctx, tx, eventID, events.TopicMessagePinUpdated, channelID, payload); err != nil {
		return Message{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Message{}, false, fmt.Errorf("messages: commit: %w", err)
	}
	return m, true, nil
}

// ListPinned returns a channel's currently-pinned messages, most-recently-
// pinned first — backed entirely by idx_messages_channel_pinned
// (migrations/shard/0010_message_pins.sql), never a scan of the full
// message log. No cursor pagination the way ListBefore has: a channel's
// pinned set is expected to stay small (pinning is a deliberate, one-at-a-
// time action, nothing like message volume), so limit alone is enough.
func (r *Repo) ListPinned(ctx context.Context, pool *pgxpool.Pool, channelID uuid.UUID, limit int) ([]Message, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+messageColumns+`
		FROM messages
		WHERE channel_id = $1 AND pinned_at IS NOT NULL
		ORDER BY pinned_at DESC
		LIMIT $2
	`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("messages: list pinned: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessageRow(rows)
		if err != nil {
			return nil, fmt.Errorf("messages: scan pinned: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repo) getByClientMessageID(ctx context.Context, pool *pgxpool.Pool, channelID, clientMessageID uuid.UUID) (Message, bool, error) {
	var m Message
	var countsRaw, latestRaw, attachmentsRaw, linkPreviewRaw, locationRaw []byte
	err := pool.QueryRow(ctx, `
		SELECT `+messageColumns+`
		FROM messages WHERE channel_id = $1 AND client_message_id = $2
	`, channelID, clientMessageID).Scan(
		&m.ChannelID, &m.Sequence, &m.MessageID, &m.SenderID, &m.ClientMessageID, &m.Body, &m.ParentID, &m.ReplyCount, &m.PollID, &m.CreatedAt, &m.EditedAt, &countsRaw, &latestRaw, &m.PinnedAt, &m.PinnedBy,
		&m.QuotedMessageID, &attachmentsRaw, &linkPreviewRaw, &locationRaw, &m.Status,
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
	if err := unmarshalMessageExtras(&m, attachmentsRaw, linkPreviewRaw, locationRaw); err != nil {
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
		SELECT `+messageColumns+`
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
		var countsRaw, latestRaw, attachmentsRaw, linkPreviewRaw, locationRaw []byte
		if err := rows.Scan(&m.ChannelID, &m.Sequence, &m.MessageID, &m.SenderID, &m.ClientMessageID, &m.Body, &m.ParentID, &m.ReplyCount, &m.PollID, &m.CreatedAt, &m.EditedAt, &countsRaw, &latestRaw, &m.PinnedAt, &m.PinnedBy,
			&m.QuotedMessageID, &attachmentsRaw, &linkPreviewRaw, &locationRaw, &m.Status); err != nil {
			return nil, fmt.Errorf("messages: scan: %w", err)
		}
		if err := unmarshalReactionState(&m, countsRaw, latestRaw); err != nil {
			return nil, err
		}
		if err := unmarshalMessageExtras(&m, attachmentsRaw, linkPreviewRaw, locationRaw); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteExpiredBefore removes one channel's messages older than cutoff, in
// batches of at most batchLimit rows per statement so a channel with a
// large backlog past its plan's retention window doesn't hold one
// long-running delete against it (INSTRUCTIONS.md §45: predictable latency
// over raw throughput). Called only by the per-shard retention sweep
// (cmd/worker), never on a request path — the returned count is for that
// sweep's own logging/metrics.
func (r *Repo) DeleteExpiredBefore(ctx context.Context, pool *pgxpool.Pool, channelID uuid.UUID, cutoff time.Time, batchLimit int) (int64, error) {
	var total int64
	for {
		tag, err := pool.Exec(ctx, `
			DELETE FROM messages
			WHERE (channel_id, sequence) IN (
				SELECT channel_id, sequence FROM messages
				WHERE channel_id = $1 AND created_at < $2
				ORDER BY sequence
				LIMIT $3
			)
		`, channelID, cutoff, batchLimit)
		if err != nil {
			return total, fmt.Errorf("messages: delete expired: %w", err)
		}
		n := tag.RowsAffected()
		total += n
		if n < int64(batchLimit) {
			return total, nil
		}
	}
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

// DailyCount is one channel's message count for one UTC calendar day
// (Day is truncated to midnight UTC).
type DailyCount struct {
	ChannelID uuid.UUID
	Day       time.Time
	Count     int64
}

// CountDailyByChannels backs the dashboard's per-app daily message
// sparkline (Apps grid): message counts per channel per UTC calendar day,
// for activity on or after `since`. Unlike SumSequencesByChannels (which
// reads the running channel_sequences counter and never touches the
// messages table), a day-bucketed count has no cheaper source and scans
// messages — bounded by the (channel_id, created_at) index (see
// migrations/shard/0005_message_retention_index.sql) and callers keeping
// `since` to a handful of days, so this stays a low-frequency admin read,
// same class as SumSequencesByChannels.
func (r *Repo) CountDailyByChannels(ctx context.Context, pool *pgxpool.Pool, channelIDs []uuid.UUID, since time.Time) ([]DailyCount, error) {
	if len(channelIDs) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT channel_id, date_trunc('day', created_at) AS day, count(*)
		FROM messages
		WHERE channel_id = ANY($1) AND created_at >= $2
		GROUP BY channel_id, day
	`, channelIDs, since)
	if err != nil {
		return nil, fmt.Errorf("messages: count daily by channels: %w", err)
	}
	defer rows.Close()

	var out []DailyCount
	for rows.Next() {
		var dc DailyCount
		if err := rows.Scan(&dc.ChannelID, &dc.Day, &dc.Count); err != nil {
			return nil, fmt.Errorf("messages: count daily by channels: %w", err)
		}
		out = append(out, dc)
	}
	return out, rows.Err()
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

// unmarshalMessageExtras fills in the four columns added by
// migrations/shard/0011_channel_capabilities.sql. attachments is NOT NULL
// (always valid JSON, defaulting to '[]'); link_preview and location are
// nullable, so a zero-length scan ([]byte(nil), meaning SQL NULL) leaves
// the corresponding pointer field nil instead of being unmarshaled.
func unmarshalMessageExtras(m *Message, attachmentsRaw, linkPreviewRaw, locationRaw []byte) error {
	if err := json.Unmarshal(attachmentsRaw, &m.Attachments); err != nil {
		return fmt.Errorf("messages: unmarshal attachments: %w", err)
	}
	if len(linkPreviewRaw) > 0 {
		var lp LinkPreview
		if err := json.Unmarshal(linkPreviewRaw, &lp); err != nil {
			return fmt.Errorf("messages: unmarshal link_preview: %w", err)
		}
		m.LinkPreview = &lp
	}
	if len(locationRaw) > 0 {
		var loc Location
		if err := json.Unmarshal(locationRaw, &loc); err != nil {
			return fmt.Errorf("messages: unmarshal location: %w", err)
		}
		m.Location = &loc
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
