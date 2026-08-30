// Package mutes owns the control-plane channel_mutes table
// (migrations/control/0013_mutes_and_unread_reminders.sql) — the "mutes"
// channel capability. Structurally this mirrors internal/blocks closely
// (Mute/Unmute/Exists/ListMuted/ListForApp all parallel
// Block/Unblock/Exists/ListBlocked/ListForApp), but the relationship itself
// is deliberately weaker than a block: muting is channel-scoped (not
// app-wide), one-directional (muting someone doesn't mute you back), and —
// unlike blocks, which internal/realtime.BlocksCache enforces bidirectionally
// against fanout — NOT wired into realtime delivery filtering at all. A
// muted sender's messages still reach every recipient's socket exactly as
// before; a client is expected to consult ListMuted (or the message
// payload's sender_id against its own local muted set) to decide whether to
// suppress a notification, badge count, or UI rendering for that sender.
// See the migration's doc comment for the full reasoning; a future
// enhancement could wire this into fanout the way blocks are, but that's
// explicitly out of scope today.
package mutes

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// Mute records that muterID has muted mutedID within channelID. Idempotent:
// muting someone already muted in this channel is a no-op, created=false.
func (r *Repo) Mute(ctx context.Context, channelID, muterID, mutedID uuid.UUID) (created bool, err error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO channel_mutes (channel_id, muter_user_id, muted_user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, muter_user_id, muted_user_id) DO NOTHING
	`, channelID, muterID, mutedID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Unmute removes a mute, but only the row muterID themself created — "the
// one who muted can only unmute," same self-service ownership restriction
// as blocks.Repo.Unblock. removed=false lets the caller treat a no-op
// (never muted, or someone else's mute) as a 404 without a separate check.
func (r *Repo) Unmute(ctx context.Context, channelID, muterID, mutedID uuid.UUID) (removed bool, err error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM channel_mutes WHERE channel_id = $1 AND muter_user_id = $2 AND muted_user_id = $3
	`, channelID, muterID, mutedID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Exists reports whether muterID has muted mutedID in channelID —
// one-directional, unlike blocks.Repo.Exists.
func (r *Repo) Exists(ctx context.Context, channelID, muterID, mutedID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM channel_mutes WHERE channel_id = $1 AND muter_user_id = $2 AND muted_user_id = $3)
	`, channelID, muterID, mutedID).Scan(&exists)
	return exists, err
}

// ListMuted returns the user IDs muterID has personally muted within
// channelID — the caller's own view, for a client to filter its own
// notifications/badges/rendering against (see the package doc comment).
func (r *Repo) ListMuted(ctx context.Context, channelID, muterID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT muted_user_id FROM channel_mutes
		WHERE channel_id = $1 AND muter_user_id = $2
		ORDER BY created_at DESC
	`, channelID, muterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Pair is a muter/muted user ID together, scoped to one channel — the shape
// ListForApp returns for a dashboard moderation view, mirroring
// blocks.Pair.
type Pair struct {
	ChannelID   uuid.UUID
	MuterUserID uuid.UUID
	MutedUserID uuid.UUID
}

// ListForApp returns every mute across every channel belonging to appID,
// for the dashboard's app-wide moderation view — joins through channels the
// same way channel-scoped dashboard listings elsewhere resolve "which rows
// belong to this app" (channel_mutes itself carries no app_id column).
func (r *Repo) ListForApp(ctx context.Context, appID int64) ([]Pair, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cm.channel_id, cm.muter_user_id, cm.muted_user_id
		FROM channel_mutes cm
		JOIN channels c ON c.channel_id = cm.channel_id
		WHERE c.app_id = $1
		ORDER BY cm.created_at DESC
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pair
	for rows.Next() {
		var p Pair
		if err := rows.Scan(&p.ChannelID, &p.MuterUserID, &p.MutedUserID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
