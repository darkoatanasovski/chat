// Package kafka wraps segmentio/kafka-go for the durable event backbone
// (INSTRUCTIONS.md §14/§15). Channel-related events are always keyed by
// channel_id so per-channel ordering is preserved within a partition.
package kafka

import (
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// NewProducer creates a writer not bound to a single topic: the outbox
// publisher sets Message.Topic per event (event_type), so future event types
// need no new writer.
func NewProducer(brokers []string) *kafkago.Writer {
	return &kafkago.Writer{
		Addr:                   kafkago.TCP(brokers...),
		Balancer:               &kafkago.Hash{}, // partition by Message.Key (channel_id)
		RequiredAcks:           kafkago.RequireOne,
		AllowAutoTopicCreation: true,
		// The outbox publisher submits one WriteMessages call per poll batch
		// (see internal/events/publisher.go), but kafka-go still queues that
		// batch per-partition and flushes on BatchSize or BatchTimeout,
		// whichever comes first. A small poll batch would otherwise sit
		// waiting out the default 1s timeout even though every message it
		// needs already arrived in the same call. Flush near-immediately
		// instead.
		BatchTimeout: 10 * time.Millisecond,
	}
}

// NewConsumer subscribes one consumer-group membership to every topic in
// topics (kafka-go's GroupTopics) rather than one Reader per topic — running
// two Readers under the same GroupID but different single Topics would be
// two separate group memberships each assuming they own the whole group,
// which Kafka's assignors aren't guaranteed to handle sanely. One Reader,
// one membership, multiple topics is the supported way to do this.
func NewConsumer(brokers []string, topics []string, groupID string) *kafkago.Reader {
	return kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:     brokers,
		GroupTopics: topics,
		GroupID:     groupID,
		// Multiple gateway instances can now join the same consumer group
		// (see cmd/gateway/main.go) and be the very first thing to ever
		// touch message.created, which relies on auto-creation — if every
		// member's initial JoinGroup lands in the narrow window before the
		// topic's partitions are fully visible, the group's leader can
		// compute (and get stuck on) a zero-partition assignment, since
		// kafka-go otherwise never re-triggers a rebalance just because
		// partition count changed. Watching for partition changes makes
		// that self-heal within one PartitionWatchInterval instead of
		// requiring every member to restart.
		WatchPartitionChanges: true,
		// Left at the zero value, kafka-go commits synchronously (a broker
		// round trip) after every single ReadMessage call — on Fanout's
		// single-threaded consume loop (internal/realtime/fanout.go) that
		// round trip serializes directly into per-message delivery latency.
		// Batching commits is safe here because at-least-once redelivery is
		// already a first-class case: Dedup (internal/realtime/dedup.go)
		// makes re-processing whatever this interval leaves uncommitted
		// after a crash or rebalance a no-op.
		CommitInterval: time.Second,
	})
}
