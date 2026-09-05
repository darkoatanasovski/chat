// Message reminders (the "message_reminders" capability) and unread
// reminders (the "unread_reminders" capability) both deliver a realtime
// notice to exactly one user, later, from a background poll loop — never
// synchronously on a request path, the same separation retention.go already
// established for a different concern. They're kept in one file because
// both are genuinely small compared to retention's per-shard channel walk,
// not because they share any state.
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
	"github.com/darkoatanasovski/chat/internal/events"
	"github.com/darkoatanasovski/chat/internal/membership"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	"github.com/darkoatanasovski/chat/internal/readstate"
	"github.com/darkoatanasovski/chat/internal/reminders"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
)

const (
	messageReminderPollInterval = 30 * time.Second
	messageReminderBatchSize    = 100
)

// startMessageReminderPoller polls this cell's own database directly —
// message_reminders is a cell table, so internal/reminders.Repo.ListDue
// already scopes itself to the given pool (this cell).
func startMessageReminderPoller(ctx context.Context, log *slog.Logger, cellPool *pgxpool.Pool) {
	repo := reminders.NewRepo()
	go func() {
		ticker := time.NewTicker(messageReminderPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				delivered, err := repo.DeliverDue(ctx, cellPool, messageReminderBatchSize)
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

// UnreadSweeper walks every channel in this cell looking for members who are
// both behind (their read watermark trails the channel's latest message) and
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
	cellPool       *pgxpool.Pool
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
		page, err := s.channelsRepo.ListForRetention(ctx, after, unreadPageSize)
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

	latest, err := s.messagesRepo.SumSequencesByChannels(ctx, s.cellPool, []uuid.UUID{channelID})
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

	states, err := s.readStateRepo.ListState(ctx, s.cellPool, channelID)
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

// notify writes the outbox row and stamps the cooldown marker. Both
// outbox_events and channel_members now live in the same cell database, so
// this could be one transaction; it stays two steps (outbox first, mark
// after) so that if the mark fails the member is nudged again slightly sooner
// than intended next cycle rather than going silent — always fail toward
// "notify."
func (s *UnreadSweeper) notify(ctx context.Context, channelID, userID uuid.UUID, lastReadSequence, latestSequence int64) error {
	tx, err := s.cellPool.Begin(ctx)
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
// cell. Channels, membership, messages and read-state all live in the cell
// DB (cellPool); apps/capabilities come from the global config DB.
func startUnreadReminderSweeper(ctx context.Context, cfg config.Config, log *slog.Logger, m *metrics.Metrics, cellPool *pgxpool.Pool) error {
	configPool, err := pgstorage.Connect(ctx, cfg.ConfigDSN)
	if err != nil {
		return fmt.Errorf("connect config db: %w", err)
	}

	sweeper := &UnreadSweeper{
		log:            log,
		m:              m,
		channelsRepo:   channels.NewRepo(cellPool),
		membershipRepo: membership.NewRepo(cellPool),
		appsRepo:       apps.NewRepo(configPool),
		messagesRepo:   messages.NewRepo(),
		readStateRepo:  readstate.NewRepo(),
		cellPool:       cellPool,
	}
	go sweeper.Run(ctx, unreadSweepInterval)

	log.Info("unread reminder sweeper started", "shard", cfg.ShardID, "interval", unreadSweepInterval)
	return nil
}
