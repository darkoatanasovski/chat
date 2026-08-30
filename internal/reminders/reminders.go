// Package reminders owns the per-shard message_reminders table
// (migrations/shard/0012_message_reminders.sql) — the "message_reminders"
// capability. A reminder is created by a channel member against one
// specific message ("remind me about this at time T") and delivered once,
// at or after T, by cmd/worker's poll loop (reminders_worker.go) — never on
// the request path, the same "background job, not the send/read path"
// separation retention already established for a different concern.
package reminders

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/events"
)

type Reminder struct {
	ReminderID  uuid.UUID
	ChannelID   uuid.UUID
	MessageID   uuid.UUID
	UserID      uuid.UUID
	RemindAt    time.Time
	DeliveredAt *time.Time
	CreatedAt   time.Time
}

type Repo struct{}

func NewRepo() *Repo { return &Repo{} }

// Create records a new reminder. No uniqueness constraint on
// (channel_id, message_id, user_id): a user may stack more than one
// reminder for the same message at different times, kept as simple as
// blocks/mutes' "just insert a row" shape rather than adding upsert
// semantics nothing here has asked for yet.
func (r *Repo) Create(ctx context.Context, pool *pgxpool.Pool, channelID, messageID, userID uuid.UUID, remindAt time.Time) (Reminder, error) {
	reminderID, err := uuid.NewV7()
	if err != nil {
		return Reminder{}, fmt.Errorf("reminders: generate id: %w", err)
	}
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO message_reminders (reminder_id, channel_id, message_id, user_id, remind_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, reminderID, channelID, messageID, userID, remindAt, now); err != nil {
		return Reminder{}, fmt.Errorf("reminders: create: %w", err)
	}
	return Reminder{
		ReminderID: reminderID, ChannelID: channelID, MessageID: messageID, UserID: userID,
		RemindAt: remindAt, CreatedAt: now,
	}, nil
}

// Cancel removes every not-yet-delivered reminder userID holds for messageID
// in channelID — DELETE /channels/{id}/messages/{message_id}/reminders has
// no reminder_id of its own to target (a client only ever knows "the
// reminder I set on this message"), so this cancels the whole set rather
// than requiring the caller to track individual reminder_ids. removed is
// how many were cancelled; 0 is a 404 signal for the caller (nothing to
// cancel — never set, or already delivered).
func (r *Repo) Cancel(ctx context.Context, pool *pgxpool.Pool, channelID, messageID, userID uuid.UUID) (removed int64, err error) {
	tag, err := pool.Exec(ctx, `
		DELETE FROM message_reminders
		WHERE channel_id = $1 AND message_id = $2 AND user_id = $3 AND delivered_at IS NULL
	`, channelID, messageID, userID)
	if err != nil {
		return 0, fmt.Errorf("reminders: cancel: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListDue returns up to limit not-yet-delivered reminders whose remind_at
// has passed, oldest first — backed entirely by idx_message_reminders_due
// (migrations/shard/0012), the poll target for cmd/worker's sweep.
func (r *Repo) ListDue(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Reminder, error) {
	rows, err := pool.Query(ctx, `
		SELECT reminder_id, channel_id, message_id, user_id, remind_at, delivered_at, created_at
		FROM message_reminders
		WHERE delivered_at IS NULL AND remind_at <= now()
		ORDER BY remind_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("reminders: list due: %w", err)
	}
	defer rows.Close()

	var out []Reminder
	for rows.Next() {
		var rem Reminder
		if err := rows.Scan(&rem.ReminderID, &rem.ChannelID, &rem.MessageID, &rem.UserID, &rem.RemindAt, &rem.DeliveredAt, &rem.CreatedAt); err != nil {
			return nil, fmt.Errorf("reminders: scan: %w", err)
		}
		out = append(out, rem)
	}
	return out, rows.Err()
}

// DeliverDue finds up to limit currently-due reminders and, for each one in
// its own transaction, marks it delivered and writes the matching
// message_reminder.due outbox row atomically — the same "domain write plus
// outbox insert share one transaction" discipline as every other event in
// this codebase (see internal/messages.Repo.Send), just driven by a poll
// loop (cmd/worker) instead of a synchronous request. One transaction per
// reminder rather than one for the whole batch, so a single bad row can't
// roll back reminders that would otherwise have been delivered on time.
// Returns how many were actually delivered by this call.
func (r *Repo) DeliverDue(ctx context.Context, pool *pgxpool.Pool, limit int) (delivered int, err error) {
	due, err := r.ListDue(ctx, pool, limit)
	if err != nil {
		return 0, err
	}
	for _, rem := range due {
		ok, err := r.deliverOne(ctx, pool, rem)
		if err != nil {
			return delivered, err
		}
		if ok {
			delivered++
		}
	}
	return delivered, nil
}

func (r *Repo) deliverOne(ctx context.Context, pool *pgxpool.Pool, rem Reminder) (delivered bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("reminders: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE message_reminders SET delivered_at = now() WHERE reminder_id = $1 AND delivered_at IS NULL
	`, rem.ReminderID)
	if err != nil {
		return false, fmt.Errorf("reminders: mark delivered: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Already delivered by a concurrent poll (e.g. two worker instances
		// briefly overlapping) — nothing left to do.
		return false, nil
	}

	payload := events.MessageReminderDuePayload{
		ReminderID: rem.ReminderID, ChannelID: rem.ChannelID, MessageID: rem.MessageID, UserID: rem.UserID,
	}
	if err := events.InsertOutbox(ctx, tx, events.TopicMessageReminderDue, rem.ChannelID, payload); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("reminders: commit: %w", err)
	}
	return true, nil
}
