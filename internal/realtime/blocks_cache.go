package realtime

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/darkoatanasovski/chat/internal/platform/metrics"
)

// BlocksCache is a write-through cache of which users a given user has any
// block relationship with, in either direction — the set fanout and
// message-listing consult so a block, once created, is actually enforced
// without a control-plane Postgres round trip on every delivery. Mirrors
// MembershipCache's shape and contract exactly: cmd/api writes through in
// the same request that writes Postgres, Postgres remains the source of
// truth, and a cache miss (including a user with genuinely zero blocks,
// which is most users) falls back to internal/blocks.Repo.BlockedPairsFor.
//
// Cached per-user rather than per-pair because the read side always asks
// "who can't $USER see," never "are these two specific users blocked" —
// fanout needs one user's (the sender's) full blocked set to filter a
// membership list against, and message-listing needs the same for the
// caller reading history.
type BlocksCache struct {
	redis   *redis.Client
	metrics *metrics.Metrics
}

func NewBlocksCache(redisClient *redis.Client, m *metrics.Metrics) *BlocksCache {
	return &BlocksCache{redis: redisClient, metrics: m}
}

func blockedSetKey(userID uuid.UUID) string {
	return "blocks:user:" + userID.String()
}

// Blocked returns the cached set of users userID has any block relationship
// with, or (nil, false) on a cache miss so the caller can fall back to
// Postgres (internal/blocks.Repo.BlockedPairsFor) and call SetBlocked.
func (c *BlocksCache) Blocked(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, bool, error) {
	var out []uuid.UUID
	var ok bool
	err := c.metrics.TimeRedis("blocks_cache_blocked", func() error {
		ids, err := c.redis.SMembers(ctx, blockedSetKey(userID)).Result()
		if err != nil {
			return fmt.Errorf("realtime: blocks cache read: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		out = make([]uuid.UUID, 0, len(ids))
		for _, s := range ids {
			id, err := uuid.Parse(s)
			if err != nil {
				continue
			}
			out = append(out, id)
		}
		ok = true
		return nil
	})
	return out, ok, err
}

// SetBlocked replaces userID's cached blocked set after a Postgres
// fallback. A user with zero blocks still needs a marker so future reads
// don't repeatedly miss and re-hit Postgres — see markerMember.
func (c *BlocksCache) SetBlocked(ctx context.Context, userID uuid.UUID, blockedWith []uuid.UUID) error {
	return c.metrics.TimeRedis("blocks_cache_set_blocked", func() error {
		key := blockedSetKey(userID)
		pipe := c.redis.TxPipeline()
		pipe.Del(ctx, key)
		pipe.SAdd(ctx, key, markerMember)
		for _, id := range blockedWith {
			pipe.SAdd(ctx, key, id.String())
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("realtime: blocks cache set: %w", err)
		}
		return nil
	})
}

// AddPair records that userA and userB now have a block relationship,
// updating both sides' cached sets directly — cmd/api calls this in the
// same request that writes the block to Postgres, so a freshly created
// block is enforced immediately rather than waiting for the next cache
// miss. Safe to call even if one side's cache was never warmed: SAdd on a
// key that doesn't exist yet just creates it, which Blocked's marker-based
// miss detection (see SetBlocked) will then correctly treat as already
// populated - the next full sync (SetBlocked) still fills in the rest.
func (c *BlocksCache) AddPair(ctx context.Context, userA, userB uuid.UUID) error {
	return c.metrics.TimeRedis("blocks_cache_add_pair", func() error {
		pipe := c.redis.TxPipeline()
		pipe.SAdd(ctx, blockedSetKey(userA), markerMember, userB.String())
		pipe.SAdd(ctx, blockedSetKey(userB), markerMember, userA.String())
		_, err := pipe.Exec(ctx)
		return err
	})
}

// RemovePair undoes AddPair. Only call this once the caller has confirmed
// (via internal/blocks.Repo) that no block remains between this pair in
// *either* direction — two independent block rows (A blocked B, and
// separately B blocked A) must both be gone before enforcement can lift,
// since unblocking only ever removes the caller's own row.
func (c *BlocksCache) RemovePair(ctx context.Context, userA, userB uuid.UUID) error {
	return c.metrics.TimeRedis("blocks_cache_remove_pair", func() error {
		pipe := c.redis.TxPipeline()
		pipe.SRem(ctx, blockedSetKey(userA), userB.String())
		pipe.SRem(ctx, blockedSetKey(userB), userA.String())
		_, err := pipe.Exec(ctx)
		return err
	})
}

// markerMember is a member that never resolves as a valid uuid.UUID
// (Blocked skips it via uuid.Parse's error), kept in every cached set
// purely to distinguish "cached, zero blocks" from "never cached" — the
// same problem MembershipCache doesn't have (a channel always has at
// least one member) but this cache does, since most users have zero
// blocks and an empty Redis set is indistinguishable from a missing key.
const markerMember = "_cached"
