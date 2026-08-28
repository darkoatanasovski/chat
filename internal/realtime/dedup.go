package realtime

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const dedupTTL = 10 * time.Minute

// Dedup makes Kafka fanout consumption idempotent per consumer group
// (INSTRUCTIONS.md §16/§49 rule 9): at-least-once delivery from Kafka is
// expected, so a group must recognize and skip an event it has already
// processed (e.g. after a rebalance hands an in-flight partition to a
// different instance, or a restart re-reads from the last committed
// offset).
//
// The namespace passed to NewDedup must be the Kafka consumer group name
// (cfg.KafkaConsumerGroup), not any one instance's gatewayID. All gateway
// instances now share a single consumer group — Kafka's own partition
// assignment ensures each message is handled by exactly one of them, and
// Fanout routes non-local members to whichever instance actually holds
// their connection (see pubsub.go) — so every member of the group must
// agree on what's already been processed. This key previously used to be
// namespaced per gatewayID from an earlier design where every gateway ran
// its own independent, full-stream consumer group; that let whichever
// group's consumer won a race claim the key globally and silently skip
// delivery in every other group. Namespacing by the (now singular) group
// name is what avoids both bugs: group-mates coordinate correctly on one
// namespace, and there is no longer a second group to collide with.
type Dedup struct {
	redis     *redis.Client
	namespace string
}

func NewDedup(redisClient *redis.Client, namespace string) *Dedup {
	return &Dedup{redis: redisClient, namespace: namespace}
}

// SeenBefore atomically marks eventID as processed by this gateway and
// reports whether it had already been seen by this gateway.
func (d *Dedup) SeenBefore(ctx context.Context, eventID string) (bool, error) {
	key := "dedup:event:" + d.namespace + ":" + eventID
	ok, err := d.redis.SetNX(ctx, key, 1, dedupTTL).Result()
	if err != nil {
		return false, fmt.Errorf("realtime: dedup check: %w", err)
	}
	return !ok, nil
}
