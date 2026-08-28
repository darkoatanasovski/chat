// Package postgres provides thin connection-pool setup shared by every
// service. Domain repositories (internal/users, internal/channels, ...) own
// their own queries; this package only owns connecting.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// connectTimeout bounds how long Connect retries before giving up. Every
// service in this platform assumes its dependencies may not be ready the
// instant it starts (container orchestration gives no ordering guarantee
// beyond "started", not "ready") — INSTRUCTIONS.md §28 "assume every service
// may crash between any two operations" extends naturally to "assume it may
// start before what it depends on is reachable."
const connectTimeout = 30 * time.Second

func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}

	deadline := time.Now().Add(connectTimeout)
	var pingErr error
	for time.Now().Before(deadline) {
		pingErr = pool.Ping(ctx)
		if pingErr == nil {
			return pool, nil
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	pool.Close()
	return nil, fmt.Errorf("postgres: ping: timed out after %s: %w", connectTimeout, pingErr)
}

// ShardPools maps a physical shard ID (e.g. "shard-a") to its connection pool.
// internal/routing decides which shard ID a given channel/user belongs to;
// domain repositories use that ID to pick a pool from this map.
type ShardPools map[string]*pgxpool.Pool

func (p ShardPools) Get(shardID string) (*pgxpool.Pool, error) {
	pool, ok := p[shardID]
	if !ok {
		return nil, fmt.Errorf("postgres: unknown shard %q", shardID)
	}
	return pool, nil
}
