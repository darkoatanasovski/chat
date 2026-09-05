// cmd/worker runs the transactional outbox publisher for one cell
// (INSTRUCTIONS.md §16; docs/adr/0006-cell-based-tenant-routing.md). It polls
// this cell's outbox_events table and publishes to this cell's Kafka,
// deleting each row only after a successful publish. It also runs the cell's
// background maintenance jobs (retention, reminders). Two+ worker replicas
// run per cell for availability.
package worker

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

func Run() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logging.New("worker-outbox", cfg.ShardID)
	m := metrics.New("chat_worker")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// This cell's own database (outbox + all tenant data). The retention and
	// reminder sweepers additionally open the global config DB for tier
	// resolution — see startRetentionSweeper.
	pool, err := pgstorage.Connect(ctx, cfg.CellDSN)
	if err != nil {
		log.Error("connect cell db", "shard", cfg.ShardID, "error", err)
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
		"cell": pool.Ping,
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
