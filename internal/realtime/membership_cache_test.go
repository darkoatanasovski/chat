package realtime

import (
	"context"
	"sort"
	"testing"

	"github.com/google/uuid"
)

func sortedIDs(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	sort.Strings(out)
	return out
}

func TestMembershipCache_MissReportsNotOK(t *testing.T) {
	cache := NewMembershipCache(testRedis(t), nil)
	members, ok, err := cache.Members(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected a cache miss for a channel that was never populated")
	}
	if members != nil {
		t.Fatalf("expected nil members on a cache miss, got %v", members)
	}
}

func TestMembershipCache_SetMembersThenGet(t *testing.T) {
	cache := NewMembershipCache(testRedis(t), nil)
	ctx := context.Background()
	channelID := uuid.New()
	want := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	if err := cache.SetMembers(ctx, channelID, want); err != nil {
		t.Fatalf("set members: %v", err)
	}

	got, ok, err := cache.Members(ctx, channelID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if !ok {
		t.Fatalf("expected a cache hit after SetMembers")
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d members, got %d: %v", len(want), len(got), got)
	}
	wantSorted, gotSorted := sortedIDs(want), sortedIDs(got)
	for i := range wantSorted {
		if wantSorted[i] != gotSorted[i] {
			t.Fatalf("member set mismatch: want %v, got %v", wantSorted, gotSorted)
		}
	}
}

func TestMembershipCache_SetMembersReplacesPreviousSet(t *testing.T) {
	cache := NewMembershipCache(testRedis(t), nil)
	ctx := context.Background()
	channelID := uuid.New()
	stale := uuid.New()
	current := []uuid.UUID{uuid.New(), uuid.New()}

	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{stale}); err != nil {
		t.Fatalf("seed stale members: %v", err)
	}
	if err := cache.SetMembers(ctx, channelID, current); err != nil {
		t.Fatalf("replace members: %v", err)
	}

	got, ok, err := cache.Members(ctx, channelID)
	if err != nil || !ok {
		t.Fatalf("members: ok=%v err=%v", ok, err)
	}
	for _, id := range got {
		if id == stale {
			t.Fatalf("expected SetMembers to fully replace the set — found stale removed member %s still present: %v", stale, got)
		}
	}
	if len(got) != len(current) {
		t.Fatalf("expected exactly the replaced set (%d members), got %d: %v", len(current), len(got), got)
	}
}

func TestMembershipCache_AddMember(t *testing.T) {
	cache := NewMembershipCache(testRedis(t), nil)
	ctx := context.Background()
	channelID := uuid.New()
	first := uuid.New()
	added := uuid.New()

	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{first}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := cache.AddMember(ctx, channelID, added); err != nil {
		t.Fatalf("add member: %v", err)
	}

	got, ok, err := cache.Members(ctx, channelID)
	if err != nil || !ok {
		t.Fatalf("members: ok=%v err=%v", ok, err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 members after adding one to a seeded channel, got %d: %v", len(got), got)
	}
}
