// Package routing resolves a channel_id to the App that owns it (app_id), the
// tenant-isolation boundary checked on every channel-scoped request (see
// cmd/api's route.AppID == identity.AppID checks). It is cache-first: a
// channel's app_id is fixed at creation, so it's cached in Redis with a
// Postgres fallback.
//
// This is all that remains of the old virtual-shard/home-region routing: in
// the cell model an App (and all its channels) lives in exactly one cell, so
// there is no per-channel region or shard to resolve — only which App a
// channel belongs to. See docs/adr/0006-cell-based-tenant-routing.md.
package routing

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const routeCacheTTL = 10 * time.Minute

// ChannelRoute is the immutable-after-creation fact a channel-scoped request
// needs: which App the channel belongs to.
type ChannelRoute struct {
	AppID int64
}

// ChannelRouteSource is the authoritative fallback used on cache miss — in
// practice internal/channels' cell-DB lookup.
type ChannelRouteSource func(ctx context.Context, channelID string) (ChannelRoute, error)

// RegionResolver answers channel_id -> app_id, cache-first. The cache is a
// simple TTL'd Redis key, not authoritative: on miss or Redis unavailability
// it always falls back to the source (Postgres).
type RegionResolver struct {
	redis  *redis.Client
	source ChannelRouteSource
}

func NewRegionResolver(redisClient *redis.Client, source ChannelRouteSource) *RegionResolver {
	return &RegionResolver{redis: redisClient, source: source}
}

func (r *RegionResolver) Resolve(ctx context.Context, channelID string) (ChannelRoute, error) {
	key := "route:channel:" + channelID

	if r.redis != nil {
		if packed, err := r.redis.Get(ctx, key).Result(); err == nil && packed != "" {
			if appID, perr := strconv.ParseInt(packed, 10, 64); perr == nil {
				return ChannelRoute{AppID: appID}, nil
			}
		} else if err != nil && !errors.Is(err, redis.Nil) {
			// Redis being unhealthy must not take down routing; fall through to Postgres.
			_ = err
		}
	}

	route, err := r.source(ctx, channelID)
	if err != nil {
		return ChannelRoute{}, fmt.Errorf("routing: resolve channel route: %w", err)
	}

	if r.redis != nil {
		_ = r.redis.Set(ctx, key, strconv.FormatInt(route.AppID, 10), routeCacheTTL).Err()
	}
	return route, nil
}

// InvalidateRoute drops the cached app_id for a channel. Unused today (a
// channel's App is immutable) but kept as the seam for a future tenant-move.
func (r *RegionResolver) InvalidateRoute(ctx context.Context, channelID string) error {
	if r.redis == nil {
		return nil
	}
	return r.redis.Del(ctx, "route:channel:"+channelID).Err()
}
