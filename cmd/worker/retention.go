// Message retention: each app sets how long its messages live
// (apps.retention_days, migrations/config/0002 — default 730 days / 2 years;
// 0 means keep forever). Enforcing that is a background job, not something the
// send/read path ever checks, so it runs here in cmd/worker alongside the
// outbox publisher: one Sweeper per cell, walking every channel in the cell
// (docs/adr/0006-cell-based-tenant-routing.md).
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/apps"
	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
)

const (
	retentionSweepInterval = time.Hour
	retentionPageSize      = 500
	retentionDeleteBatch   = 1000
)

// Sweeper walks every channel in this cell and deletes any of its messages
// older than its owning organization's plan allows. There is no virtual-shard
// range: a cell holds exactly the channels of the apps pinned to it.
type Sweeper struct {
	log *slog.Logger
	m   *metrics.Metrics

	channelsRepo *channels.Repo
	appsRepo     *apps.Repo
	messagesRepo *messages.Repo
	cellPool     *pgxpool.Pool
}

func NewSweeper(log *slog.Logger, m *metrics.Metrics, channelsRepo *channels.Repo, appsRepo *apps.Repo, messagesRepo *messages.Repo, cellPool *pgxpool.Pool) *Sweeper {
	return &Sweeper{
		log: log, m: m,
		channelsRepo: channelsRepo, appsRepo: appsRepo, messagesRepo: messagesRepo, cellPool: cellPool,
	}
}

// Run sweeps immediately, then every interval, until ctx is cancelled. A
// failed sweep is logged and retried next tick — retention being briefly
// late is never worth taking the process down for.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	s.sweepOnce(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

func (s *Sweeper) sweepOnce(ctx context.Context) {
	after := uuid.Nil
	// Per-sweep memo so each app's retention setting is read once, not per channel.
	retentionByApp := map[int64]int{}
	for {
		page, err := s.channelsRepo.ListForRetention(ctx, after, retentionPageSize)
		if err != nil {
			s.log.Error("retention: list channels", "error", err)
			return
		}
		if len(page) == 0 {
			return
		}

		for _, ch := range page {
			days, ok := retentionByApp[ch.AppID]
			if !ok {
				days, err = s.appsRepo.RetentionDaysForApp(ctx, ch.AppID)
				if err != nil {
					s.log.Warn("retention: resolve app retention", "channel_id", ch.ChannelID, "app_id", ch.AppID, "error", err)
					continue
				}
				retentionByApp[ch.AppID] = days
			}
			if days <= 0 {
				continue // keep forever
			}
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			deleted, err := s.messagesRepo.DeleteExpiredBefore(ctx, s.cellPool, ch.ChannelID, cutoff, retentionDeleteBatch)
			if err != nil {
				s.log.Error("retention: delete expired", "channel_id", ch.ChannelID, "error", err)
				continue
			}
			if deleted > 0 {
				s.m.MessagesExpiredTotal.Add(float64(deleted))
				s.log.Info("retention: deleted expired messages", "channel_id", ch.ChannelID, "app_id", ch.AppID, "retention_days", days, "deleted", deleted)
			}
		}

		after = page[len(page)-1].ChannelID
		if len(page) < retentionPageSize {
			return
		}
	}
}

// startRetentionSweeper wires up and launches the Sweeper for this cell,
// running until ctx is cancelled. Channels and messages both live in the cell
// DB (cellPool); each app's retention_days is read from the global config DB
// (apps table), memoized per sweep.
func startRetentionSweeper(ctx context.Context, cfg config.Config, log *slog.Logger, m *metrics.Metrics, cellPool *pgxpool.Pool) error {
	configPool, err := pgstorage.Connect(ctx, cfg.ConfigDSN)
	if err != nil {
		return fmt.Errorf("connect config db: %w", err)
	}

	appsRepo := apps.NewRepo(configPool)
	channelsRepo := channels.NewRepo(cellPool)
	messagesRepo := messages.NewRepo()

	sweeper := NewSweeper(log, m, channelsRepo, appsRepo, messagesRepo, cellPool)
	go sweeper.Run(ctx, retentionSweepInterval)

	log.Info("retention sweeper started", "shard", cfg.ShardID, "interval", retentionSweepInterval)
	return nil
}
