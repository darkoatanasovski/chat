package realtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/darkoatanasovski/chat/internal/events"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Regression test for the scenario this architecture change exists to
// support: every gateway shares one Kafka consumer group now, so a given
// message is handled by exactly one instance — it can no longer assume "not
// local" means "not connected anywhere." A member connected to a different
// gateway instance must still receive the message, routed through
// Registry + Publisher/Subscriber rather than Hub directly.
func TestFanout_DeliversLocalAndRemoteMembers(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	localUser := uuid.New()
	remoteUser := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{localUser, remoteUser}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	// hubHere belongs to the instance processing the message.
	hubHere := NewHub(nil)
	localConn := hubHere.Register(localUser, "eu")

	// hubRemote simulates a second gateway instance holding remoteUser's
	// connection. The Fanout under test never touches hubRemote directly —
	// it can only reach it through Registry + Publisher, exactly as two
	// real, separate processes would.
	hubRemote := NewHub(nil)
	remoteConn := hubRemote.Register(remoteUser, "eu")

	registry := NewRegistry(client, nil)
	remoteGatewayID := "eu-remote-" + uuid.NewString()
	if err := registry.Register(ctx, remoteUser, remoteConn.ID, "eu", remoteGatewayID); err != nil {
		t.Fatalf("register remote connection: %v", err)
	}
	t.Cleanup(func() { _ = registry.Unregister(ctx, remoteUser, remoteConn.ID) })

	publisher := NewPublisher(client, nil)
	subscriber := NewSubscriber(client, remoteGatewayID, hubRemote, discardLogger())
	subCtx, cancelSub := context.WithCancel(ctx)
	defer cancelSub()
	go subscriber.Run(subCtx)
	waitForSubscriber(t, client, gatewayChannel(remoteGatewayID))

	delivery := NewDelivery(hubHere, cache, nil, registry, publisher, discardLogger())
	fanout := NewFanout(nil, delivery, NewDedup(client, uuid.NewString(), nil), nil, discardLogger())

	payload := events.MessageCreatedPayload{
		MessageID:       uuid.New(),
		ChannelID:       channelID,
		SenderID:        localUser,
		ClientMessageID: uuid.New(),
		Sequence:        1,
		Body:            "hello from local",
		CreatedAt:       time.Now().UTC(),
	}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	msg := kafkago.Message{Value: value}

	if err := fanout.handle(ctx, msg); err != nil {
		t.Fatalf("handle: %v", err)
	}

	assertDelivered(t, "local", localConn)
	assertDelivered(t, "remote (via registry + pubsub)", remoteConn)
}

// TestFanout_MemberWithNoLiveConnectionIsSkipped confirms a member who
// isn't connected anywhere — locally or on record in Registry — is skipped
// silently rather than erroring, the same best-effort contract a purely
// local delivery already has.
func TestFanout_MemberWithNoLiveConnectionIsSkipped(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	offlineUser := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{offlineUser}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	registry := NewRegistry(client, nil)
	publisher := NewPublisher(client, nil)
	delivery := NewDelivery(hub, cache, nil, registry, publisher, discardLogger())
	fanout := NewFanout(nil, delivery, NewDedup(client, uuid.NewString(), nil), nil, discardLogger())

	payload := events.MessageCreatedPayload{
		MessageID:       uuid.New(),
		ChannelID:       channelID,
		SenderID:        offlineUser,
		ClientMessageID: uuid.New(),
		Sequence:        1,
		Body:            "nobody home",
		CreatedAt:       time.Now().UTC(),
	}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := fanout.handle(ctx, kafkago.Message{Value: value}); err != nil {
		t.Fatalf("expected no error for a member with no live connection, got: %v", err)
	}
}

