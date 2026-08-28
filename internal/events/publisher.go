package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/darkoatanasovski/chat/internal/platform/metrics"
)

// Publisher polls outbox_events on one physical shard and publishes to
// Kafka, deleting each row only after a successful publish
// (INSTRUCTIONS.md §16). One Publisher runs per physical shard (cmd/worker).
type Publisher struct {
	pool    *pgxpool.Pool
	writer  *kafkago.Writer
	metrics *metrics.Metrics
}

func NewPublisher(pool *pgxpool.Pool, writer *kafkago.Writer, m *metrics.Metrics) *Publisher {
	return &Publisher{pool: pool, writer: writer, metrics: m}
}

// PollOnce publishes up to batchSize pending events and returns how many it
// published. Consumers must be idempotent (dedup by event_id) because a
// process crash between publish and delete can redeliver an event
// (INSTRUCTIONS.md §16, §28: prefer at-least-once + idempotency).
func (p *Publisher) PollOnce(ctx context.Context, batchSize int) (int, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT event_id, event_type, channel_id, payload, created_at
		FROM outbox_events
		ORDER BY created_at
		LIMIT $1
	`, batchSize)
	if err != nil {
		return 0, fmt.Errorf("events: poll outbox: %w", err)
	}

	var pending []OutboxRow
	for rows.Next() {
		var r OutboxRow
		if err := rows.Scan(&r.EventID, &r.EventType, &r.ChannelID, &r.Payload, &r.CreatedAt); err != nil {
			rows.Close()
			return 0, fmt.Errorf("events: scan outbox row: %w", err)
		}
		pending = append(pending, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("events: iterate outbox: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	return p.publishAndDelete(ctx, pending)
}

// publishAndDelete writes the whole poll batch to Kafka in a single
// WriteMessages call and issues a single batched DELETE for whatever
// succeeded, instead of round-tripping to Kafka and Postgres once per row.
// The per-row loop this replaced was the actual throughput ceiling: at
// sustained rates above ~70-75 msg/s on one shard it fell behind and
// outbox_events backed up, growing delivery latency without bound for as
// long as the overload continued (measured via tools/loadtest --rate).
//
// On a partial failure — some messages in the batch land, others don't —
// kafka-go reports one error per message via WriteErrors; only the rows that
// actually landed are deleted. The rest are left for the next PollOnce, safe
// because consumers are already required to be idempotent for exactly this
// reason (a crash between publish and delete can redeliver an event).
func (p *Publisher) publishAndDelete(ctx context.Context, pending []OutboxRow) (int, error) {
	msgs := make([]kafkago.Message, len(pending))
	for i, r := range pending {
		msgs[i] = kafkago.Message{
			Topic: r.EventType,
			Key:   []byte(r.ChannelID.String()),
			Value: r.Payload,
		}
	}

	start := time.Now()
	writeErr := p.writer.WriteMessages(ctx, msgs...)
	if p.metrics != nil {
		p.metrics.KafkaProducerLatency.Observe(time.Since(start).Seconds())
	}

	toDelete := pending
	if writeErr != nil {
		var werrs kafkago.WriteErrors
		if !errors.As(writeErr, &werrs) {
			return 0, fmt.Errorf("events: publish batch of %d: %w", len(pending), writeErr)
		}
		toDelete = make([]OutboxRow, 0, len(pending))
		for i, e := range werrs {
			if e == nil {
				toDelete = append(toDelete, pending[i])
			}
		}
		if len(toDelete) == 0 {
			return 0, fmt.Errorf("events: publish batch of %d: %w", len(pending), writeErr)
		}
	}

	ids := make([]string, len(toDelete))
	for i, r := range toDelete {
		ids[i] = r.EventID.String()
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM outbox_events WHERE event_id = ANY($1::uuid[])`, ids); err != nil {
		return 0, fmt.Errorf("events: delete %d published outbox rows: %w", len(ids), err)
	}
	return len(toDelete), nil
}

// Run polls forever at the given interval until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context, interval time.Duration, batchSize int) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := p.PollOnce(ctx, batchSize); err != nil {
				return err
			}
		}
	}
}
