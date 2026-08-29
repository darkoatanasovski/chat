// Package blocks owns the control-plane user_blocks table
// (migrations/control/0004_user_blocks.sql). A block is directional for
// ownership — only the user who created it may remove it (Unblock's WHERE
// clause) — but internal/realtime enforces it bidirectionally: once either
// side has blocked the other, neither sees the other's messages. This
// package only manages the row itself; internal/realtime.BlocksCache is
// what fanout/message-listing actually consult on the hot path, kept in
// sync by cmd/api calling this Repo and the cache together in the same
// request, the same write-through pattern MembershipCache already uses.
//
// Repo binds one *pgxpool.Pool at construction rather than taking it per
// call the way reactions/messages do — those vary their pool per call
// because messages are sharded across multiple physical Postgres
// instances; user_blocks lives in the control plane alone, exactly like
// membership.Repo, which this package mirrors instead.
package blocks

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

// Block records that blockerID has blocked blockedID. Idempotent: blocking
// someone already blocked is a no-op, created=false.
func (r *Repo) Block(ctx context.Context, appID int64, blockerID, blockedID uuid.UUID) (created bool, err error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO user_blocks (app_id, blocker_user_id, blocked_user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (blocker_user_id, blocked_user_id) DO NOTHING
	`, appID, blockerID, blockedID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Unblock removes a block, but only the row where blockerID is the one who
// created it — "the one who blocked can only unblock." A caller who was
// never the blocker of this pair (never blocked them, or the block runs the
// other direction) simply matches zero rows; removed=false lets the caller
// treat that as a 404 without needing a separate ownership check.
func (r *Repo) Unblock(ctx context.Context, blockerID, blockedID uuid.UUID) (removed bool, err error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM user_blocks WHERE blocker_user_id = $1 AND blocked_user_id = $2
	`, blockerID, blockedID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// UnblockAny removes a block regardless of who created it, scoped to appID
// as defense in depth — the dashboard admin override for "the owner of the
// app can unblock directly," distinct from Unblock's self-service
// ownership restriction.
func (r *Repo) UnblockAny(ctx context.Context, appID int64, blockerID, blockedID uuid.UUID) (removed bool, err error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM user_blocks WHERE app_id = $1 AND blocker_user_id = $2 AND blocked_user_id = $3
	`, appID, blockerID, blockedID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Exists reports whether a block exists between userA and userB in either
// direction. Callers use this after Unblock/UnblockAny to decide whether
// internal/realtime.BlocksCache.RemovePair is actually safe to call — since
// unblocking only ever removes the caller's own row, a separate block in
// the opposite direction (each user independently blocked the other) must
// still be enforced.
func (r *Repo) Exists(ctx context.Context, userA, userB uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM user_blocks
			WHERE (blocker_user_id = $1 AND blocked_user_id = $2)
			   OR (blocker_user_id = $2 AND blocked_user_id = $1)
		)
	`, userA, userB).Scan(&exists)
	return exists, err
}

// ListBlocked returns the user IDs blockerID has personally blocked (the
// caller's own outbound block list — "who have I blocked").
func (r *Repo) ListBlocked(ctx context.Context, blockerID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT blocked_user_id FROM user_blocks WHERE blocker_user_id = $1 ORDER BY created_at DESC
	`, blockerID)
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

// BlockedPairsFor returns every user ID that has any block relationship
// with userID in either direction — the set internal/realtime.BlocksCache
// caches per user and fanout/message-listing filter against. Bidirectional
// by design: enforcement doesn't care which side created the block, only
// that one exists.
func (r *Repo) BlockedPairsFor(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT blocked_user_id FROM user_blocks WHERE blocker_user_id = $1
		UNION
		SELECT blocker_user_id FROM user_blocks WHERE blocked_user_id = $1
	`, userID)
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

// Pair is a blocker/blocked user ID together — the shape ListForApp
// returns for a dashboard moderation view.
type Pair struct {
	BlockerUserID uuid.UUID
	BlockedUserID uuid.UUID
}

// ListForApp returns every block within appID, for the dashboard's
// app-wide moderation view.
func (r *Repo) ListForApp(ctx context.Context, appID int64) ([]Pair, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT blocker_user_id, blocked_user_id FROM user_blocks WHERE app_id = $1 ORDER BY created_at DESC
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pair
	for rows.Next() {
		var p Pair
		if err := rows.Scan(&p.BlockerUserID, &p.BlockedUserID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
