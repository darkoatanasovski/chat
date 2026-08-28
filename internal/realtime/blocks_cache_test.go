package realtime

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestBlocksCache_MissReportsNotOK(t *testing.T) {
	cache := NewBlocksCache(testRedis(t), nil)
	blocked, ok, err := cache.Blocked(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected a cache miss for a user that was never populated")
	}
	if blocked != nil {
		t.Fatalf("expected nil blocked set on a cache miss, got %v", blocked)
	}
}

func TestBlocksCache_SetBlockedThenGet(t *testing.T) {
	cache := NewBlocksCache(testRedis(t), nil)
	ctx := context.Background()
	userID := uuid.New()
	want := []uuid.UUID{uuid.New(), uuid.New()}

	if err := cache.SetBlocked(ctx, userID, want); err != nil {
		t.Fatalf("set blocked: %v", err)
	}

	got, ok, err := cache.Blocked(ctx, userID)
	if err != nil {
		t.Fatalf("blocked: %v", err)
	}
	if !ok {
		t.Fatalf("expected a cache hit after SetBlocked")
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d blocked users, got %d: %v", len(want), len(got), got)
	}
}

// TestBlocksCache_SetBlockedWithZeroBlocksStillReportsHit is the property
// MembershipCache doesn't need (a channel always has at least one member):
// most users have zero blocks, and an empty Redis set is indistinguishable
// from a key that was never created — the marker member is what lets a
// genuinely-empty cached set still report ok=true instead of looking like
// a miss on every single check.
func TestBlocksCache_SetBlockedWithZeroBlocksStillReportsHit(t *testing.T) {
	cache := NewBlocksCache(testRedis(t), nil)
	ctx := context.Background()
	userID := uuid.New()

	if err := cache.SetBlocked(ctx, userID, nil); err != nil {
		t.Fatalf("set blocked (empty): %v", err)
	}

	got, ok, err := cache.Blocked(ctx, userID)
	if err != nil {
		t.Fatalf("blocked: %v", err)
	}
	if !ok {
		t.Fatalf("expected a cache hit even for a user with zero blocks")
	}
	if len(got) != 0 {
		t.Fatalf("expected zero blocked users, got %v", got)
	}
}

func TestBlocksCache_AddPairUpdatesBothSides(t *testing.T) {
	cache := NewBlocksCache(testRedis(t), nil)
	ctx := context.Background()
	userA, userB := uuid.New(), uuid.New()

	if err := cache.AddPair(ctx, userA, userB); err != nil {
		t.Fatalf("add pair: %v", err)
	}

	gotA, okA, err := cache.Blocked(ctx, userA)
	if err != nil || !okA || len(gotA) != 1 || gotA[0] != userB {
		t.Fatalf("userA's cached set: ok=%v got=%v err=%v", okA, gotA, err)
	}
	gotB, okB, err := cache.Blocked(ctx, userB)
	if err != nil || !okB || len(gotB) != 1 || gotB[0] != userA {
		t.Fatalf("userB's cached set: ok=%v got=%v err=%v", okB, gotB, err)
	}
}

func TestBlocksCache_RemovePairUpdatesBothSides(t *testing.T) {
	cache := NewBlocksCache(testRedis(t), nil)
	ctx := context.Background()
	userA, userB := uuid.New(), uuid.New()

	if err := cache.AddPair(ctx, userA, userB); err != nil {
		t.Fatalf("add pair: %v", err)
	}
	if err := cache.RemovePair(ctx, userA, userB); err != nil {
		t.Fatalf("remove pair: %v", err)
	}

	gotA, okA, err := cache.Blocked(ctx, userA)
	if err != nil || !okA || len(gotA) != 0 {
		t.Fatalf("userA's cached set after removal: ok=%v got=%v err=%v", okA, gotA, err)
	}
	gotB, okB, err := cache.Blocked(ctx, userB)
	if err != nil || !okB || len(gotB) != 0 {
		t.Fatalf("userB's cached set after removal: ok=%v got=%v err=%v", okB, gotB, err)
	}
}
