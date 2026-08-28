package realtime

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/darkoatanasovski/chat/internal/platform/metrics"
)

// MembershipCache is a write-through cache of channel membership
// (INSTRUCTIONS.md §21) that gateways consult during fanout instead of
// querying the control-plane Postgres on every delivered message. cmd/api
// writes to it synchronously in the same request that writes Postgres, so it
// is never more than one request's-worth of writes stale; Postgres remains
// the sole source of truth and any consumer that needs a guaranteed-fresh
// membership list should still read it directly.
type MembershipCache struct {
	redis   *redis.Client
	metrics *metrics.Metrics
}

func NewMembershipCache(redisClient *redis.Client, m *metrics.Metrics) *MembershipCache {
	return &MembershipCache{redis: redisClient, metrics: m}
}

func membersKey(channelID uuid.UUID) string {
	return "membership:channel:" + channelID.String() + ":members"
}

func (m *MembershipCache) AddMember(ctx context.Context, channelID, userID uuid.UUID) error {
	return m.metrics.TimeRedis("membership_cache_add_member", func() error {
		if err := m.redis.SAdd(ctx, membersKey(channelID), userID.String()).Err(); err != nil {
			return fmt.Errorf("realtime: cache add member: %w", err)
		}
		return nil
	})
}

func (m *MembershipCache) RemoveMember(ctx context.Context, channelID, userID uuid.UUID) error {
	return m.metrics.TimeRedis("membership_cache_remove_member", func() error {
		if err := m.redis.SRem(ctx, membersKey(channelID), userID.String()).Err(); err != nil {
			return fmt.Errorf("realtime: cache remove member: %w", err)
		}
		return nil
	})
}

// SetMembers replaces the full member set, used once at channel creation.
func (m *MembershipCache) SetMembers(ctx context.Context, channelID uuid.UUID, userIDs []uuid.UUID) error {
	return m.metrics.TimeRedis("membership_cache_set_members", func() error {
		key := membersKey(channelID)
		pipe := m.redis.TxPipeline()
		pipe.Del(ctx, key)
		for _, id := range userIDs {
			pipe.SAdd(ctx, key, id.String())
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("realtime: cache set members: %w", err)
		}
		return nil
	})
}

// Members returns cached member IDs, or (nil, false) on a cache miss so the
// caller can fall back to Postgres (internal/membership.Repo.ListMembers).
func (m *MembershipCache) Members(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, bool, error) {
	var out []uuid.UUID
	var ok bool
	err := m.metrics.TimeRedis("membership_cache_members", func() error {
		ids, err := m.redis.SMembers(ctx, membersKey(channelID)).Result()
		if err != nil {
			return fmt.Errorf("realtime: cache members: %w", err)
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
