// Message reminders (the "message_reminders" capability) and unread
// reminders (the "unread_reminders" capability) both deliver a realtime
// notice to exactly one user, later, from a background poll loop — never
// synchronously on a request path, the same separation retention.go already
// established for a different concern. They're kept in one file because
// both are genuinely small compared to retention's per-shard channel walk,
// not because they share any state.
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
	"github.com/darkoatanasovski/chat/internal/events"
	"github.com/darkoatanasovski/chat/internal/membership"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	"github.com/darkoatanasovski/chat/internal/readstate"
	"github.com/darkoatanasovski/chat/internal/reminders"
	"github.com/darkoatanasovski/chat/internal/routing"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
)

const (
	messageReminderPollInterval = 30 * time.Second
	messageReminderBatchSize    = 100
)

// startMessageReminderPoller polls this instance's own shard pool directly
// — message_reminders is a shard table with no control-plane dependency,
// unlike the unread sweep below, so there's no per-shard-range channel walk
// needed here: internal/reminders.Repo.ListDue already scopes itself to
// whatever shard the given pool is.
func startMessageReminderPoller(ctx context.Context, log *slog.Logger, shardPool *pgxpool.Pool) {
	repo := reminders.NewRepo()
	go func() {
		ticker := time.NewTicker(messageReminderPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				delivered, err := repo.DeliverDue(ctx, shardPool, messageReminderBatchSize)
				if err != nil {
					log.Error("message reminder poll", "error", err)
					continue
				}
				if delivered > 0 {
					log.Info("message reminders delivered", "count", delivered)
				}
			}
		}
	}()
}

const (
	unreadSweepInterval  = 15 * time.Minute
	unreadReminderMinGap = time.Hour
	unreadPageSize       = 500
)

// UnreadSweeper walks every channel this physical shard owns (same
// [vsMin, vsMax] scoping as Sweeper) looking for members who are both
// behind (their read watermark trails the channel's latest message) and
// due for another nudge (never reminded, or last reminded more than
// unreadReminderMinGap ago) in an app that has unread_reminders on.
type UnreadSweeper struct {
	log *slog.Logger
	m   *metrics.Metrics

	channelsRepo   *channels.Repo
	membershipRepo *membership.Repo
	appsRepo       *apps.Repo
	messagesRepo   *messages.Repo
	readStateRepo  *readstate.Repo
	shardPool      *pgxpool.Pool

	vsMin, vsMax int
}

