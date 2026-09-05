// Package appconfig is the cache-first reader of the global config database
// (migrations/config). It answers the two questions the new cell-based
// routing model needs (docs/adr/0006-cell-based-tenant-routing.md):
//
//	apikey  -> AppConfig   (the router: which cell to proxy to)
//	app_id  -> AppConfig   (a cell service: this app's placement + settings)
//
// It replaces internal/routing.RegionResolver (channel_id -> home_region) and
// generalizes internal/apps.TierResolver (app_id -> tier): routing is now a
// property of the App (the tenant), not of each channel. Every lookup is
// cache-first — a Redis-backed value with a 1-day TTL fallback — and
// invalidated explicitly on change (Invalidate), so a placement or settings
// edit takes effect at once rather than waiting out the TTL.
package appconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// ErrNotFound is returned when no active app matches the key or id.
var ErrNotFound = errors.New("appconfig: app not found")

// TTL is the cache fallback lifetime. Placement and settings are immutable or
// change rarely; on any change the writer calls Invalidate so this is only a
// safety net against a missed invalidation, not the primary freshness bound.
const TTL = 24 * time.Hour

// Placement is which cell holds an app's tenant data.
type Placement struct {
	Region string `json:"region"`
	Shard  string `json:"shard"`
}

// Provisioned reports whether the app has been assigned to a cell yet.
func (p Placement) Provisioned() bool { return p.Region != "" && p.Shard != "" }

// AppConfig is one app's globally-owned facts. It deliberately does NOT carry
// tenant data (users, channels) — only what the router and a cell service
// need to locate and govern the app.
type AppConfig struct {
	AppID        int64      `json:"app_id"`
	OrgID        int64      `json:"org_id"`
	Placement    Placement  `json:"placement"`
	Tier         string     `json:"tier"`
	Capabilities json.RawMessage `json:"capabilities"`
}

// Resolver reads the config DB, caching results in Redis. A nil redis client
// is allowed (every lookup then goes straight to Postgres) so tests and
// single-node setups need no cache.
type Resolver struct {
	pool  *pgxpool.Pool
	cache *redis.Client
}

func New(pool *pgxpool.Pool, cache *redis.Client) *Resolver {
	return &Resolver{pool: pool, cache: cache}
}

// ByAPIKey resolves an inbound apikey to its app's config. This is the
// router's hot path: it runs on (nearly) every request, so it is cache-first
// and, on a miss, a single indexed join (app_credentials.key -> apps).
func (r *Resolver) ByAPIKey(ctx context.Context, apiKey string) (AppConfig, error) {
	if apiKey == "" {
		return AppConfig{}, ErrNotFound
	}
	cacheKey := "appcfg:key:" + apiKey
	if cfg, ok := r.getCached(ctx, cacheKey); ok {
		return cfg, nil
	}

	const q = `
		SELECT a.app_id, a.org_id, a.region, a.shard, o.tier, a.channel_capabilities
		FROM app_credentials c
		JOIN apps a ON a.app_id = c.app_id
		JOIN organizations o ON o.org_id = a.org_id
		WHERE c.key = $1 AND c.revoked_at IS NULL`
	cfg, err := r.scan(ctx, q, apiKey)
	if err != nil {
		return AppConfig{}, err
	}
	r.setCached(ctx, cacheKey, cfg)
	// Also warm the by-id entry — a request resolved by key will usually be
	// followed by a by-id settings read inside the cell.
	r.setCached(ctx, idKey(cfg.AppID), cfg)
	return cfg, nil
}

// ByAppID resolves an app_id (from a verified token claim) to its config.
func (r *Resolver) ByAppID(ctx context.Context, appID int64) (AppConfig, error) {
	cacheKey := idKey(appID)
	if cfg, ok := r.getCached(ctx, cacheKey); ok {
		return cfg, nil
	}
	const q = `
		SELECT a.app_id, a.org_id, a.region, a.shard, o.tier, a.channel_capabilities
		FROM apps a
		JOIN organizations o ON o.org_id = a.org_id
		WHERE a.app_id = $1`
	cfg, err := r.scan(ctx, q, appID)
	if err != nil {
		return AppConfig{}, err
	}
	r.setCached(ctx, cacheKey, cfg)
	return cfg, nil
}

// Invalidate drops both cache entries for an app after a placement/settings/
// credential change so the next read reflects it immediately. apiKeys are the
// app's currently-active keys (pass the ones affected; unknown keys expire via TTL).
func (r *Resolver) Invalidate(ctx context.Context, appID int64, apiKeys ...string) {
	if r.cache == nil {
		return
	}
	keys := make([]string, 0, len(apiKeys)+1)
	keys = append(keys, idKey(appID))
	for _, k := range apiKeys {
		keys = append(keys, "appcfg:key:"+k)
	}
	_ = r.cache.Del(ctx, keys...).Err()
}

func (r *Resolver) scan(ctx context.Context, q string, arg any) (AppConfig, error) {
	var cfg AppConfig
	var region, shard *string
	err := r.pool.QueryRow(ctx, q, arg).Scan(
		&cfg.AppID, &cfg.OrgID, &region, &shard, &cfg.Tier, &cfg.Capabilities,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AppConfig{}, ErrNotFound
		}
		return AppConfig{}, fmt.Errorf("appconfig: query: %w", err)
	}
	if region != nil {
		cfg.Placement.Region = *region
	}
	if shard != nil {
		cfg.Placement.Shard = *shard
	}
	return cfg, nil
}

func (r *Resolver) getCached(ctx context.Context, key string) (AppConfig, bool) {
	if r.cache == nil {
		return AppConfig{}, false
	}
	packed, err := r.cache.Get(ctx, key).Result()
	if err != nil || packed == "" {
		return AppConfig{}, false
	}
	var cfg AppConfig
	if json.Unmarshal([]byte(packed), &cfg) != nil {
		return AppConfig{}, false
	}
	return cfg, true
}

func (r *Resolver) setCached(ctx context.Context, key string, cfg AppConfig) {
	if r.cache == nil {
		return
	}
	packed, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	_ = r.cache.Set(ctx, key, packed, TTL).Err()
}

func idKey(appID int64) string { return fmt.Sprintf("appcfg:id:%d", appID) }
