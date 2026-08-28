package events

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/darkoatanasovski/chat/internal/storage/kafka"
)

// testShardPool connects to the shard-a Postgres exposed by the local
// docker-compose dev stack, skipping the test if it isn't reachable.
func testShardPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SHARD_A_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://chat:chat@localhost:5434/chat?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("shard-a postgres not reachable (start the stack: make up): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("shard-a postgres not reachable (start the stack: make up): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func testKafkaWriter(t *testing.T) *kafkago.Writer {
	t.Helper()
	addr := os.Getenv("KAFKA_TEST_BROKERS")
	if addr == "" {
		addr = "localhost:29092"
	}
	w := kafka.NewProducer([]string{addr})
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func countOutboxRows(t *testing.T, pool *pgxpool.Pool, eventID uuid.UUID) int {
	t.Helper()
	var count int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox_events WHERE event_id = $1`, eventID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	return count
}

// insertTestOutboxRow inserts directly (bypassing InsertOutbox's V7-generated
// ID) so the test controls the event_id and can assert on it precisely.
func insertTestOutboxRow(t *testing.T, pool *pgxpool.Pool, channelID uuid.UUID) uuid.UUID {
	t.Helper()
	eventID := uuid.New()
	payload := MessageCreatedPayload{
		MessageID: uuid.New(),
		ChannelID: channelID,
		SenderID:  uuid.New(),
		Sequence:  1,
		Body:      "test message " + eventID.String(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	_, err = pool.Exec(context.Background(), `
		INSERT INTO outbox_events (event_id, event_type, channel_id, payload, created_at)
		VALUES ($1, $2, $3, $4, now())
	`, eventID, TopicMessageCreated, channelID, data)
	if err != nil {
		t.Fatalf("seed outbox row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM outbox_events WHERE event_id = $1`, eventID)
	})
	return eventID
}

// insertTestOutboxRowsBatched seeds n rows in a single round trip via
// CopyFrom, instead of n sequential single-row inserts. The dev stack's
// real worker-outbox-a container is always running against this same
// shard, polling every 250ms — n sequential inserts for a large n can span
// enough wall-clock time for that poll to fire mid-seed and claim the
// earliest rows before the test's own PollOnce ever sees them. A single
// batched insert keeps the seeding window well under 250ms.
func insertTestOutboxRowsBatched(t *testing.T, pool *pgxpool.Pool, channelID uuid.UUID, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, n)
	rows := make([][]any, n)
	for i := range n {
		eventID := uuid.New()
		payload := MessageCreatedPayload{
			MessageID: uuid.New(),
			ChannelID: channelID,
			SenderID:  uuid.New(),
			Sequence:  int64(i + 1),
			Body:      "test message " + eventID.String(),
		}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		ids[i] = eventID
		rows[i] = []any{eventID, TopicMessageCreated, channelID, data, time.Now().UTC()}
	}

	_, err := pool.CopyFrom(context.Background(),
		pgx.Identifier{"outbox_events"},
		[]string{"event_id", "event_type", "channel_id", "payload", "created_at"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		t.Fatalf("batch seed %d outbox rows: %v", n, err)
	}
	t.Cleanup(func() {
		idStrs := make([]string, n)
		for i, id := range ids {
			idStrs[i] = id.String()
		}
		_, _ = pool.Exec(context.Background(), `DELETE FROM outbox_events WHERE event_id = ANY($1::uuid[])`, idStrs)
	})
	return ids
}

func TestInsertOutbox_VisibleAfterCommit(t *testing.T) {
	pool := testShardPool(t)
	ctx := context.Background()
	channelID := uuid.New()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	payload := MessageCreatedPayload{MessageID: uuid.New(), ChannelID: channelID, SenderID: uuid.New(), Sequence: 1, Body: "hi"}
	if err := InsertOutbox(ctx, tx, TopicMessageCreated, channelID, payload); err != nil {
		t.Fatalf("insert outbox: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE channel_id = $1`, channelID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 outbox row after commit, got %d", count)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM outbox_events WHERE channel_id = $1`, channelID)
}

// TestInsertOutbox_RolledBackTransactionLeavesNoRow is the core guarantee of
// the transactional outbox pattern: if the domain write InsertOutbox
// accompanies fails and the transaction rolls back, the event must not
// exist either — otherwise a consumer could observe an event for a message
// that was never actually persisted.
func TestInsertOutbox_RolledBackTransactionLeavesNoRow(t *testing.T) {
	pool := testShardPool(t)
	ctx := context.Background()
	channelID := uuid.New()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	payload := MessageCreatedPayload{MessageID: uuid.New(), ChannelID: channelID, SenderID: uuid.New(), Sequence: 1, Body: "should not persist"}
	if err := InsertOutbox(ctx, tx, TopicMessageCreated, channelID, payload); err != nil {
		t.Fatalf("insert outbox: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE channel_id = $1`, channelID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 outbox rows after rollback, got %d", count)
	}
}

