package quota

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRateLimiter_BurstThenDeny(t *testing.T) {
	rl := NewRateLimiter(testRedis(t))
	ctx := context.Background()
	key := "test:ratelimit:" + uuid.NewString()
	const limit = 3
	const now = int64(1_700_000_000)

	for i := 1; i <= limit; i++ {
		ok, err := rl.Allow(ctx, key, limit, now)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		if !ok {
			t.Fatalf("request %d: expected allow within burst capacity %d, got deny", i, limit)
		}
	}

	ok, err := rl.Allow(ctx, key, limit, now)
	if err != nil {
		t.Fatalf("over-limit request: unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected deny once burst capacity %d is exhausted at the same instant, got allow", limit)
	}
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(testRedis(t))
	ctx := context.Background()
	key := "test:ratelimit:" + uuid.NewString()
	const limit = 60 // 1 token/sec, easy to reason about
	const now = int64(1_700_000_000)

	for i := 1; i <= limit; i++ {
		if ok, err := rl.Allow(ctx, key, limit, now); err != nil || !ok {
			t.Fatalf("seed request %d: ok=%v err=%v", i, ok, err)
		}
	}
	if ok, _ := rl.Allow(ctx, key, limit, now); ok {
		t.Fatalf("expected bucket to be empty immediately after exhausting burst capacity")
	}

	// 10 seconds later, at 1 token/sec, exactly 10 more requests should be
	// allowed and the 11th denied.
	later := now + 10
	for i := 1; i <= 10; i++ {
		if ok, err := rl.Allow(ctx, key, limit, later); err != nil || !ok {
			t.Fatalf("refilled request %d: expected allow after 10s of refill, ok=%v err=%v", i, ok, err)
		}
	}
	if ok, err := rl.Allow(ctx, key, limit, later); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if ok {
		t.Fatalf("expected deny once the 10 refilled tokens are also spent")
	}
}

func TestRateLimiter_NonPositiveLimitAlwaysDenies(t *testing.T) {
	rl := NewRateLimiter(testRedis(t))
	ctx := context.Background()
	key := "test:ratelimit:" + uuid.NewString()

	ok, err := rl.Allow(ctx, key, 0, 1_700_000_000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected a zero/negative per-minute limit to always deny")
	}
}

func TestRateLimiter_IndependentKeys(t *testing.T) {
	rl := NewRateLimiter(testRedis(t))
	ctx := context.Background()
	keyA := "test:ratelimit:" + uuid.NewString()
	keyB := "test:ratelimit:" + uuid.NewString()
	const limit = 1
	const now = int64(1_700_000_000)

	if ok, err := rl.Allow(ctx, keyA, limit, now); err != nil || !ok {
		t.Fatalf("key A first request: ok=%v err=%v", ok, err)
	}
	if ok, _ := rl.Allow(ctx, keyA, limit, now); ok {
		t.Fatalf("key A second request: expected deny, its single token is spent")
	}
	// A different subject (e.g. a different user_id) must have its own
	// independent bucket.
	if ok, err := rl.Allow(ctx, keyB, limit, now); err != nil || !ok {
		t.Fatalf("key B first request: expected allow, unaffected by key A, ok=%v err=%v", ok, err)
	}
}
