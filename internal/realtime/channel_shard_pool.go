package realtime

import (
	"context"
	"hash/fnv"
	"sync"

	"github.com/google/uuid"
	kafkago "github.com/segmentio/kafka-go"
)

// shardJob is one unit of work submitted to a channelShardPool. done is
// buffered (capacity 1) so the worker never blocks handing back its result,
// regardless of when (or whether) the submitter is still waiting on it.
type shardJob struct {
	msg  kafkago.Message
	done chan<- error
}

// channelShardPool runs a fixed number of worker goroutines, each owning one
// shard's own FIFO job queue. Every job for a given routing key (channel_id)
// always lands on the same shard — shardIndex is a pure function of the
// key — so all of one channel's events are processed by the same single
// goroutine, strictly in submission order, with no coordination needed
// beyond that. Different channels land on different shards (by hash, so
// spread is independent of how channel IDs happen to be generated) and run
// fully concurrently. This is what lets one busy channel's slow delivery
// stop stalling every other channel a gateway instance is handling, without
// ever risking two events for the *same* channel being processed out of
// order relative to each other.
//
// Each shard's queue is buffered to queueSize. Fanout.Run sizes queueSize to
// its own max-in-flight count, so a submit can never block: in the worst
// case every in-flight message hashes to the same single shard, and that
// shard's buffer is exactly large enough to hold all of them without the
// caller waiting on a busy queue — the real backpressure point is the
// caller's own in-flight limit, not this pool.
type channelShardPool struct {
	queues []chan shardJob
	wg     sync.WaitGroup
}

func newChannelShardPool(ctx context.Context, numShards, queueSize int, process func(context.Context, kafkago.Message) error) *channelShardPool {
	p := &channelShardPool{queues: make([]chan shardJob, numShards)}
	for i := range p.queues {
		q := make(chan shardJob, queueSize)
		p.queues[i] = q
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for job := range q {
				job.done <- process(ctx, job.msg)
			}
		}()
	}
	return p
}

func (p *channelShardPool) submit(shard int, job shardJob) {
	p.queues[shard] <- job
}

// close signals every shard worker to exit once it has drained whatever is
// already queued, and waits for them to do so — in-flight jobs still run to
// completion (and still report their result on job.done) rather than being
// discarded, so a caller already waiting on one isn't left hanging.
func (p *channelShardPool) close() {
	for _, q := range p.queues {
		close(q)
	}
	p.wg.Wait()
}

// shardIndex deterministically maps a routing key to one of numShards
// shards. FNV-1a is used purely for its speed and even bit distribution
// over UUIDs, not for any cryptographic property — this only needs to keep
// one channel's traffic on one shard and spread different channels across
// the rest.
func shardIndex(key uuid.UUID, numShards int) int {
	h := fnv.New32a()
	h.Write(key[:])
	return int(h.Sum32() % uint32(numShards))
}
