package kafka

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	kafkago "github.com/segmentio/kafka-go"
)

// testBrokers mirrors internal/events/publisher_test.go's pattern: the
// docker-compose dev stack's host-exposed Kafka port, skipping (not
// failing) the test when it isn't reachable.
func testBrokers(t *testing.T) []string {
	t.Helper()
	addr := os.Getenv("KAFKA_TEST_BROKERS")
	if addr == "" {
		addr = "localhost:29092"
	}
	conn, err := kafkago.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Skipf("kafka not reachable at %s (start the stack: make up): %v", addr, err)
	}
	_ = conn.Close()
	return []string{addr}
}

// produceRetrying retries WriteMessages briefly against a brand-new topic:
// the very first write can race the broker's own auto-topic-creation and
// come back "Unknown Topic Or Partition" even though AllowAutoTopicCreation
// triggered creation moments earlier.
func produceRetrying(t *testing.T, writer *kafkago.Writer, msgs ...kafkago.Message) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = writer.WriteMessages(context.Background(), msgs...); lastErr == nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("produce messages: %v", lastErr)
}

// TestLagPoller_ReportsRemainingMessagesAfterPartialConsumption produces N
// messages to a fresh topic, consumes and commits all but k of them under a
// dedicated consumer group, and verifies the poller reports exactly k lag —
// proving it reads the real committed offset from the broker rather than
// whatever a Reader's own internal counters last happened to observe.
func TestLagPoller_ReportsRemainingMessagesAfterPartialConsumption(t *testing.T) {
	brokers := testBrokers(t)
	topic := "lag-poller-test-" + uuid.NewString()
	groupID := "lag-poller-test-group-" + uuid.NewString()

	writer := NewProducer(brokers)
	t.Cleanup(func() { _ = writer.Close() })

	const total = 10
	const consume = 6
	msgs := make([]kafkago.Message, total)
	for i := range total {
		msgs[i] = kafkago.Message{Topic: topic, Value: []byte("msg")}
	}
	produceRetrying(t, writer, msgs...)

	reader := NewConsumer(brokers, []string{topic}, groupID)
	t.Cleanup(func() { _ = reader.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for range consume {
		if _, err := reader.ReadMessage(ctx); err != nil {
			t.Fatalf("consume message: %v", err)
		}
	}

	tracker := newLagTracker()
	poller := NewLagPoller(brokers, groupID, []string{topic}, testLogger(), tracker.record)

	// Poll a few times with a short pause — OffsetFetch can briefly lag the
	// broker's own commit bookkeeping right after ReadMessage returns. The
	// auto-created topic may have more than one partition (the broker's
	// configured default), so total lag is a sum across whichever
	// partitions this round reported, not a single scalar.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		poller.pollOnce(ctx)
		if got := tracker.total(topic); got == total-consume {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}

	t.Fatalf("expected total lag %d for topic %s, got %d (by partition: %v)", total-consume, topic, tracker.total(topic), tracker.snapshot(topic))
}

// TestLagPoller_ZeroAfterFullConsumption confirms lag reaches zero once
// every produced message has been consumed and committed.
func TestLagPoller_ZeroAfterFullConsumption(t *testing.T) {
	brokers := testBrokers(t)
	topic := "lag-poller-test-" + uuid.NewString()
	groupID := "lag-poller-test-group-" + uuid.NewString()

	writer := NewProducer(brokers)
	t.Cleanup(func() { _ = writer.Close() })

	const total = 5
	msgs := make([]kafkago.Message, total)
	for i := range total {
		msgs[i] = kafkago.Message{Topic: topic, Value: []byte("msg")}
	}
	produceRetrying(t, writer, msgs...)

	reader := NewConsumer(brokers, []string{topic}, groupID)
	t.Cleanup(func() { _ = reader.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for range total {
		if _, err := reader.ReadMessage(ctx); err != nil {
			t.Fatalf("consume message: %v", err)
		}
	}

	tracker := newLagTracker()
	poller := NewLagPoller(brokers, groupID, []string{topic}, testLogger(), tracker.record)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		poller.pollOnce(ctx)
		if tracker.total(topic) == 0 && tracker.hasReported(topic) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}

	t.Fatalf("expected zero lag for topic %s after full consumption, got %d (by partition: %v)", topic, tracker.total(topic), tracker.snapshot(topic))
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// lagTracker accumulates per-partition lag reports across repeated
// pollOnce calls into a running per-topic view — each report overwrites
// only its own partition's entry, so totals stay correct across a
// multi-partition topic without conflating stale and fresh rounds.
type lagTracker struct {
	mu   sync.Mutex
	data map[string]map[int]int64
}

func newLagTracker() *lagTracker {
	return &lagTracker{data: make(map[string]map[int]int64)}
}

func (lt *lagTracker) record(topic string, partition int, lag int64) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.data[topic] == nil {
		lt.data[topic] = make(map[int]int64)
	}
	lt.data[topic][partition] = lag
}

func (lt *lagTracker) total(topic string) int64 {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	var sum int64
	for _, lag := range lt.data[topic] {
		sum += lag
	}
	return sum
}

func (lt *lagTracker) hasReported(topic string) bool {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return len(lt.data[topic]) > 0
}

func (lt *lagTracker) snapshot(topic string) map[int]int64 {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	out := make(map[int]int64, len(lt.data[topic]))
	maps.Copy(out, lt.data[topic])
	return out
}
