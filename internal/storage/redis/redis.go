// Package redis wraps the Valkey/Redis client used for ephemeral/distributed
// state: routing cache, membership cache, rate limiting, quota cache,
// connection registry (INSTRUCTIONS.md §21). Never the authoritative store
// for messages or resource-limit state.
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// connectTimeout matches postgres.Connect's rationale: don't assume a
// dependency is reachable the instant this process starts.
const connectTimeout = 30 * time.Second

// Connect opens a plain single-node client pinned to addr. Use
// ConnectSentinel instead wherever Valkey/Redis runs as a Sentinel-managed
// primary/replica pair (see deploy/docker-compose.yml's valkey-sentinel-*
// services) so a failover doesn't leave the process holding a dead address.
func Connect(ctx context.Context, addr string) (*redis.Client, error) {
	return connectWithRetry(ctx, redis.NewClient(&redis.Options{Addr: addr}))
}

// ConnectSentinel opens a client that discovers the current primary through
// the given Sentinel instances and transparently follows it across
// failover, rather than pinning to one address the way Connect does.
// masterName must match the name every Sentinel's own config file monitors
// it under ("sentinel monitor <masterName> ..."), not a hostname.
func ConnectSentinel(ctx context.Context, sentinelAddrs []string, masterName string) (*redis.Client, error) {
	client := redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    masterName,
		SentinelAddrs: sentinelAddrs,
	})
	return connectWithRetry(ctx, client)
}

// ConnectFromEnv picks ConnectSentinel when sentinelAddrs is non-empty,
// otherwise falls back to Connect(addr) — the one seam every service's
// wiring should go through, so failover mode is a config toggle rather than
// two separate call sites that could drift out of sync.
func ConnectFromEnv(ctx context.Context, addr string, sentinelAddrs []string, masterName string) (*redis.Client, error) {
	if len(sentinelAddrs) > 0 {
		return ConnectSentinel(ctx, sentinelAddrs, masterName)
	}
	return Connect(ctx, addr)
}

func connectWithRetry(ctx context.Context, client *redis.Client) (*redis.Client, error) {
	deadline := time.Now().Add(connectTimeout)
	var pingErr error
	for time.Now().Before(deadline) {
		pingErr = client.Ping(ctx).Err()
		if pingErr == nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	return nil, fmt.Errorf("redis: ping: timed out after %s: %w", connectTimeout, pingErr)
}
