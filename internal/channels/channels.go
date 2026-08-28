// Package channels owns channel identity and the one piece of authoritative
// routing metadata that isn't purely computed: home_region
// (INSTRUCTIONS.md §5). virtual_shard IS purely computed (internal/routing)
// but is stored alongside home_region at creation time so reads never need
// to recompute it either.
package channels

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/routing"
)

var ErrNotFound = errors.New("channel not found")

type Channel struct {
	ChannelID    uuid.UUID
	Name         string
	HomeRegion   string
	VirtualShard int
	AppID        int64 // tenant-isolation boundary — see routing.ChannelRoute
	CreatedBy    uuid.UUID
	CreatedAt    time.Time
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// CreateWithCreatorMembership creates the channel and bootstraps the
// creator's membership + user_channels index row in a single transaction.
// Channel creation is the one place these three control-plane tables must be
// written atomically; subsequent member additions are handled by
// internal/membership.
func (r *Repo) CreateWithCreatorMembership(ctx context.Context, c Channel) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("channels: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO channels (channel_id, name, home_region, virtual_shard, app_id, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, c.ChannelID, c.Name, c.HomeRegion, c.VirtualShard, c.AppID, c.CreatedBy, c.CreatedAt); err != nil {
		return fmt.Errorf("channels: create: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, added_at) VALUES ($1, $2, $3)
	`, c.ChannelID, c.CreatedBy, c.CreatedAt); err != nil {
		return fmt.Errorf("channels: add creator membership: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO user_channels (user_id, channel_id, joined_at) VALUES ($1, $2, $3)
	`, c.CreatedBy, c.ChannelID, c.CreatedAt); err != nil {
		return fmt.Errorf("channels: index creator user_channels: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("channels: commit: %w", err)
	}
	return nil
}

func (r *Repo) Get(ctx context.Context, channelID uuid.UUID) (Channel, error) {
	var c Channel
	err := r.pool.QueryRow(ctx, `
		SELECT channel_id, name, home_region, virtual_shard, app_id, created_by, created_at
		FROM channels WHERE channel_id = $1
	`, channelID).Scan(&c.ChannelID, &c.Name, &c.HomeRegion, &c.VirtualShard, &c.AppID, &c.CreatedBy, &c.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Channel{}, ErrNotFound
		}
		return Channel{}, fmt.Errorf("channels: get: %w", err)
	}
	return c, nil
}

// CountByCreator backs the max_channels resource quota
// (INSTRUCTIONS.md §22/§25): always read from authoritative Postgres state,
// never estimated from a cache.
func (r *Repo) CountByCreator(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM channels WHERE created_by = $1`, userID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("channels: count by creator: %w", err)
	}
	return count, nil
}

// ChannelWithStats is a channel plus the two pieces of context the
// dashboard's channels view needs beyond the raw row: who created it (by
// name, not just id) and how many members it currently has.
type ChannelWithStats struct {
	Channel
	CreatorName string
	MemberCount int
}

// ListByApp backs the dashboard's channels view — every channel in one app,
// newest first, unbounded like ListByApp on users (see internal/users for
// why: an operator view, not an end-user-facing feed).
func (r *Repo) ListByApp(ctx context.Context, appID int64) ([]ChannelWithStats, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.channel_id, c.name, c.home_region, c.virtual_shard, c.app_id, c.created_by, c.created_at,
		       u.display_name,
		       (SELECT count(*) FROM channel_members cm WHERE cm.channel_id = c.channel_id)
		FROM channels c
		JOIN users u ON u.user_id = c.created_by
		WHERE c.app_id = $1
		ORDER BY c.created_at DESC
	`, appID)
	if err != nil {
		return nil, fmt.Errorf("channels: list by app: %w", err)
	}
	defer rows.Close()

	var out []ChannelWithStats
	for rows.Next() {
		var c ChannelWithStats
		if err := rows.Scan(&c.ChannelID, &c.Name, &c.HomeRegion, &c.VirtualShard, &c.AppID, &c.CreatedBy, &c.CreatedAt, &c.CreatorName, &c.MemberCount); err != nil {
			return nil, fmt.Errorf("channels: list by app: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountByApp backs the dashboard's usage view — a raw channel count per
// app, informational only (same reasoning as users.Repo.CountByApp).
func (r *Repo) CountByApp(ctx context.Context, appID int64) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM channels WHERE app_id = $1`, appID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("channels: count by app: %w", err)
	}
	return count, nil
}

// RouteSource adapts Repo.Get for routing.RegionResolver's fallback.
func (r *Repo) RouteSource(ctx context.Context, channelID string) (routing.ChannelRoute, error) {
	id, err := uuid.Parse(channelID)
	if err != nil {
		return routing.ChannelRoute{}, fmt.Errorf("channels: invalid channel id: %w", err)
	}
	c, err := r.Get(ctx, id)
	if err != nil {
		return routing.ChannelRoute{}, err
	}
	return routing.ChannelRoute{HomeRegion: c.HomeRegion, AppID: c.AppID}, nil
}

// UpdateLastMessage denormalizes the latest-message pointer into
// user_channels for a batch of members. This is best-effort bookkeeping for
// GET /users/me/channels ordering, not a durability guarantee: the message
// itself is already safely committed on its shard before this is called, and
// a failure here just means a channel's "last message" position is briefly
// stale until the next message repairs it.
func (r *Repo) UpdateLastMessage(ctx context.Context, channelID uuid.UUID, memberIDs []uuid.UUID, sequence int64, at time.Time) error {
	if len(memberIDs) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE user_channels
		SET last_message_sequence = $1, last_message_at = $2
		WHERE channel_id = $3 AND user_id = ANY($4) AND last_message_sequence < $1
	`, sequence, at, channelID, memberIDs)
	if err != nil {
		return fmt.Errorf("channels: update last message: %w", err)
	}
	return nil
}
