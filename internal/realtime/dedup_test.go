package realtime

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// testRedis returns a client for the local Valkey instance used by the
// docker-compose dev stack (deploy/docker-compose.yml), skipping the test if
// it isn't reachable rather than failing the whole suite in an environment
// where the stack isn't up.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("valkey not reachable at %s (start the stack: make up): %v", addr, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// Regression test for the bug where every gateway region shared one dedup
// namespace: whichever region's fanout consumer processed a given
// channel_id:sequence event first would mark it globally "seen", so every
// other region's Fanout.handle silently skipped delivery — including to its
// own local connections — for the rest of that event's TTL.
func TestDedup_NamespaceIsolation(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()
	eventID := uuid.NewString() + ":1"

	euDedup := NewDedup(client, "eu-gateway-"+uuid.NewString())
	usDedup := NewDedup(client, "us-gateway-"+uuid.NewString())

	euSeen, err := euDedup.SeenBefore(ctx, eventID)
	if err != nil {
		t.Fatalf("eu SeenBefore: %v", err)
	}
	if euSeen {
		t.Fatalf("eu: expected first SeenBefore to report false (not seen), got true")
	}

	// A different region's gateway processing the SAME event must not be
	// affected by eu's claim — each region needs to run its own local
	// delivery pass over every message.
	usSeen, err := usDedup.SeenBefore(ctx, eventID)
	if err != nil {
		t.Fatalf("us SeenBefore: %v", err)
	}
	if usSeen {
		t.Fatalf("us: SeenBefore reported true for an event only eu has processed — dedup namespaces are not isolated (the bug this test guards against)")
	}

	// Within a single region, a redelivered event (e.g. after a consumer
	// group rebalance) must still be recognized as already processed.
	euSeenAgain, err := euDedup.SeenBefore(ctx, eventID)
	if err != nil {
		t.Fatalf("eu second SeenBefore: %v", err)
	}
	if !euSeenAgain {
		t.Fatalf("eu: expected second SeenBefore for the same event to report true (seen), got false")
	}
}
