package quota

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// tokenBucketScript implements a token bucket entirely inside Redis (one
// round trip per check, INSTRUCTIONS.md §24 "algorithms that do not require
// excessive Redis operations"). KEYS[1] is the bucket key; ARGV are
// capacity, refill-per-second, requested tokens (always 1), and the current
// unix time in seconds (passed in rather than using Redis TIME so behavior
// is deterministic under test).
const tokenBucketScript = `
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_per_sec = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

local bucket = redis.call("HMGET", key, "tokens", "updated_at")
local tokens = tonumber(bucket[1])
local updated_at = tonumber(bucket[2])

if tokens == nil then
  tokens = capacity
  updated_at = now
end

local elapsed = math.max(0, now - updated_at)
tokens = math.min(capacity, tokens + elapsed * refill_per_sec)

local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end

redis.call("HSET", key, "tokens", tokens, "updated_at", now)
redis.call("EXPIRE", key, ttl)

return allowed
`

// RateLimiter enforces per-minute rate limits (as opposed to resource
// limits — see Quota.Allow) via a Redis-backed token bucket. It is the sole
// authority for rate decisions; unlike resource limits, being briefly wrong
// about a rate limit has no durable-state consequence, so Redis alone is
// sufficient (INSTRUCTIONS.md §25).
type RateLimiter struct {
	redis  *redis.Client
	script *redis.Script
}

func NewRateLimiter(redisClient *redis.Client) *RateLimiter {
	return &RateLimiter{redis: redisClient, script: redis.NewScript(tokenBucketScript)}
}

// Allow checks a bucket identified by key with the given per-minute limit.
// The bucket's capacity equals the per-minute limit and refills continuously
// at limit/60 tokens per second, so a client can burst up to the full
// per-minute allowance and then must wait for it to refill.
func (rl *RateLimiter) Allow(ctx context.Context, key string, perMinuteLimit int, nowUnix int64) (bool, error) {
	if perMinuteLimit <= 0 {
		return false, nil
	}
	refillPerSec := float64(perMinuteLimit) / 60.0
	result, err := rl.script.Run(ctx, rl.redis, []string{key},
		perMinuteLimit, refillPerSec, nowUnix, 120,
	).Int()
	if err != nil {
		return false, fmt.Errorf("quota: rate limit check: %w", err)
	}
	return result == 1, nil
}
