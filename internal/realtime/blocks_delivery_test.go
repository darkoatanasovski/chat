package realtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/darkoatanasovski/chat/internal/events"
)

// TestFanout_SkipsDeliveryToBlockedRecipient proves block enforcement on
// the live WebSocket delivery path, not just message history
// (cmd/api.handleListMessages's filtering is a separate code path — this
// is what a still-open connection actually receives in real time). A
// sender and a recipient who has any block relationship with them
// (blocksCache.AddPair, mirroring what cmd/api writes through on a real
// block/unblock call) must not exchange the frame, while an unrelated
// third member still receives it normally.
func TestFanout_SkipsDeliveryToBlockedRecipient(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	sender := uuid.New()
	blockedRecipient := uuid.New()
	unaffected := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{sender, blockedRecipient, unaffected}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	blockedConn := hub.Register(blockedRecipient, "eu")
	unaffectedConn := hub.Register(unaffected, "eu")

	blocksCache := NewBlocksCache(client, nil)
	if err := blocksCache.AddPair(ctx, sender, blockedRecipient); err != nil {
		t.Fatalf("seed blocks cache: %v", err)
	}

	registry := NewRegistry(client, nil)
	publisher := NewPublisher(client, nil)
	delivery := NewDelivery(hub, cache, nil, blocksCache, nil, registry, publisher, discardLogger())
	fanout := NewFanout(nil, delivery, NewDedup(client, uuid.NewString(), nil), nil, discardLogger())

	payload := events.MessageCreatedPayload{
		MessageID: uuid.New(), ChannelID: channelID, SenderID: sender,
		ClientMessageID: uuid.New(), Sequence: 1, Body: "hello", CreatedAt: time.Now().UTC(),
	}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := fanout.handle(ctx, kafkago.Message{Value: value}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	assertDelivered(t, "unaffected member", unaffectedConn)

	select {
	case <-blockedConn.send:
		t.Fatalf("expected the blocked recipient to receive nothing, but a frame arrived")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestFanout_BlockIsBidirectionalOnDeliveryToo mirrors
// TestBlocking_EnforcedBidirectionallyInMessageHistory (cmd/api) at the
// realtime layer: if the *recipient* is the one who blocked the sender
// (rather than the other way around), the sender's message still isn't
// delivered to them.
func TestFanout_BlockIsBidirectionalOnDeliveryToo(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	sender := uuid.New()
	recipientWhoBlockedSender := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{sender, recipientWhoBlockedSender}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	recipientConn := hub.Register(recipientWhoBlockedSender, "eu")

	blocksCache := NewBlocksCache(client, nil)
	// The recipient blocked the sender — ownership is directional in
	// Postgres (internal/blocks.Repo), but the cache's AddPair call cmd/api
	// makes is symmetric either way, exactly like this.
	if err := blocksCache.AddPair(ctx, recipientWhoBlockedSender, sender); err != nil {
		t.Fatalf("seed blocks cache: %v", err)
	}

	registry := NewRegistry(client, nil)
	publisher := NewPublisher(client, nil)
	delivery := NewDelivery(hub, cache, nil, blocksCache, nil, registry, publisher, discardLogger())
	fanout := NewFanout(nil, delivery, NewDedup(client, uuid.NewString(), nil), nil, discardLogger())

	payload := events.MessageCreatedPayload{
		MessageID: uuid.New(), ChannelID: channelID, SenderID: sender,
		ClientMessageID: uuid.New(), Sequence: 1, Body: "hello", CreatedAt: time.Now().UTC(),
	}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := fanout.handle(ctx, kafkago.Message{Value: value}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	select {
	case <-recipientConn.send:
		t.Fatalf("expected no delivery: recipient blocked the sender, even though the sender didn't block anyone")
	case <-time.After(200 * time.Millisecond):
	}
}