func TestPublisher_PollOnce_PublishesAndDeletes(t *testing.T) {
	pool := testShardPool(t)
	writer := testKafkaWriter(t)
	channelID := uuid.New()
	eventID := insertTestOutboxRow(t, pool, channelID)

	pub := NewPublisher(pool, writer, nil)
	published, err := pub.PollOnce(context.Background(), 100)
	if err != nil {
		t.Fatalf("poll once: %v", err)
	}
	if published == 0 {
		t.Fatalf("expected at least 1 event published, got 0")
	}

	if got := countOutboxRows(t, pool, eventID); got != 0 {
		t.Fatalf("expected the published row to be deleted, still found %d", got)
	}
}

func TestPublisher_PollOnce_NothingPendingIsNoop(t *testing.T) {
	pool := testShardPool(t)
	writer := testKafkaWriter(t)

	// Drain whatever might be pending from other tests/processes first so
	// this assertion is meaningful.
	pub := NewPublisher(pool, writer, nil)
	for {
		n, err := pub.PollOnce(context.Background(), 500)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		if n == 0 {
			break
		}
	}

	published, err := pub.PollOnce(context.Background(), 100)
	if err != nil {
		t.Fatalf("poll once on empty outbox: %v", err)
	}
	if published != 0 {
		t.Fatalf("expected 0 published with nothing pending, got %d", published)
	}
}

func TestPublisher_PollOnce_RespectsBatchSize(t *testing.T) {
	pool := testShardPool(t)
	writer := testKafkaWriter(t)
	channelID := uuid.New()

	const seeded = 5
	const batchSize = 2
	ids := make([]uuid.UUID, seeded)
	for i := range seeded {
		ids[i] = insertTestOutboxRow(t, pool, channelID)
	}

	pub := NewPublisher(pool, writer, nil)
	published, err := pub.PollOnce(context.Background(), batchSize)
	if err != nil {
		t.Fatalf("poll once: %v", err)
	}
	if published != batchSize {
		t.Fatalf("expected exactly batchSize=%d published in one poll, got %d", batchSize, published)
	}

	remaining := 0
	for _, id := range ids {
		remaining += countOutboxRows(t, pool, id)
	}
	if remaining != seeded-batchSize {
		t.Fatalf("expected %d rows still pending after a partial batch, got %d", seeded-batchSize, remaining)
	}

	// Drain the rest so the cleanup deletes below are no-ops either way.
	if _, err := pub.PollOnce(context.Background(), seeded); err != nil {
		t.Fatalf("drain remainder: %v", err)
	}
}

// TestPublisher_PollOnce_PublishesLargeBatchInOneRoundTrip is a regression
// test for the sequential per-message publish loop this replaced: publishing
// row-by-row (each awaiting its own Kafka ack, then its own DELETE) measured
// at ~70-75 msg/s sustained on one shard under tools/loadtest --rate. A
// single batched WriteMessages call plus one batched DELETE should publish
// hundreds of rows in well under a second, not seconds growing linearly with
// row count.
func TestPublisher_PollOnce_PublishesLargeBatchInOneRoundTrip(t *testing.T) {
	pool := testShardPool(t)
	writer := testKafkaWriter(t)
	channelID := uuid.New()

	const seeded = 300
	ids := insertTestOutboxRowsBatched(t, pool, channelID, seeded)

	pub := NewPublisher(pool, writer, nil)
	start := time.Now()
	published, err := pub.PollOnce(context.Background(), seeded)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("poll once: %v", err)
	}
	if published != seeded {
		t.Fatalf("expected all %d seeded rows published in one poll, got %d", seeded, published)
	}
	// The old sequential implementation took roughly seeded * ~10ms+ (one
	// Kafka round trip per row) at best, ~seeded seconds at worst (pre the
	// BatchTimeout fix). 2s is a generous ceiling for one batched call.
	if elapsed > 2*time.Second {
		t.Fatalf("expected a %d-row batch to publish in well under 2s via one round trip, took %v", seeded, elapsed)
	}

	remaining := 0
	for _, id := range ids {
		remaining += countOutboxRows(t, pool, id)
	}
	if remaining != 0 {
		t.Fatalf("expected all rows deleted after a fully successful batch publish, %d still pending", remaining)
	}
}
