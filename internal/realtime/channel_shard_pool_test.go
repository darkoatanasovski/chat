package realtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	kafkago "github.com/segmentio/kafka-go"
)

func TestChannelShardPool_SameShardProcessesInSubmissionOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var order []int

	pool := newChannelShardPool(ctx, 4, 16, func(_ context.Context, msg kafkago.Message) error {
		mu.Lock()
		order = append(order, int(msg.Value[0]))
		mu.Unlock()
		return nil
	})
	defer pool.close()

	const n = 50
	dones := make([]chan error, n)
	for i := range n {
		dones[i] = make(chan error, 1)
		// Every job targets shard 0 — a single worker goroutine — so
		// submission order must equal processing order with no coordination
		// beyond the shard's own FIFO queue.
		pool.submit(0, shardJob{msg: kafkago.Message{Value: []byte{byte(i)}}, done: dones[i]})
	}
	for i := range n {
		if err := <-dones[i]; err != nil {
			t.Fatalf("job %d: unexpected error: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != n {
		t.Fatalf("expected %d jobs processed, got %d", n, len(order))
	}
	for i, v := range order {
		if v != i {
			t.Fatalf("expected shard 0 to process jobs in submission order; at position %d expected %d, got %d (full order: %v)", i, i, v, order)
		}
	}
}

func TestChannelShardPool_DifferentShardsRunConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slowStarted := make(chan struct{})
	release := make(chan struct{})

	pool := newChannelShardPool(ctx, 2, 4, func(_ context.Context, msg kafkago.Message) error {
		if msg.Topic == "slow" {
			close(slowStarted)
			<-release
		}
		return nil
	})
	defer pool.close()

	slowDone := make(chan error, 1)
	pool.submit(0, shardJob{msg: kafkago.Message{Topic: "slow"}, done: slowDone})

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow job on shard 0 never started")
	}

	// While shard 0's worker is still blocked inside the slow job, shard 1
	// must be free to process its own job immediately — if the pool were
	// accidentally serialized (e.g. a shared lock across shards), this
	// would hang until the slow job is released.
	fastDone := make(chan error, 1)
	pool.submit(1, shardJob{msg: kafkago.Message{Topic: "fast"}, done: fastDone})

	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("fast job: unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fast job on shard 1 did not complete while shard 0 was still busy — shards are not running concurrently")
	}

	close(release)
	select {
	case <-slowDone:
	case <-time.After(2 * time.Second):
		t.Fatal("slow job never completed after being released")
	}
}

func TestChannelShardPool_Close_DrainsQueuedJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var processed int32
	var mu sync.Mutex

	pool := newChannelShardPool(ctx, 3, 8, func(_ context.Context, _ kafkago.Message) error {
		mu.Lock()
		processed++
		mu.Unlock()
		return nil
	})

	const n = 12
	dones := make([]chan error, n)
	for i := range n {
		dones[i] = make(chan error, 1)
		pool.submit(i%3, shardJob{msg: kafkago.Message{}, done: dones[i]})
	}

	// close() must not return until every already-queued job — including
	// ones still sitting in a shard's buffer, not yet picked up — has run.
	pool.close()

	mu.Lock()
	defer mu.Unlock()
	if processed != n {
		t.Fatalf("expected all %d queued jobs to be drained before close() returned, got %d", n, processed)
	}
	for i, d := range dones {
		select {
		case <-d:
		default:
			t.Fatalf("job %d's done channel never received a result", i)
		}
	}
}

func TestShardIndex_DeterministicForSameKey(t *testing.T) {
	key := uuid.New()
	first := shardIndex(key, 16)
	for range 100 {
		if got := shardIndex(key, 16); got != first {
			t.Fatalf("shardIndex(%s, 16) returned %d then %d — must be a pure function of its inputs", key, first, got)
		}
	}
}

func TestShardIndex_InRange(t *testing.T) {
	for range 500 {
		idx := shardIndex(uuid.New(), 7)
		if idx < 0 || idx >= 7 {
			t.Fatalf("shardIndex returned out-of-range index %d for numShards=7", idx)
		}
	}
}

func TestShardIndex_DistributesAcrossShards(t *testing.T) {
	const numShards = 8
	seen := make(map[int]int)
	for range 400 {
		seen[shardIndex(uuid.New(), numShards)]++
	}
	for s := range numShards {
		if seen[s] == 0 {
			t.Fatalf("shard %d received no jobs out of 400 random keys across %d shards — hash distribution looks broken: %v", s, numShards, seen)
		}
	}
}
