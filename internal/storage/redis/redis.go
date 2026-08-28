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

func Connect(ctx context.Context, addr string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})

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
