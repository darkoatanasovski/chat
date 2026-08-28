package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/darkoatanasovski/chat/internal/events"
)

// fakeMessageSource is an in-memory MessageSource: FetchMessage serves a
// fixed backlog in order, then blocks on ctx.Done() once exhausted — the
// same behavior a real *kafkago.Reader has when there's simply nothing new
// to consume yet. CommitMessages just records what it was given, in call
// order, so tests can assert Run() only ever commits in fetch order.
type fakeMessageSource struct {
	mu        sync.Mutex
	backlog   []kafkago.Message
	idx       int
	committed []kafkago.Message
}

func newFakeMessageSource(msgs ...kafkago.Message) *fakeMessageSource {
	return &fakeMessageSource{backlog: msgs}
}

func (f *fakeMessageSource) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	f.mu.Lock()
	if f.idx < len(f.backlog) {
		m := f.backlog[f.idx]
		f.idx++
		f.mu.Unlock()
		return m, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return kafkago.Message{}, ctx.Err()
}

func (f *fakeMessageSource) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	f.mu.Lock()
	f.committed = append(f.committed, msgs...)
	f.mu.Unlock()
	return nil
}

// committedValues returns each committed message's Body field (the test
// marker set by msgForChannel), in commit order — not the raw JSON, which
// also carries a fresh random message_id/sender_id/created_at per call and
// would never compare equal across two constructions of "the same" message.
func (f *fakeMessageSource) committedValues() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.committed))
	for i, m := range f.committed {
		var payload events.MessageCreatedPayload
		if err := json.Unmarshal(m.Value, &payload); err != nil {
			out[i] = string(m.Value) // malformed-payload test case
			continue
		}
		out[i] = payload.Body
	}
	return out
}

// distinctShardIDs returns two UUIDs guaranteed to hash to different shards
// under shardIndex(_, numShards) — generating two random UUIDs directly
// would leave a 1/numShards chance of a collision, which is correct,
// expected behavior for Fanout.Run (same shard means strictly serial, by
// design) but would make a test asserting concurrency flaky rather than
// wrong.
func distinctShardIDs(t *testing.T, numShards int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	a := uuid.New()
	for range 10000 {
		b := uuid.New()
		if shardIndex(a, numShards) != shardIndex(b, numShards) {
			return a, b
		}
	}
	t.Fatalf("could not find two UUIDs on different shards out of %d shards after 10000 attempts", numShards)
	return uuid.Nil, uuid.Nil
}

func msgForChannel(t *testing.T, channelID uuid.UUID, marker string) kafkago.Message {
	t.Helper()
	payload := events.MessageCreatedPayload{
		MessageID: uuid.New(), ChannelID: channelID, SenderID: uuid.New(),
		ClientMessageID: uuid.New(), Sequence: 1, Body: marker, CreatedAt: time.Now().UTC(),
	}
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return kafkago.Message{Value: value}
}

