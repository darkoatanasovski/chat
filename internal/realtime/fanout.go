package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/darkoatanasovski/chat/internal/events"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
)

// DeliveryFrame is the JSON payload pushed to WebSocket clients for
// message.created.
type DeliveryFrame struct {
	Type             string     `json:"type"`
	ChannelID        uuid.UUID  `json:"channel_id"`
	MessageID        uuid.UUID  `json:"message_id"`
	Sequence         int64      `json:"sequence"`
	SenderID         uuid.UUID  `json:"sender_id"`
	Body             string     `json:"body"`
	ParentID         *uuid.UUID `json:"parent_id,omitempty"`
	ParentReplyCount *int64     `json:"parent_reply_count,omitempty"`
	PollID           *uuid.UUID `json:"poll_id,omitempty"`
	CreatedAt        string     `json:"created_at"`
}

// ReactionDeliveryFrame is the JSON payload pushed to WebSocket clients for
// reaction.updated — carries the message's fresh denormalized state
// directly, same as ReactionUpdatedPayload, so the client patches its local
// copy of the message without ever re-fetching it.
type ReactionDeliveryFrame struct {
	Type            string                   `json:"type"`
	ChannelID       uuid.UUID                `json:"channel_id"`
	MessageID       uuid.UUID                `json:"message_id"`
	ActorID         uuid.UUID                `json:"actor_id"`
	Reaction        string                   `json:"reaction"`
	Action          string                   `json:"action"`
	ReactionCounts  map[string]int           `json:"reaction_counts"`
	LatestReactions []events.ReactionSummary `json:"latest_reactions"`
}

// MessageEditDeliveryFrame is the JSON payload pushed to WebSocket clients
// for message.edited — a message's fresh body/edited_at, same "carries the
// full current state" shape as ReactionDeliveryFrame, so the client patches
// its local copy without a follow-up GET.
type MessageEditDeliveryFrame struct {
	Type      string    `json:"type"`
	ChannelID uuid.UUID `json:"channel_id"`
	MessageID uuid.UUID `json:"message_id"`
	SenderID  uuid.UUID `json:"sender_id"`
	Body      string    `json:"body"`
	EditedAt  string    `json:"edited_at"`
}

// ReadDeliveryFrame is the JSON payload pushed to WebSocket clients for
// read.updated — one user's fresh watermark; the client updates its local
// per-member read-state map and recomputes "seen by" for its own messages.
type ReadDeliveryFrame struct {
	Type             string    `json:"type"`
	ChannelID        uuid.UUID `json:"channel_id"`
	UserID           uuid.UUID `json:"user_id"`
	LastReadSequence int64     `json:"last_read_sequence"`
}

// PollVoteDeliveryFrame is the JSON payload pushed to WebSocket clients for
// poll.vote_updated — a poll's fresh per-option tallies, same "carries the
// full current state" shape as ReactionDeliveryFrame, so the client patches
// its local copy of the poll without a follow-up GET.
type PollVoteDeliveryFrame struct {
	Type        string                  `json:"type"`
	ChannelID   uuid.UUID               `json:"channel_id"`
	PollID      uuid.UUID               `json:"poll_id"`
	ActorID     uuid.UUID               `json:"actor_id"`
	Options     []events.PollOptionTally `json:"options"`
	TotalVoters int                     `json:"total_voters"`
}

// MessagePinDeliveryFrame is the JSON payload pushed to WebSocket clients
// for message.pin_updated — a message's fresh pinned state, same "carries
// the full current state" shape as ReactionDeliveryFrame, so the client
// patches its local copy of the message without a follow-up GET.
type MessagePinDeliveryFrame struct {
	Type      string     `json:"type"`
	ChannelID uuid.UUID  `json:"channel_id"`
	MessageID uuid.UUID  `json:"message_id"`
	ActorID   uuid.UUID  `json:"actor_id"`
	Action    string     `json:"action"`
	PinnedAt  *time.Time `json:"pinned_at"`
	PinnedBy  *uuid.UUID `json:"pinned_by"`
}

