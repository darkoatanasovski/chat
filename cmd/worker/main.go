// cmd/worker runs the transactional outbox publisher for exactly one
// physical shard (INSTRUCTIONS.md §16). One instance per shard — see
// deploy/docker-compose.yml's worker-outbox-a / worker-outbox-b — polls that
// shard's outbox_events table and publishes to Kafka, deleting each row only
// after a successful publish.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/darkoatanasovski/chat/internal/events"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/debug"
	"github.com/darkoatanasovski/chat/internal/platform/health"
	"github.com/darkoatanasovski/chat/internal/platform/logging"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	kafkastorage "github.com/darkoatanasovski/chat/internal/storage/kafka"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
)

const (
	pollInterval = 250 * time.Millisecond
	batchSize    = 100
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logging.New("worker-outbox", cfg.ShardID)
	m := metrics.New("chat_worker")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgstorage.Connect(ctx, cfg.ShardDSN)
	if err != nil {
		log.Error("connect shard db", "shard", cfg.ShardID, "error", err)
		os.Exit(1)
	}

	writer := kafkastorage.NewProducer(cfg.KafkaBrokers)
	defer writer.Close()

	publisher := events.NewPublisher(pool, writer, m)

	if err := startRetentionSweeper(ctx, cfg, log, m, pool); err != nil {
		// Retention is maintenance, not correctness-critical to any request
		// path — log and keep the outbox publisher running rather than
		// taking the whole instance down over it.
		log.Error("retention sweeper not started", "error", err)
	}

	// message_reminders and unread_reminders (the "message_reminders" and
	// "unread_reminders" capabilities) are both background jobs of the
	// same non-critical class as retention — a failure to start either one
	// logs and lets the outbox publisher keep running rather than taking
	// the whole worker instance down.
	startMessageReminderPoller(ctx, log, pool)
	if err := startUnreadReminderSweeper(ctx, cfg, log, m, pool); err != nil {
		log.Error("unread reminder sweeper not started", "error", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/healthz", health.Handler(map[string]health.Checker{
		"shard": pool.Ping,
	}))
	debug.Mount(mux)
	go func() {
		log.Info("metrics listening", "addr", cfg.MetricsAddr)
		if err := http.ListenAndServe(cfg.MetricsAddr, mux); err != nil {
			log.Error("metrics server", "error", err)
		}
	}()

	log.Info("outbox publisher started", "shard", cfg.ShardID, "poll_interval", pollInterval)
	if err := publisher.Run(ctx, pollInterval, batchSize); err != nil && ctx.Err() == nil {
		log.Error("publisher stopped", "error", err)
		os.Exit(1)
	}
}
