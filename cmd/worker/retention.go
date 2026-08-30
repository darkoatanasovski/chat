// Message retention: each organization's tier caps how long its messages
// live (quota.TierLimits.RetentionDays, configured in deploy/tiers.yaml —
// e.g. FREE keeps a week, ENTERPRISE keeps forever). Enforcing that is a
// background job, not something the send/read path ever checks, so it runs
// here in cmd/worker alongside the outbox publisher: one Sweeper instance
// per physical shard, touching only the messages that already live on its
// own shard pool.
package main

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
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/routing"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
)

const (
	retentionSweepInterval = time.Hour
	retentionPageSize      = 500
	retentionDeleteBatch   = 1000
)

// Sweeper walks every channel whose virtual_shard falls in [vsMin, vsMax]
// (the range this physical shard owns per shards.yaml) and deletes any of
// its messages older than its owning organization's plan allows.
type Sweeper struct {
	log *slog.Logger
	m   *metrics.Metrics

	channelsRepo *channels.Repo
	appTiers     *apps.TierResolver
	messagesRepo *messages.Repo
	shardPool    *pgxpool.Pool

	tiers map[string]quota.TierLimits
	vsMin int
	vsMax int
}

func NewSweeper(log *slog.Logger, m *metrics.Metrics, channelsRepo *channels.Repo, appTiers *apps.TierResolver, messagesRepo *messages.Repo, shardPool *pgxpool.Pool, tiers map[string]quota.TierLimits, vsMin, vsMax int) *Sweeper {
	return &Sweeper{
		log: log, m: m,
		channelsRepo: channelsRepo, appTiers: appTiers, messagesRepo: messagesRepo, shardPool: shardPool,
		tiers: tiers, vsMin: vsMin, vsMax: vsMax,
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
	for {
		page, err := s.channelsRepo.ListByVirtualShardRange(ctx, s.vsMin, s.vsMax, after, retentionPageSize)
		if err != nil {
			s.log.Error("retention: list channels", "error", err)
			return
		}
		if len(page) == 0 {
			return
		}

		for _, ch := range page {
			tier, err := s.appTiers.TierForApp(ctx, ch.AppID)
			if err != nil {
				s.log.Warn("retention: resolve tier", "channel_id", ch.ChannelID, "app_id", ch.AppID, "error", err)
				continue
			}
			limits, ok := s.tiers[tier]
			if !ok || limits.RetentionDays <= 0 {
				continue
			}
			cutoff := time.Now().UTC().AddDate(0, 0, -limits.RetentionDays)
			deleted, err := s.messagesRepo.DeleteExpiredBefore(ctx, s.shardPool, ch.ChannelID, cutoff, retentionDeleteBatch)
			if err != nil {
				s.log.Error("retention: delete expired", "channel_id", ch.ChannelID, "error", err)
				continue
			}
			if deleted > 0 {
				s.m.MessagesExpiredTotal.Add(float64(deleted))
				s.log.Info("retention: deleted expired messages", "channel_id", ch.ChannelID, "tier", tier, "deleted", deleted)
			}
		}

		after = page[len(page)-1].ChannelID
		if len(page) < retentionPageSize {
			return
		}
	}
}

// startRetentionSweeper wires up and launches the Sweeper for this
// instance's own shard, running until ctx is cancelled. It opens its own
// connection to the control-plane DB (organizations/apps/channels — none
// of which live on the shard cmd/worker otherwise only ever talks to) and
// resolves tier via apps.TierResolver with no Redis client (nil is
// explicitly supported — see its doc comment): a once-an-hour background
// sweep has no need for that cache's speed, only Postgres as the source of
// truth.
func startRetentionSweeper(ctx context.Context, cfg config.Config, log *slog.Logger, m *metrics.Metrics, shardPool *pgxpool.Pool) error {
	shardsCfg, err := routing.LoadShardsConfig(cfg.ShardsConfigPath)
	if err != nil {
		return fmt.Errorf("load shards config: %w", err)
	}
	var vsMin, vsMax int
	found := false
	for _, ps := range shardsCfg.PhysicalShards {
		if ps.ID == cfg.ShardID {
			vsMin, vsMax = ps.VirtualShardRange[0], ps.VirtualShardRange[1]
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("shard id %q has no virtual_shard_range in shards config", cfg.ShardID)
	}

	tiers, err := quota.LoadTiers(cfg.TiersConfigPath)
	if err != nil {
		return fmt.Errorf("load tiers config: %w", err)
	}

	controlPool, err := pgstorage.Connect(ctx, cfg.ControlDSN)
	if err != nil {
		return fmt.Errorf("connect control db: %w", err)
	}

	appsRepo := apps.NewRepo(controlPool)
	appTiers := apps.NewTierResolver(nil, appsRepo.TierSource)
	channelsRepo := channels.NewRepo(controlPool)
	messagesRepo := messages.NewRepo()

	sweeper := NewSweeper(log, m, channelsRepo, appTiers, messagesRepo, shardPool, tiers, vsMin, vsMax)
	go sweeper.Run(ctx, retentionSweepInterval)

	log.Info("retention sweeper started", "shard", cfg.ShardID, "virtual_shard_range", [2]int{vsMin, vsMax}, "interval", retentionSweepInterval)
	return nil
}
