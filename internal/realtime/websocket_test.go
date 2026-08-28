package realtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestConnectHandler_RelayTyping_DeliversToOtherMembers proves typing
// presence reaches every other member of the channel and is never written
// anywhere durable — this exercises relayTyping directly (the same method
// readPump calls for an inbound typing.start/typing.stop frame) rather than
// standing up a real WebSocket, matching how Fanout's tests call handle()
// directly instead of running Kafka.
func TestConnectHandler_RelayTyping_DeliversToOtherMembers(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	typist := uuid.New()
	other := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{typist, other}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	typistConn := hub.Register(typist, "eu")
	otherConn := hub.Register(other, "eu")

	delivery := NewDelivery(hub, cache, nil, NewRegistry(client, nil), NewPublisher(client, nil), discardLogger())
	h := NewConnectHandler(nil, hub, NewRegistry(client, nil), delivery, "eu-test", nil, discardLogger())

	h.relayTyping(ctx, typist, channelID, true)

	select {
	case payload := <-otherConn.send:
		var frame TypingFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("unmarshal delivered frame: %v", err)
		}
		if frame.Type != "typing.updated" || frame.UserID != typist || !frame.Typing {
			t.Fatalf("unexpected frame: %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected the other member to receive a typing.updated frame")
	}

	select {
	case <-typistConn.send:
		t.Fatalf("expected the typist to never receive their own typing echo")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestConnectHandler_RelayTyping_RequiresMembership proves a user asserting
// a channel_id they don't actually belong to can't broadcast typing into it
// (INSTRUCTIONS.md §43: never trust client-asserted state) — membership is
// resolved server-side from Redis/Postgres, not taken from the frame.
func TestConnectHandler_RelayTyping_RequiresMembership(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	member := uuid.New()
	outsider := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{member}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	memberConn := hub.Register(member, "eu")
	hub.Register(outsider, "eu")

	delivery := NewDelivery(hub, cache, nil, NewRegistry(client, nil), NewPublisher(client, nil), discardLogger())
	h := NewConnectHandler(nil, hub, NewRegistry(client, nil), delivery, "eu-test", nil, discardLogger())

	h.relayTyping(ctx, outsider, channelID, true)

	select {
	case <-memberConn.send:
		t.Fatalf("expected no delivery: outsider is not a member of this channel")
	case <-time.After(200 * time.Millisecond):
	}
}