// TestFanoutRun_CommitsInFetchOrderEvenWhenLaterMessageFinishesFirst is the
// correctness-critical guarantee the whole parallel dispatch design rests
// on: kafka-go's commit only ever tracks the *highest* offset it's told
// about per partition, so committing a later message before an earlier one
// has actually finished would let a crash between those two commits skip
// redelivering the earlier one forever. Two different channels are used so
// they land on different shards and genuinely run concurrently; channel A's
// message is deliberately the slower of the two to complete.
func TestFanoutRun_CommitsInFetchOrderEvenWhenLaterMessageFinishesFirst(t *testing.T) {
	const shards = 4
	channelA, channelB := distinctShardIDs(t, shards)
	msgA := msgForChannel(t, channelA, "A")
	msgB := msgForChannel(t, channelB, "B")
	source := newFakeMessageSource(msgA, msgB)

	release := make(chan struct{})
	started := make(chan string, 2)

	fanout := &Fanout{reader: source, log: discardLogger(), shards: shards}
	fanout.processFn = func(_ context.Context, msg kafkago.Message) error {
		var payload events.MessageCreatedPayload
		if err := json.Unmarshal(msg.Value, &payload); err != nil {
			return err
		}
		started <- payload.Body
		if payload.Body == "A" {
			<-release // A is held open until the test explicitly releases it
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- fanout.Run(ctx) }()

	// Both jobs should be in flight concurrently (A blocked, B free to run
	// to completion) before A is released.
	seen := map[string]bool{}
	for range 2 {
		select {
		case b := <-started:
			seen[b] = true
		case <-time.After(2 * time.Second):
			t.Fatal("both messages should have started processing concurrently")
		}
	}
	if !seen["A"] || !seen["B"] {
		t.Fatalf("expected both A and B to start, got %v", seen)
	}

	// B has certainly finished by now (nothing blocks it); A has not.
	// Give B's completion a moment to reach the commit log before asserting
	// nothing was committed yet — Run() must still be waiting on A.
	time.Sleep(100 * time.Millisecond)
	if got := source.committedValues(); len(got) != 0 {
		t.Fatalf("expected no commits before the earlier-fetched message (A) finishes, got %v", got)
	}

	close(release)

	deadline := time.After(2 * time.Second)
	for len(source.committedValues()) < 2 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for both commits, got %v", source.committedValues())
		case <-time.After(10 * time.Millisecond):
		}
	}

	if got := source.committedValues(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("expected commits in fetch order [A B] regardless of completion order, got %v", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("expected Run to return context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// TestFanoutRun_MalformedPayloadStillGetsProcessedAndCommitted proves a
// message whose channel_id can't be parsed for shard routing doesn't get
// silently dropped — it falls back to shard 0 and still goes through the
// exact same processing and commit path as everything else, so the real
// error (an unmarshal failure) surfaces from handle() itself rather than
// being swallowed earlier.
func TestFanoutRun_MalformedPayloadStillGetsProcessedAndCommitted(t *testing.T) {
	bad := kafkago.Message{Value: []byte("not json")}
	source := newFakeMessageSource(bad)

	fanout := &Fanout{reader: source, log: discardLogger(), shards: 4}
	processed := make(chan error, 1)
	fanout.processFn = func(_ context.Context, msg kafkago.Message) error {
		err := fmt.Errorf("simulated handle failure for malformed payload")
		processed <- err
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- fanout.Run(ctx) }()

	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("malformed-payload message was never dispatched to processFn")
	}

	deadline := time.After(2 * time.Second)
	for len(source.committedValues()) < 1 {
		select {
		case <-deadline:
			t.Fatal("malformed-payload message was processed but never committed")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-runErr
}

// TestFanoutRun_GracefulShutdownDrainsInFlightWork confirms that cancelling
// ctx while messages are still being processed doesn't discard their
// results: Run must wait for already-dispatched work to finish and commit
// it before returning, so a clean shutdown never re-delivers work it had
// actually already completed.
func TestFanoutRun_GracefulShutdownDrainsInFlightWork(t *testing.T) {
	channelA, channelB, channelC := uuid.New(), uuid.New(), uuid.New()
	msgs := []kafkago.Message{
		msgForChannel(t, channelA, "A"),
		msgForChannel(t, channelB, "B"),
		msgForChannel(t, channelC, "C"),
	}
	source := newFakeMessageSource(msgs...)

	fanout := &Fanout{reader: source, log: discardLogger(), shards: 4}
	inFlight := make(chan struct{}, 3)
	fanout.processFn = func(_ context.Context, _ kafkago.Message) error {
		time.Sleep(50 * time.Millisecond)
		inFlight <- struct{}{}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- fanout.Run(ctx) }()

	// Cancel almost immediately — before any of the three could plausibly
	// have finished — to exercise the shutdown-drain path, not just the
	// steady-state path already covered above.
	time.Sleep(5 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	if got := len(source.committedValues()); got != 3 {
		t.Fatalf("expected all 3 in-flight messages to be drained and committed on shutdown, got %d committed", got)
	}
}

// TestFanoutRun_EndToEnd_DifferentChannelsDeliverConcurrentlyWithRealHandle
// is the integration test tying the whole design together: Run(), the real
// (non-stubbed) handle()/Delivery/Hub/Registry, and Redis, driving three
// distinct channels' messages through the shard pool at once and confirming
// every recipient actually receives their frame — not just that the
// dispatch mechanics are sound in isolation.
func TestFanoutRun_EndToEnd_DifferentChannelsDeliverConcurrentlyWithRealHandle(t *testing.T) {
	client := testRedis(t)
	cache := NewMembershipCache(client, nil)
	hub := NewHub(nil)
	registry := NewRegistry(client, nil)
	publisher := NewPublisher(client, nil)
	delivery := NewDelivery(hub, cache, nil, registry, publisher, discardLogger())
	dedup := NewDedup(client, uuid.NewString(), nil)

	ctxSetup := context.Background()
	type recipient struct {
		channelID uuid.UUID
		conn      *Connection
	}
	var recipients []recipient
	var msgs []kafkago.Message
	for i := range 3 {
		channelID := uuid.New()
		userID := uuid.New()
		if err := cache.SetMembers(ctxSetup, channelID, []uuid.UUID{userID}); err != nil {
			t.Fatalf("seed membership %d: %v", i, err)
		}
		conn := hub.Register(userID, "eu")
		recipients = append(recipients, recipient{channelID: channelID, conn: conn})
		msgs = append(msgs, msgForChannel(t, channelID, fmt.Sprintf("msg-%d", i)))
	}

	source := newFakeMessageSource(msgs...)
	fanout := NewFanout(source, delivery, dedup, nil, discardLogger())
	fanout.SetShards(4)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- fanout.Run(ctx) }()

	for i, r := range recipients {
		assertDelivered(t, fmt.Sprintf("recipient %d", i), r.conn)
	}

	deadline := time.After(2 * time.Second)
	for len(source.committedValues()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for all 3 commits, got %v", source.committedValues())
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// TestFanoutRun_SameChannelStaysInOrderAcrossManyMessages stresses the
// property TestChannelShardPool_SameShardProcessesInSubmissionOrder already
// proves in isolation, but end to end through Run() with a real channel_id
// -> shard hash instead of an explicit shard index — many messages for one
// channel, interleaved with other channels, must still be handled by that
// channel's single shard strictly in fetch order.
func TestFanoutRun_SameChannelStaysInOrderAcrossManyMessages(t *testing.T) {
	hot := uuid.New()
	other := uuid.New()

	var msgs []kafkago.Message
	for i := range 20 {
		msgs = append(msgs, msgForChannel(t, hot, fmt.Sprintf("hot-%02d", i)))
		msgs = append(msgs, msgForChannel(t, other, fmt.Sprintf("other-%02d", i)))
	}
	source := newFakeMessageSource(msgs...)

	fanout := &Fanout{reader: source, log: discardLogger(), shards: 8}
	var mu sync.Mutex
	var hotOrder []string
	fanout.processFn = func(_ context.Context, msg kafkago.Message) error {
		var payload events.MessageCreatedPayload
		if err := json.Unmarshal(msg.Value, &payload); err != nil {
			return err
		}
		if payload.ChannelID == hot {
			mu.Lock()
			hotOrder = append(hotOrder, payload.Body)
			mu.Unlock()
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- fanout.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(hotOrder)
		mu.Unlock()
		if n == 20 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for all 20 hot-channel messages, got %d", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-runErr

	mu.Lock()
	defer mu.Unlock()
	for i, body := range hotOrder {
		want := fmt.Sprintf("hot-%02d", i)
		if body != want {
			t.Fatalf("expected hot channel messages processed in order; at position %d expected %q, got %q (full order: %v)", i, want, body, hotOrder)
		}
	}
}