func (s *UnreadSweeper) Run(ctx context.Context, interval time.Duration) {
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

func (s *UnreadSweeper) sweepOnce(ctx context.Context) {
	after := uuid.Nil
	for {
		page, err := s.channelsRepo.ListByVirtualShardRange(ctx, s.vsMin, s.vsMax, after, unreadPageSize)
		if err != nil {
			s.log.Error("unread reminders: list channels", "error", err)
			return
		}
		if len(page) == 0 {
			return
		}

		for _, ch := range page {
			s.sweepChannel(ctx, ch.ChannelID, ch.AppID)
		}

		after = page[len(page)-1].ChannelID
		if len(page) < unreadPageSize {
			return
		}
	}
}

func (s *UnreadSweeper) sweepChannel(ctx context.Context, channelID uuid.UUID, appID int64) {
	caps, err := s.appsRepo.ChannelCapabilities(ctx, appID)
	if err != nil {
		s.log.Warn("unread reminders: resolve capabilities", "channel_id", channelID, "app_id", appID, "error", err)
		return
	}
	if !caps.UnreadReminders {
		return
	}

	latest, err := s.messagesRepo.SumSequencesByChannels(ctx, s.shardPool, []uuid.UUID{channelID})
	if err != nil {
		s.log.Warn("unread reminders: resolve latest sequence", "channel_id", channelID, "error", err)
		return
	}
	latestSeq := latest[channelID]
	if latestSeq == 0 {
		// No messages sent yet — nothing to be behind on.
		return
	}

	cutoff := time.Now().UTC().Add(-unreadReminderMinGap)
	candidates, err := s.membershipRepo.UnreadReminderCandidates(ctx, channelID, cutoff)
	if err != nil {
		s.log.Warn("unread reminders: resolve candidates", "channel_id", channelID, "error", err)
		return
	}
	if len(candidates) == 0 {
		return
	}

	states, err := s.readStateRepo.ListState(ctx, s.shardPool, channelID)
	if err != nil {
		s.log.Warn("unread reminders: resolve read state", "channel_id", channelID, "error", err)
		return
	}
	lastRead := make(map[uuid.UUID]int64, len(states))
	for _, st := range states {
		lastRead[st.UserID] = st.LastReadSequence
	}

	for _, userID := range candidates {
		// Absent from lastRead means this member has never marked anything
		// read in this channel — watermark 0, behind by construction since
		// latestSeq > 0 was already checked above.
		if lastRead[userID] >= latestSeq {
			continue
		}
		if err := s.notify(ctx, channelID, userID, lastRead[userID], latestSeq); err != nil {
			s.log.Warn("unread reminders: notify", "channel_id", channelID, "user_id", userID, "error", err)
			continue
		}
		if s.m != nil {
			s.m.UnreadRemindersSentTotal.Inc()
		}
	}
}

// notify writes the shard-side outbox row and stamps the control-plane
// cooldown marker. These are two different physical databases (outbox_events
// lives per-shard, channel_members lives on the control plane — see
// internal/membership's package doc comment), so true cross-database
// atomicity isn't available here the way every same-database event in this
// codebase gets it. Best-effort ordering: the outbox row goes first: if
// marking the cooldown afterward fails, the worst case is this member gets
// nudged again slightly sooner than unreadReminderMinGap intends next
// cycle, not silence — always fail toward "notify," never toward "go
// quiet."
func (s *UnreadSweeper) notify(ctx context.Context, channelID, userID uuid.UUID, lastReadSequence, latestSequence int64) error {
	tx, err := s.shardPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("unread reminders: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	payload := events.UnreadReminderDuePayload{
		ChannelID:        channelID,
		UserID:           userID,
		LastReadSequence: lastReadSequence,
		LatestSequence:   latestSequence,
	}
	if err := events.InsertOutbox(ctx, tx, events.TopicUnreadReminderDue, channelID, payload); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("unread reminders: commit: %w", err)
	}

	if err := s.membershipRepo.MarkUnreadReminderSent(ctx, channelID, userID); err != nil {
		return fmt.Errorf("unread reminders: mark sent: %w", err)
	}
	return nil
}

// startUnreadReminderSweeper wires up and launches UnreadSweeper for this
// instance's own shard — same virtual-shard-range resolution and control-
// plane connection pattern as startRetentionSweeper, just a second,
// independent background job sharing that same connection.
func startUnreadReminderSweeper(ctx context.Context, cfg config.Config, log *slog.Logger, m *metrics.Metrics, shardPool *pgxpool.Pool) error {
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

	controlPool, err := pgstorage.Connect(ctx, cfg.ControlDSN)
	if err != nil {
		return fmt.Errorf("connect control db: %w", err)
	}

	sweeper := &UnreadSweeper{
		log:            log,
		m:              m,
		channelsRepo:   channels.NewRepo(controlPool),
		membershipRepo: membership.NewRepo(controlPool),
		appsRepo:       apps.NewRepo(controlPool),
		messagesRepo:   messages.NewRepo(),
		readStateRepo:  readstate.NewRepo(),
		shardPool:      shardPool,
		vsMin:          vsMin,
		vsMax:          vsMax,
	}
	go sweeper.Run(ctx, unreadSweepInterval)

	log.Info("unread reminder sweeper started", "shard", cfg.ShardID, "virtual_shard_range", [2]int{vsMin, vsMax}, "interval", unreadSweepInterval)
	return nil
}
