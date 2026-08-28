package routing

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const homeRegionCacheTTL = 10 * time.Minute

// ChannelRoute is the immutable-after-creation metadata every channel-scoped
// request needs: which region is authoritative for writes, and which App
// this channel belongs to (the tenant-isolation boundary — see
// cmd/api's channel.AppID == identity.AppID checks).
type ChannelRoute struct {
	HomeRegion string
	AppID      int64
}

// ChannelRouteSource is the authoritative fallback used on cache miss. In
// practice this is internal/channels' control-plane lookup.
type ChannelRouteSource func(ctx context.Context, channelID string) (ChannelRoute, error)

// RegionResolver answers channel_id -> {home_region, app_id}, cache-first
// (INSTRUCTIONS.md §6: "heavily cached... not a central database lookup for
// every message"). Both fields are resolved together in one cached lookup
// since both are fixed at channel creation and a request needing one
// routinely needs the other (home_region to decide forwarding, app_id to
// verify tenant isolation) — two independent caches would just double the
// Redis round trips for no benefit. The cache is a simple TTL'd key, not
// authoritative: on miss or Redis unavailability it always falls back to
// Postgres.
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
			if route, ok := unpackRoute(packed); ok {
				return route, nil
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
		_ = r.redis.Set(ctx, key, packRoute(route), homeRegionCacheTTL).Err()
	}
	return route, nil
}

// InvalidateRoute is unused for V1 (a channel's route is immutable after
// creation) but kept as the seam for a future "move channel region"
// operation.
func (r *RegionResolver) InvalidateRoute(ctx context.Context, channelID string) error {
	if r.redis == nil {
		return nil
	}
	return r.redis.Del(ctx, "route:channel:"+channelID).Err()
}

func packRoute(route ChannelRoute) string {
	return route.HomeRegion + "|" + strconv.FormatInt(route.AppID, 10)
}

func unpackRoute(packed string) (ChannelRoute, bool) {
	region, appIDStr, ok := strings.Cut(packed, "|")
	if !ok {
		return ChannelRoute{}, false
	}
	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil {
		return ChannelRoute{}, false
	}
	return ChannelRoute{HomeRegion: region, AppID: appID}, true
}
