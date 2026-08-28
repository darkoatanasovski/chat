package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/darkoatanasovski/chat/internal/platform/metrics"
)

// gatewayChannel is the Redis Pub/Sub channel a single gateway instance
// listens on for deliveries targeting connections it, and only it, holds
// locally. Named by gatewayID (see cmd/gateway/main.go), not region: once
// more than one gateway process can serve a region, region alone no longer
// identifies a destination.
func gatewayChannel(gatewayID string) string {
	return "gateway:push:" + gatewayID
}

// pushEnvelope is what crosses the wire on a gatewayChannel: one already
// -marshaled DeliveryFrame plus the specific users on the receiving
// instance it's meant for. Grouping multiple users into one envelope keeps
// this to one Publish call per target gateway per event, not one per user.
type pushEnvelope struct {
	UserIDs []uuid.UUID `json:"user_ids"`
	Frame   []byte      `json:"frame"`
}

// Publisher hands a delivery frame to whichever gateway instance(s) hold a
// message's non-local recipients — the cross-process half of Fanout.handle,
// using Registry.GatewaysForUsers to find where. Pub/Sub is at-most-once:
// consistent with the rest of realtime delivery (see writePump's doc
// comment), a missed push is recovered by the client's existing
// reconnect-and-refetch path, not retried here.
type Publisher struct {
	redis   *redis.Client
	metrics *metrics.Metrics
}

func NewPublisher(redisClient *redis.Client, m *metrics.Metrics) *Publisher {
	return &Publisher{redis: redisClient, metrics: m}
}

// Push delivers frame to userIDs' connections on gatewayID. Callers group
// their remote members by target gateway first (see Fanout.handle) so this
// is called once per destination gateway, not once per user.
func (p *Publisher) Push(ctx context.Context, gatewayID string, userIDs []uuid.UUID, frame []byte) error {
	return p.metrics.TimeRedis("publisher_push", func() error {
		data, err := json.Marshal(pushEnvelope{UserIDs: userIDs, Frame: frame})
		if err != nil {
			return fmt.Errorf("realtime: marshal push envelope: %w", err)
		}
		if err := p.redis.Publish(ctx, gatewayChannel(gatewayID), data).Err(); err != nil {
			return fmt.Errorf("realtime: publish to gateway %s: %w", gatewayID, err)
		}
		return nil
	})
}

// Subscriber listens on this instance's own gatewayChannel and hands
// whatever arrives to the local Hub — the receiving half of Publisher.Push,
// using exactly the same Hub.DeliverToUser path a local delivery would.
type Subscriber struct {
	redis     *redis.Client
	gatewayID string
	hub       *Hub
	log       *slog.Logger
}

func NewSubscriber(redisClient *redis.Client, gatewayID string, hub *Hub, log *slog.Logger) *Subscriber {
	return &Subscriber{redis: redisClient, gatewayID: gatewayID, hub: hub, log: log}
}

// Run blocks, delivering pushes until ctx is cancelled.
func (s *Subscriber) Run(ctx context.Context) error {
	pubsub := s.redis.Subscribe(ctx, gatewayChannel(s.gatewayID))
	defer pubsub.Close()

	go func() {
		<-ctx.Done()
		_ = pubsub.Close()
	}()

	for msg := range pubsub.Channel() {
		s.handle(msg.Payload)
	}
	return ctx.Err()
}

func (s *Subscriber) handle(payload string) {
	var env pushEnvelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		s.log.Error("pubsub: unmarshal push envelope", "error", err)
		return
	}
	for _, userID := range env.UserIDs {
		s.hub.DeliverToUser(userID, env.Frame)
	}
}
