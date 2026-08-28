package apps

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const tierCacheTTL = 10 * time.Minute

// TierSource is the authoritative fallback used on a cache miss — in
// practice Repo.TierSource (the live apps↔organizations join).
type TierSource func(ctx context.Context, appID int64) (string, error)

// TierResolver answers app_id -> organization tier, cache-first
// (INSTRUCTIONS.md §6: "heavily cached... not a central database lookup
// for every message"), mirroring internal/routing.RegionResolver exactly.
// The cache is a short-TTL accelerator, not a second source of truth: an
// org's tier change becomes visible to that org's every app within one TTL
// window, not only at each end-user's next token reissue — tier is
// deliberately never baked into a token (see internal/platform/auth).
type TierResolver struct {
	redis  *redis.Client
	source TierSource
}

func NewTierResolver(redisClient *redis.Client, source TierSource) *TierResolver {
	return &TierResolver{redis: redisClient, source: source}
}

func (r *TierResolver) TierForApp(ctx context.Context, appID int64) (string, error) {
	key := "route:app:" + strconv.FormatInt(appID, 10) + ":tier"

	if r.redis != nil {
		if tier, err := r.redis.Get(ctx, key).Result(); err == nil && tier != "" {
			return tier, nil
		} else if err != nil && !errors.Is(err, redis.Nil) {
			// Redis being unhealthy must not take down tier resolution;
			// fall through to Postgres.
			_ = err
		}
	}

	tier, err := r.source(ctx, appID)
	if err != nil {
		return "", fmt.Errorf("apps: resolve tier: %w", err)
	}

	if r.redis != nil {
		_ = r.redis.Set(ctx, key, tier, tierCacheTTL).Err()
	}
	return tier, nil
}
