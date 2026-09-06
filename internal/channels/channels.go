// Package channels owns channel identity, stored in the cell database. A
// channel carries no routing metadata of its own: its region and shard are
// its app's placement (config DB, see docs/adr/0006-cell-based-tenant-routing.md),
// and all of its data lives in the one cell its app is pinned to. The only
// authoritative fact kept here beyond identity is app_id — the
// tenant-isolation boundary.
package channels

import (
	"context"
	"encoding/json"
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
	ChannelID uuid.UUID
	Name      string
	AppID     int64 // tenant-isolation boundary — see routing.ChannelRoute
	CreatedBy uuid.UUID
	CreatedAt time.Time
	// Visibility is "public" (any app user may read/join/discover it) or
	// "private" (members only — the default). See migrations/cell/0002.
	Visibility string
	// Custom is app-defined JSON metadata (searchable). Defaults to {}.
	Custom json.RawMessage
}

// VisibilityPublic / VisibilityPrivate are the allowed channel visibilities.
const (
	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

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

	visibility := c.Visibility
	if visibility == "" {
		visibility = VisibilityPrivate
	}
	custom := c.Custom
	if len(custom) == 0 {
		custom = json.RawMessage("{}")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO channels (channel_id, name, app_id, created_by, created_at, visibility, custom)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, c.ChannelID, c.Name, c.AppID, c.CreatedBy, c.CreatedAt, visibility, custom); err != nil {
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
	var custom []byte
	err := r.pool.QueryRow(ctx, `
		SELECT channel_id, name, app_id, created_by, created_at, visibility, custom
		FROM channels WHERE channel_id = $1
	`, channelID).Scan(&c.ChannelID, &c.Name, &c.AppID, &c.CreatedBy, &c.CreatedAt, &c.Visibility, &custom)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Channel{}, ErrNotFound
		}
		return Channel{}, fmt.Errorf("channels: get: %w", err)
	}
	if len(custom) > 0 {
		c.Custom = append(json.RawMessage(nil), custom...)
	} else {
		c.Custom = json.RawMessage("{}")
	}
	return c, nil
}

// SetVisibility updates a channel's visibility (public|private).
func (r *Repo) SetVisibility(ctx context.Context, channelID uuid.UUID, visibility string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE channels SET visibility = $1 WHERE channel_id = $2`, visibility, channelID); err != nil {
		return fmt.Errorf("channels: set visibility: %w", err)
	}
	return nil
}

// JoinPublic adds userID as a member of a public channel (self-service join).
// Idempotent; the caller must have already confirmed the channel is public.
func (r *Repo) JoinPublic(ctx context.Context, channelID, userID uuid.UUID) error {
	now := time.Now().UTC()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("channels: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO channel_members (channel_id, user_id, added_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, channelID, userID, now); err != nil {
		return fmt.Errorf("channels: join member: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_channels (user_id, channel_id, joined_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, userID, channelID, now); err != nil {
		return fmt.Errorf("channels: join index: %w", err)
	}
	return tx.Commit(ctx)
}

// Search returns channels in an app matching q by name, restricted to those
// the user may see: public channels plus any the user is already a member of.
// Empty q returns public + member channels. Ordered by name, capped at limit.
func (r *Repo) Search(ctx context.Context, appID int64, userID uuid.UUID, q string, limit int) ([]Channel, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT c.channel_id, c.name, c.app_id, c.created_by, c.created_at, c.visibility, c.custom
		FROM channels c
		WHERE c.app_id = $1
		  AND ($2 = '' OR c.name ILIKE '%' || $2 || '%')
		  AND (c.visibility = 'public'
		       OR EXISTS (SELECT 1 FROM channel_members m WHERE m.channel_id = c.channel_id AND m.user_id = $3))
		ORDER BY c.name
		LIMIT $4
	`, appID, q, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("channels: search: %w", err)
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		var custom []byte
		if err := rows.Scan(&c.ChannelID, &c.Name, &c.AppID, &c.CreatedBy, &c.CreatedAt, &c.Visibility, &custom); err != nil {
			return nil, fmt.Errorf("channels: search scan: %w", err)
		}
		if len(custom) > 0 {
			c.Custom = append(json.RawMessage(nil), custom...)
		} else {
			c.Custom = json.RawMessage("{}")
		}
		out = append(out, c)
	}
	return out, rows.Err()
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
		SELECT c.channel_id, c.name, c.app_id, c.created_by, c.created_at,
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
		if err := rows.Scan(&c.ChannelID, &c.Name, &c.AppID, &c.CreatedBy, &c.CreatedAt, &c.CreatorName, &c.MemberCount); err != nil {
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

// ChannelRouteInfo is the lean per-channel data the dashboard's message-count
// and polls views need: which app a channel belongs to and its name. Region
// is no longer per-channel (it's the app's placement), and there is no
// virtual shard — every channel in a cell lives on that cell's one database —
// so neither is carried here anymore.
type ChannelRouteInfo struct {
	ChannelID uuid.UUID
	AppID     int64
	Name      string
}

// ListRouteInfoByApps backs the dashboard's messages-sent view and the
// dashboard's polls view — every channel across a set of an org's apps in one
// query. (When those apps span multiple cells, the dashboard queries each
// cell and merges; within a cell this is a single scan.)
func (r *Repo) ListRouteInfoByApps(ctx context.Context, appIDs []int64) ([]ChannelRouteInfo, error) {
	if len(appIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT channel_id, app_id, name FROM channels WHERE app_id = ANY($1)
	`, appIDs)
	if err != nil {
		return nil, fmt.Errorf("channels: list route info by apps: %w", err)
	}
	defer rows.Close()

	var out []ChannelRouteInfo
	for rows.Next() {
		var c ChannelRouteInfo
		if err := rows.Scan(&c.ChannelID, &c.AppID, &c.Name); err != nil {
			return nil, fmt.Errorf("channels: list route info by apps: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListForRetention backs the message-retention sweep (chat worker). A cell's
// worker sweeps every channel in its own cell — there is no virtual-shard
// range to restrict to, because a cell holds exactly the channels of the apps
// pinned to it. Keyset-paginated on channel_id (INSTRUCTIONS.md §11: never
// OFFSET); afterChannelID is the last channel_id from the previous page
// (uuid.Nil for the first page).
func (r *Repo) ListForRetention(ctx context.Context, afterChannelID uuid.UUID, limit int) ([]Channel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT channel_id, name, app_id, created_by, created_at
		FROM channels
		WHERE channel_id > $1
		ORDER BY channel_id
		LIMIT $2
	`, afterChannelID, limit)
	if err != nil {
		return nil, fmt.Errorf("channels: list for retention: %w", err)
	}
	defer rows.Close()

	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ChannelID, &c.Name, &c.AppID, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("channels: list for retention: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RouteSource adapts Repo.Get for routing.RegionResolver's channel->app_id
// lookup (the tenant-isolation check; home-region forwarding is gone).
func (r *Repo) RouteSource(ctx context.Context, channelID string) (routing.ChannelRoute, error) {
	id, err := uuid.Parse(channelID)
	if err != nil {
		return routing.ChannelRoute{}, fmt.Errorf("channels: invalid channel id: %w", err)
	}
	c, err := r.Get(ctx, id)
	if err != nil {
		return routing.ChannelRoute{}, err
	}
	return routing.ChannelRoute{AppID: c.AppID}, nil
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