// CustomEventDeliveryFrame is the JSON payload pushed to WebSocket clients
// for custom.event — see events.CustomEventPayload's doc comment for what
// EventType/Data mean; this is simply that payload's realtime-wire shape
// (Type replaces the Kafka topic name the way every other *DeliveryFrame
// does).
type CustomEventDeliveryFrame struct {
	Type      string          `json:"type"`
	ChannelID uuid.UUID       `json:"channel_id"`
	SenderID  uuid.UUID       `json:"sender_id"`
	EventType string          `json:"event_type"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// MessageSource is the minimal *kafkago.Reader surface Fanout.Run needs —
// narrow enough that tests can fake the read/commit loop without a live
// Kafka broker. *kafkago.Reader satisfies this directly.
type MessageSource interface {
	FetchMessage(ctx context.Context) (kafkago.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
}

// defaultFanoutShards is used when a Fanout's shard count is never set via
// SetShards — see channel_shard_pool.go for what a shard is and why message
// order per channel doesn't depend on it.
const defaultFanoutShards = 16

// Fanout consumes message.created, reaction.updated, and read.updated and
// delivers each to every member of the channel, wherever their connection
// lives. All gateway instances share one Kafka consumer group subscribed to
// every topic (INSTRUCTIONS.md §17), so a given event is handled by exactly one
// instance — not every instance in every region, the way V1 worked. That
// instance delivers directly to whichever members are connected to it
// locally (Hub) and routes everyone else through Registry + Publisher to
// whichever instance actually holds their connection.
//
// Run fans work for *different* channels out across a bounded pool of
// per-channel shards (channel_shard_pool.go) so one busy channel no longer
// serializes delivery for every other channel this instance happens to
// share a Kafka partition with — previously a single goroutine handled
// every channel's events one at a time, so a hot channel's slow delivery
// stalled unrelated channels behind it. A single channel's own events are
// still processed strictly one at a time, in Kafka's order, because
// channel_id always hashes to the same shard — an extremely hot single
// channel's own throughput ceiling is therefore unchanged by this; scaling
// that further would mean parallelizing delivery *within* one channel's
// member list, which is a separate, not-yet-needed optimization.
type Fanout struct {
	reader   MessageSource
	dedup    *Dedup
	delivery *Delivery
	metrics  *metrics.Metrics
	log      *slog.Logger
	shards   int

	// processFn stands in for handle in tests that need to control per-
	// message timing/ordering without a real Redis-backed Delivery. Left
	// nil in production, where Run always calls the real f.handle.
	processFn func(context.Context, kafkago.Message) error
}

func NewFanout(reader MessageSource, delivery *Delivery, dedup *Dedup, m *metrics.Metrics, log *slog.Logger) *Fanout {
	return &Fanout{reader: reader, delivery: delivery, dedup: dedup, metrics: m, log: log, shards: defaultFanoutShards}
}

// SetShards overrides the number of per-channel shards Run uses, e.g. from
// operator-facing config (FANOUT_SHARDS). Values <= 0 are ignored, leaving
// the default in place, so a missing/invalid env var can't accidentally
// collapse fanout back to fully serial.
func (f *Fanout) SetShards(n int) {
	if n > 0 {
		f.shards = n
	}
}

// Run blocks, consuming until ctx is cancelled. It fetches messages strictly
// in Kafka order (preserving per-partition read order and therefore
// correct offset semantics) but hands each one to channelShardPool for
// concurrent processing, then commits offsets back in that same fetch
// order once each message's processing has actually completed — never
// earlier. That ordering matters for correctness, not just tidiness:
// kafka-go's commit only tracks the highest offset it's told about per
// partition (see offsetStash.merge), so committing offset N+1 before N has
// finished would let a crash between those two commits skip redelivering
// message N forever, even though it was never actually processed.
//
// Fetching runs on its own goroutine, handed off to the main loop over an
// unbuffered channel, so a message can be committed the moment it finishes
// processing rather than only when numShards messages happen to be
// in flight — draining strictly at capacity would leave low-traffic
// channels' offsets uncommitted indefinitely between bursts.
func (f *Fanout) Run(ctx context.Context) error {
	numShards := max(f.shards, 1)

	process := f.processFn
	if process == nil {
		process = f.handle
	}
	pool := newChannelShardPool(ctx, numShards, numShards, process)
	defer pool.close()

	type fetchResult struct {
		msg kafkago.Message
		err error
	}
	fetched := make(chan fetchResult)
	go func() {
		for {
			msg, err := f.reader.FetchMessage(ctx)
			select {
			case fetched <- fetchResult{msg: msg, err: err}:
			case <-ctx.Done():
				return
			}
		}
	}()

	type inFlight struct {
		msg  kafkago.Message
		done chan error
	}
	var pending []inFlight

	// completeOldest takes an already-received result for pending[0] — never
	// re-reads its done channel, since a value can only be received once.
	completeOldest := func(err error) {
		oldest := pending[0]
		pending = pending[1:]
		if err != nil {
			f.log.Error("fanout: handle message", "error", err)
		}
		if err := f.reader.CommitMessages(ctx, oldest.msg); err != nil {
			f.log.Error("fanout: commit offset", "error", err)
		}
	}

	dispatch := func(msg kafkago.Message) {
		shard := 0
		if key, kerr := channelShardKey(msg.Value); kerr == nil {
			shard = shardIndex(key, numShards)
		}
		// A malformed payload (kerr != nil) falls back to shard 0 rather
		// than being dropped here — handle()'s own json.Unmarshal will
		// fail identically and report the real error either way.
		done := make(chan error, 1)
		pool.submit(shard, shardJob{msg: msg, done: done})
		pending = append(pending, inFlight{msg: msg, done: done})
	}

	for {
		// Only accept a new fetch while under the in-flight cap; a nil
		// channel in a select is never ready, so this simply disables that
		// case (and therefore fetching) once pending is full, applying
		// backpressure without a separate branch for it.
		var fetchIn <-chan fetchResult
		if len(pending) < numShards {
			fetchIn = fetched
		}

		var oldestDone <-chan error
		if len(pending) > 0 {
			oldestDone = pending[0].done
		}

		select {
		case <-ctx.Done():
			// Graceful shutdown: still drain and commit whatever was
			// already dispatched rather than discarding completed work.
			for len(pending) > 0 {
				completeOldest(<-pending[0].done)
			}
			return ctx.Err()

		case res := <-fetchIn:
			if res.err != nil {
				f.log.Error("fanout: fetch message", "error", res.err)
				continue
			}
			dispatch(res.msg)

		case err := <-oldestDone:
			completeOldest(err)
		}
	}
}

// channelShardKey extracts the channel_id every event payload this consumer
// handles carries under the same JSON field, without needing the
// topic-specific unmarshal handle() does — Run must pick a shard before it
// knows (or cares) which concrete payload type a message holds.
func channelShardKey(value []byte) (uuid.UUID, error) {
	var envelope struct {
		ChannelID uuid.UUID `json:"channel_id"`
	}
	if err := json.Unmarshal(value, &envelope); err != nil {
		return uuid.Nil, err
	}
	return envelope.ChannelID, nil
}

func (f *Fanout) handle(ctx context.Context, msg kafkago.Message) error {
	switch msg.Topic {
	case events.TopicReactionUpdated:
		return f.handleReactionUpdated(ctx, msg)
	case events.TopicReadUpdated:
		return f.handleReadUpdated(ctx, msg)
	case events.TopicPollVoteUpdated:
		return f.handlePollVoteUpdated(ctx, msg)
	case events.TopicMessageEdited:
		return f.handleMessageEdited(ctx, msg)
	case events.TopicMessagePinUpdated:
		return f.handleMessagePinUpdated(ctx, msg)
	case events.TopicCustomEvent:
		return f.handleCustomEvent(ctx, msg)
	case events.TopicMessageReminderDue:
		return f.handleMessageReminderDue(ctx, msg)
	case events.TopicUnreadReminderDue:
		return f.handleUnreadReminderDue(ctx, msg)
	default:
		// Empty/unrecognized Topic falls through to message.created too —
		// tests construct kafkago.Message without setting Topic, and this
		// was the only event type before reactions existed.
		return f.handleMessageCreated(ctx, msg)
	}
}

func (f *Fanout) handleMessageCreated(ctx context.Context, msg kafkago.Message) error {
	var payload events.MessageCreatedPayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	eventID := fmt.Sprintf("%s:%d", payload.ChannelID, payload.Sequence)
	seen, err := f.dedup.SeenBefore(ctx, eventID)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(DeliveryFrame{
		Type:             "message.created",
		ChannelID:        payload.ChannelID,
		MessageID:        payload.MessageID,
		Sequence:         payload.Sequence,
		SenderID:         payload.SenderID,
		Body:             payload.Body,
		ParentID:         payload.ParentID,
		ParentReplyCount: payload.ParentReplyCount,
		PollID:           payload.PollID,
		CreatedAt:        payload.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal delivery frame: %w", err)
	}

	if err := f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, payload.SenderID, uuid.Nil); err != nil {
		return err
	}

	if f.metrics != nil {
		f.metrics.MessageDeliveryLatency.Observe(time.Since(payload.CreatedAt).Seconds())
	}
	return nil
}

func (f *Fanout) handleReactionUpdated(ctx context.Context, msg kafkago.Message) error {
	var payload events.ReactionUpdatedPayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	seen, err := f.dedup.SeenBefore(ctx, payload.EventID.String())
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(ReactionDeliveryFrame{
		Type:            "reaction.updated",
		ChannelID:       payload.ChannelID,
		MessageID:       payload.MessageID,
		ActorID:         payload.ActorID,
		Reaction:        payload.Reaction,
		Action:          payload.Action,
		ReactionCounts:  payload.ReactionCounts,
		LatestReactions: payload.LatestReactions,
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal reaction delivery frame: %w", err)
	}

	return f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, payload.ActorID, uuid.Nil)
}

func (f *Fanout) handlePollVoteUpdated(ctx context.Context, msg kafkago.Message) error {
	var payload events.PollVoteUpdatedPayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	seen, err := f.dedup.SeenBefore(ctx, payload.EventID.String())
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(PollVoteDeliveryFrame{
		Type:        "poll.vote_updated",
		ChannelID:   payload.ChannelID,
		PollID:      payload.PollID,
		ActorID:     payload.ActorID,
		Options:     payload.Options,
		TotalVoters: payload.TotalVoters,
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal poll vote delivery frame: %w", err)
	}

	return f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, payload.ActorID, uuid.Nil)
}

func (f *Fanout) handleMessageEdited(ctx context.Context, msg kafkago.Message) error {
	var payload events.MessageEditedPayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	seen, err := f.dedup.SeenBefore(ctx, payload.EventID.String())
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(MessageEditDeliveryFrame{
		Type:      "message.edited",
		ChannelID: payload.ChannelID,
		MessageID: payload.MessageID,
		SenderID:  payload.SenderID,
		Body:      payload.Body,
		EditedAt:  payload.EditedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal message edit delivery frame: %w", err)
	}

	return f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, payload.SenderID, uuid.Nil)
}

func (f *Fanout) handleMessagePinUpdated(ctx context.Context, msg kafkago.Message) error {
	var payload events.MessagePinUpdatedPayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	seen, err := f.dedup.SeenBefore(ctx, payload.EventID.String())
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(MessagePinDeliveryFrame{
		Type:      "message.pin_updated",
		ChannelID: payload.ChannelID,
		MessageID: payload.MessageID,
		ActorID:   payload.ActorID,
		Action:    payload.Action,
		PinnedAt:  payload.PinnedAt,
		PinnedBy:  payload.PinnedBy,
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal message pin delivery frame: %w", err)
	}

	return f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, payload.ActorID, uuid.Nil)
}

func (f *Fanout) handleReadUpdated(ctx context.Context, msg kafkago.Message) error {
	var payload events.ReadUpdatedPayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	seen, err := f.dedup.SeenBefore(ctx, payload.EventID.String())
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(ReadDeliveryFrame{
		Type:             "read.updated",
		ChannelID:        payload.ChannelID,
		UserID:           payload.UserID,
		LastReadSequence: payload.LastReadSequence,
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal read delivery frame: %w", err)
	}

	return f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, payload.UserID, uuid.Nil)
}

// handleCustomEvent relays a client-published custom event to every other
// member of its channel — the "custom_events" capability. cmd/api's
// handleSendCustomEvent has already verified the capability was on and the
// sender was a member before writing the outbox row this consumes, so
// there's nothing further to authorize here; this is pure delivery, same
// division of responsibility as every other *Updated event.
func (f *Fanout) handleCustomEvent(ctx context.Context, msg kafkago.Message) error {
	var payload events.CustomEventPayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	seen, err := f.dedup.SeenBefore(ctx, payload.EventID.String())
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(CustomEventDeliveryFrame{
		Type:      "custom.event",
		ChannelID: payload.ChannelID,
		SenderID:  payload.SenderID,
		EventType: payload.EventType,
		Data:      payload.Data,
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal custom event delivery frame: %w", err)
	}

	// Unlike message.created, the sender is NOT excluded here — a custom
	// event has no independent "sender already has this from the HTTP
	// response" precedent, so it's delivered back to every member
	// including whoever sent it, matching how a client that fires a custom
	// event usually wants its own other connections/devices to see it too.
	return f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, payload.SenderID, uuid.Nil)
}

// MessageReminderDueFrame is the JSON payload pushed to WebSocket clients
// for message_reminder.due — see events.MessageReminderDuePayload's doc
// comment; delivered to exactly one recipient via Delivery.ToUser, not
// broadcast to the channel.
type MessageReminderDueFrame struct {
	Type       string    `json:"type"`
	ReminderID uuid.UUID `json:"reminder_id"`
	ChannelID  uuid.UUID `json:"channel_id"`
	MessageID  uuid.UUID `json:"message_id"`
}

// handleMessageReminderDue relays one due reminder to the single user who
// set it — internal/reminders.Repo.DeliverDue (cmd/worker) has already
// atomically marked it delivered and written this outbox row in the same
// transaction, so this is pure best-effort delivery: if the user isn't
// connected right now, the reminder is still recorded as delivered and
// simply never reaches a live socket (this codebase has no push-
// notification fallback — see INSTRUCTIONS.md §18 on WebSocket delivery
// being best-effort throughout).
func (f *Fanout) handleMessageReminderDue(ctx context.Context, msg kafkago.Message) error {
	var payload events.MessageReminderDuePayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	seen, err := f.dedup.SeenBefore(ctx, payload.ReminderID.String())
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(MessageReminderDueFrame{
		Type:       "message_reminder.due",
		ReminderID: payload.ReminderID,
		ChannelID:  payload.ChannelID,
		MessageID:  payload.MessageID,
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal message reminder delivery frame: %w", err)
	}
	return f.delivery.ToUser(ctx, payload.UserID, frame)
}

// UnreadReminderDueFrame is the JSON payload pushed to WebSocket clients for
// unread_reminder.due — single-recipient, same as MessageReminderDueFrame.
type UnreadReminderDueFrame struct {
	Type             string    `json:"type"`
	ChannelID        uuid.UUID `json:"channel_id"`
	LastReadSequence int64     `json:"last_read_sequence"`
	LatestSequence   int64     `json:"latest_sequence"`
}

// handleUnreadReminderDue relays one unread-channel notice to the single
// member it concerns — cmd/worker's unread sweep has already decided this
// member is both behind and due for another nudge (respecting its own
// minimum-gap cooldown) before writing this outbox row, so, like
// handleMessageReminderDue, this is pure best-effort delivery with nothing
// further to authorize.
func (f *Fanout) handleUnreadReminderDue(ctx context.Context, msg kafkago.Message) error {
	var payload events.UnreadReminderDuePayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		return fmt.Errorf("fanout: unmarshal payload: %w", err)
	}

	// Deduped by (channel, user, last_read_sequence): the same stale
	// watermark should only ever produce one nudge, even if the sweep
	// somehow ran twice before the outbox row was picked up — but a
	// genuinely new nudge (the member read further, then fell behind
	// again) has a different LastReadSequence and is correctly treated as
	// a fresh event.
	dedupKey := fmt.Sprintf("%s:%s:%d", payload.ChannelID, payload.UserID, payload.LastReadSequence)
	seen, err := f.dedup.SeenBefore(ctx, dedupKey)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}

	frame, err := json.Marshal(UnreadReminderDueFrame{
		Type:             "unread_reminder.due",
		ChannelID:        payload.ChannelID,
		LastReadSequence: payload.LastReadSequence,
		LatestSequence:   payload.LatestSequence,
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal unread reminder delivery frame: %w", err)
	}
	return f.delivery.ToUser(ctx, payload.UserID, frame)
}