// TestFanout_DeliversReactionUpdated proves reaction.updated goes through
// the exact same local/remote delivery path as message.created — it's
// dispatched by msg.Topic, not a separate consumer.
func TestFanout_DeliversReactionUpdated(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	localUser := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{localUser}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	localConn := hub.Register(localUser, "eu")

	registry := NewRegistry(client, nil)
	publisher := NewPublisher(client, nil)
	delivery := NewDelivery(hub, cache, nil, registry, publisher, discardLogger())
	fanout := NewFanout(nil, delivery, NewDedup(client, uuid.NewString(), nil), nil, discardLogger())

	payload := events.ReactionUpdatedPayload{
		EventID:         uuid.New(),
		ChannelID:       channelID,
		MessageID:       uuid.New(),
		ActorID:         localUser,
		Reaction:        "like",
		Action:          "added",
		ReactionCounts:  map[string]int{"like": 1},
		LatestReactions: []events.ReactionSummary{{Reaction: "like", UserID: localUser, CreatedAt: time.Now().UTC()}},
	}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := fanout.handle(ctx, kafkago.Message{Topic: events.TopicReactionUpdated, Value: value}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	assertDelivered(t, "local", localConn)
}

// TestFanout_ReactionUpdatedDedupsByEventID confirms a redelivered
// reaction.updated (e.g. after an outbox publish/delete crash) is only
// pushed once — the same at-least-once + idempotent-consumer contract
// message.created already has, just keyed by EventID instead of
// (channel_id, sequence) since a (message, user, reaction) triple alone
// isn't unique across a toggle add/remove/add history.
func TestFanout_ReactionUpdatedDedupsByEventID(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	localUser := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{localUser}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	localConn := hub.Register(localUser, "eu")

	registry := NewRegistry(client, nil)
	publisher := NewPublisher(client, nil)
	delivery := NewDelivery(hub, cache, nil, registry, publisher, discardLogger())
	fanout := NewFanout(nil, delivery, NewDedup(client, uuid.NewString(), nil), nil, discardLogger())

	payload := events.ReactionUpdatedPayload{
		EventID: uuid.New(), ChannelID: channelID, MessageID: uuid.New(), ActorID: localUser,
		Reaction: "celebrate", Action: "added", ReactionCounts: map[string]int{"celebrate": 1},
	}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	msg := kafkago.Message{Topic: events.TopicReactionUpdated, Value: value}

	if err := fanout.handle(ctx, msg); err != nil {
		t.Fatalf("first handle: %v", err)
	}
	assertDelivered(t, "first delivery", localConn)

	if err := fanout.handle(ctx, msg); err != nil {
		t.Fatalf("redelivered handle: %v", err)
	}
	select {
	case <-localConn.send:
		t.Fatalf("expected the redelivered event to be deduped, got a second delivery frame")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestFanout_DeliversReadUpdated proves read.updated goes through the same
// dispatch-by-topic + Delivery path as message.created/reaction.updated.
func TestFanout_DeliversReadUpdated(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	localUser := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{localUser}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	localConn := hub.Register(localUser, "eu")

	registry := NewRegistry(client, nil)
	publisher := NewPublisher(client, nil)
	delivery := NewDelivery(hub, cache, nil, registry, publisher, discardLogger())
	fanout := NewFanout(nil, delivery, NewDedup(client, uuid.NewString(), nil), nil, discardLogger())

	payload := events.ReadUpdatedPayload{
		EventID: uuid.New(), ChannelID: channelID, UserID: localUser, LastReadSequence: 5,
	}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := fanout.handle(ctx, kafkago.Message{Topic: events.TopicReadUpdated, Value: value}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	assertDelivered(t, "local", localConn)
}

// TestFanout_ReadUpdatedDedupsByEventID mirrors the reaction.updated dedup
// test — a redelivered read.updated (outbox publish/delete crash) is only
// pushed once.
func TestFanout_ReadUpdatedDedupsByEventID(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	cache := NewMembershipCache(client, nil)
	channelID := uuid.New()
	localUser := uuid.New()
	if err := cache.SetMembers(ctx, channelID, []uuid.UUID{localUser}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	hub := NewHub(nil)
	localConn := hub.Register(localUser, "eu")

	registry := NewRegistry(client, nil)
	publisher := NewPublisher(client, nil)
	delivery := NewDelivery(hub, cache, nil, registry, publisher, discardLogger())
	fanout := NewFanout(nil, delivery, NewDedup(client, uuid.NewString(), nil), nil, discardLogger())

	payload := events.ReadUpdatedPayload{EventID: uuid.New(), ChannelID: channelID, UserID: localUser, LastReadSequence: 3}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	msg := kafkago.Message{Topic: events.TopicReadUpdated, Value: value}

	if err := fanout.handle(ctx, msg); err != nil {
		t.Fatalf("first handle: %v", err)
	}
	assertDelivered(t, "first delivery", localConn)

	if err := fanout.handle(ctx, msg); err != nil {
		t.Fatalf("redelivered handle: %v", err)
	}
	select {
	case <-localConn.send:
		t.Fatalf("expected the redelivered event to be deduped, got a second delivery frame")
	case <-time.After(200 * time.Millisecond):
	}
}

func assertDelivered(t *testing.T, label string, conn *Connection) {
	t.Helper()
	select {
	case <-conn.send:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: expected a delivery frame on the connection, got none", label)
	}
}

// waitForSubscriber polls Redis until it reports a live subscriber on
// channel, avoiding the race between a goroutine's Subscribe() taking
// effect server-side and this test's next Publish() — Pub/Sub has no
// backlog, so a Publish before the SUBSCRIBE is processed is simply lost.
func waitForSubscriber(t *testing.T, client *redis.Client, channel string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		counts, err := client.PubSubNumSub(context.Background(), channel).Result()
		if err == nil && counts[channel] > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber on %q never became visible to Redis", channel)
}
