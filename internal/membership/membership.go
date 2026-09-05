// Package membership owns channel_members and the user_channels index for
// every member-addition after channel creation (the creator's own bootstrap
// row is written atomically by internal/channels).
package membership

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) IsMember(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM channel_members WHERE channel_id = $1 AND user_id = $2)
	`, channelID, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("membership: is member: %w", err)
	}
	return exists, nil
}

// CountMembers backs the max_channel_members resource quota — always read
// from authoritative Postgres state (INSTRUCTIONS.md §25).
func (r *Repo) CountMembers(ctx context.Context, channelID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM channel_members WHERE channel_id = $1`, channelID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("membership: count: %w", err)
	}
	return count, nil
}

// AddMember writes channel_members and the corresponding user_channels index
// row in one transaction. Both live in the control-plane DB, so this is a
// single-shard write, not a cross-shard transaction.
func (r *Repo) AddMember(ctx context.Context, channelID, userID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("membership: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, added_at) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
	`, channelID, userID, now); err != nil {
		return fmt.Errorf("membership: add: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_channels (user_id, channel_id, joined_at) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
	`, userID, channelID, now); err != nil {
		return fmt.Errorf("membership: index: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("membership: commit: %w", err)
	}
	return nil
}

// RemoveMember is the inverse of AddMember — removes both the
// channel_members row and its user_channels index entry in one transaction.
// There's no protection against removing a channel's creator: created_by on
// the channels row is immutable history, not a membership guarantee, so
// removing the creator just leaves the channel without that member, exactly
// like removing anyone else.
func (r *Repo) RemoveMember(ctx context.Context, channelID, userID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("membership: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM channel_members WHERE channel_id = $1 AND user_id = $2`, channelID, userID); err != nil {
		return fmt.Errorf("membership: remove: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_channels WHERE channel_id = $1 AND user_id = $2`, channelID, userID); err != nil {
		return fmt.Errorf("membership: deindex: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("membership: commit: %w", err)
	}
	return nil
}

// ListMembers returns every member of a channel. Used to populate the
// gateway's Redis-backed membership cache (internal/realtime) and to
// denormalize last-message pointers after a send — both bounded operations
// under V1's conservative channel-member limits (INSTRUCTIONS.md §31).
func (r *Repo) ListMembers(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT user_id FROM channel_members WHERE channel_id = $1`, channelID)
	if err != nil {
		return nil, fmt.Errorf("membership: list: %w", err)
	}
	defer rows.Close()

	var members []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("membership: scan: %w", err)
		}
		members = append(members, id)
	}
	return members, rows.Err()
}

// Member is a channel member plus their display name and presence, for UI
// member lists (GET /channels/{id}/members) — ListMembers itself stays
// name/presence-free since its other callers (membership cache seeding,
// quota checks, fanout) only ever need user_ids.
type Member struct {
	UserID      uuid.UUID
	DisplayName string
	// LastActiveAt is nil until this user's first tracked activity — see
	// internal/users.IsOnline for how it's turned into online status.
	LastActiveAt *time.Time
}

func (r *Repo) ListMembersWithNames(ctx context.Context, channelID uuid.UUID) ([]Member, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cm.user_id, u.display_name, u.last_active_at
		FROM channel_members cm
		JOIN users u ON u.user_id = cm.user_id
		WHERE cm.channel_id = $1
		ORDER BY cm.added_at
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("membership: list with names: %w", err)
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.LastActiveAt); err != nil {
			return nil, fmt.Errorf("membership: scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListChannelsForUser backs GET /users/me/channels: a single control-plane
// query keyed by user_id, never a scatter/gather across message shards
// (INSTRUCTIONS.md §13).
type UserChannel struct {
	ChannelID           uuid.UUID
	ChannelName         string
	LastMessageSequence int64
	LastMessageAt       *time.Time
}

// UnreadReminderCandidates returns channelID's member user_ids that are due
// a fresh nudge attempt — last_unread_reminder_sent_at is null (never
// reminded) or older than cutoff (cmd/worker's minimum-gap cooldown,
// computed by the caller as now()-minGap) — the "unread_reminders"
// capability. This is only the cooldown filter: the caller still has to
// check each candidate's actual read state (internal/readstate, a
// different database) before deciding whether they're truly behind.
func (r *Repo) UnreadReminderCandidates(ctx context.Context, channelID uuid.UUID, cutoff time.Time) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id FROM channel_members
		WHERE channel_id = $1 AND (last_unread_reminder_sent_at IS NULL OR last_unread_reminder_sent_at < $2)
	`, channelID, cutoff)
	if err != nil {
		return nil, fmt.Errorf("membership: unread reminder candidates: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("membership: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkUnreadReminderSent stamps last_unread_reminder_sent_at = now() so
// UnreadReminderCandidates won't offer this member again until the next
// cooldown window passes.
func (r *Repo) MarkUnreadReminderSent(ctx context.Context, channelID, userID uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE channel_members SET last_unread_reminder_sent_at = now() WHERE channel_id = $1 AND user_id = $2
	`, channelID, userID); err != nil {
		return fmt.Errorf("membership: mark unread reminder sent: %w", err)
	}
	return nil
}

func (r *Repo) ListChannelsForUser(ctx context.Context, userID uuid.UUID) ([]UserChannel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT uc.channel_id, c.name, uc.last_message_sequence, uc.last_message_at
		FROM user_channels uc
		JOIN channels c ON c.channel_id = uc.channel_id
		WHERE uc.user_id = $1
		ORDER BY uc.last_message_at DESC NULLS LAST, uc.joined_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("membership: list channels for user: %w", err)
	}
	defer rows.Close()

	var out []UserChannel
	for rows.Next() {
		var uc UserChannel
		if err := rows.Scan(&uc.ChannelID, &uc.ChannelName, &uc.LastMessageSequence, &uc.LastMessageAt); err != nil {
			return nil, fmt.Errorf("membership: scan: %w", err)
		}
		out = append(out, uc)
	}
	return out, rows.Err()
}
