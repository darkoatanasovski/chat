package kafka

import (
	"context"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// LagPoller periodically reports per-partition consumer-group lag by
// querying the broker directly — the log-end offset via ListOffsets and the
// group's committed offset via OffsetFetch — rather than relying on a
// consuming Reader's own Stats().Lag, which only reflects whichever
// partition it last happened to read a message from, not a true per-
// partition or aggregate figure. Lag is a group-level property, not a
// per-consumer one, so every gateway instance in the group polling and
// reporting the same numbers is redundant but harmless, not wrong: whichever
// instance Prometheus scrapes next just sees the same, still-correct value.
type LagPoller struct {
	client  *kafkago.Client
	groupID string
	topics  []string
	log     *slog.Logger
	onLag   func(topic string, partition int, lag int64)
}

func NewLagPoller(brokers []string, groupID string, topics []string, log *slog.Logger, onLag func(topic string, partition int, lag int64)) *LagPoller {
	return &LagPoller{
		client:  &kafkago.Client{Addr: kafkago.TCP(brokers...)},
		groupID: groupID,
		topics:  topics,
		log:     log,
		onLag:   onLag,
	}
}

// Run blocks, polling every interval until ctx is cancelled. A failed poll
// (broker unreachable, group not yet formed) is logged and skipped — this is
// an observability signal, not something worth retrying aggressively or
// failing the process over.
func (p *LagPoller) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *LagPoller) pollOnce(ctx context.Context) {
	meta, err := p.client.Metadata(ctx, &kafkago.MetadataRequest{Topics: p.topics})
	if err != nil {
		p.log.Error("kafka lag poller: metadata", "error", err)
		return
	}

	partitionsByTopic := make(map[string][]int)
	offsetReqByTopic := make(map[string][]kafkago.OffsetRequest)
	for _, topic := range meta.Topics {
		if topic.Error != nil {
			continue
		}
		for _, part := range topic.Partitions {
			partitionsByTopic[topic.Name] = append(partitionsByTopic[topic.Name], part.ID)
			// Both ends: LastOffset is the lag ceiling, FirstOffset is the
			// fallback floor for a partition the group has never committed
			// on yet (see below) — one round trip covers both.
			offsetReqByTopic[topic.Name] = append(offsetReqByTopic[topic.Name],
				kafkago.FirstOffsetOf(part.ID), kafkago.LastOffsetOf(part.ID))
		}
	}
	if len(partitionsByTopic) == 0 {
		return
	}

	endOffsets, err := p.client.ListOffsets(ctx, &kafkago.ListOffsetsRequest{Topics: offsetReqByTopic})
	if err != nil {
		p.log.Error("kafka lag poller: list offsets", "error", err)
		return
	}

	committed, err := p.client.OffsetFetch(ctx, &kafkago.OffsetFetchRequest{
		GroupID: p.groupID,
		Topics:  partitionsByTopic,
	})
	if err != nil {
		p.log.Error("kafka lag poller: offset fetch", "group", p.groupID, "error", err)
		return
	}

	committedByPartition := make(map[string]map[int]int64)
	for topic, parts := range committed.Topics {
		byPart := make(map[int]int64, len(parts))
		for _, part := range parts {
			if part.Error != nil || part.CommittedOffset < 0 {
				// No committed offset yet — the group has never consumed
				// this partition (fresh group, or a consumer that crashed
				// before its first commit). That's not zero lag: it's
				// reported below using FirstOffset as the baseline instead,
				// so a stuck-before-ever-committing consumer still shows up
				// as real backlog rather than silently reporting nothing.
				continue
			}
			byPart[part.Partition] = part.CommittedOffset
		}
		committedByPartition[topic] = byPart
	}

	for topic, parts := range endOffsets.Topics {
		byPart := committedByPartition[topic]
		for _, part := range parts {
			if part.Error != nil {
				continue
			}
			baseline, hasCommit := byPart[part.Partition]
			if !hasCommit {
				baseline = part.FirstOffset
			}
			lag := max(part.LastOffset-baseline, 0)
			p.onLag(topic, part.Partition, lag)
		}
	}
}
