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
	Type      string    `json:"type"`
	ChannelID uuid.UUID `json:"channel_id"`
	MessageID uuid.UUID `json:"message_id"`
	Sequence  int64     `json:"sequence"`
	SenderID  uuid.UUID `json:"sender_id"`
	Body      string    `json:"body"`
	CreatedAt string    `json:"created_at"`
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

// ReadDeliveryFrame is the JSON payload pushed to WebSocket clients for
// read.updated — one user's fresh watermark; the client updates its local
// per-member read-state map and recomputes "seen by" for its own messages.
type ReadDeliveryFrame struct {
	Type             string    `json:"type"`
	ChannelID        uuid.UUID `json:"channel_id"`
	UserID           uuid.UUID `json:"user_id"`
	LastReadSequence int64     `json:"last_read_sequence"`
}

// Fanout consumes message.created, reaction.updated, and read.updated and
// delivers each to every member of the channel, wherever their connection
// lives. All gateway instances share one Kafka consumer group subscribed to
// every topic (INSTRUCTIONS.md §17), so a given event is handled by exactly one
// instance — not every instance in every region, the way V1 worked. That
// instance delivers directly to whichever members are connected to it
// locally (Hub) and routes everyone else through Registry + Publisher to
// whichever instance actually holds their connection. This is still the
// fan-out-on-write path appropriate for V1's conservative channel-member
// limits (§31); an extremely hot channel would eventually need dedicated
// fanout infrastructure without moving message storage (§30), which this
// consumer intentionally does not attempt to solve yet.
type Fanout struct {
	reader   *kafkago.Reader
	dedup    *Dedup
	delivery *Delivery
	metrics  *metrics.Metrics
	log      *slog.Logger
}

func NewFanout(reader *kafkago.Reader, delivery *Delivery, dedup *Dedup, m *metrics.Metrics, log *slog.Logger) *Fanout {
	return &Fanout{reader: reader, delivery: delivery, dedup: dedup, metrics: m, log: log}
}

// Run blocks, consuming until ctx is cancelled.
func (f *Fanout) Run(ctx context.Context) error {
	for {
		msg, err := f.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			f.log.Error("fanout: read message", "error", err)
			continue
		}
		if err := f.handle(ctx, msg); err != nil {
			f.log.Error("fanout: handle message", "error", err)
		}
	}
}

func (f *Fanout) handle(ctx context.Context, msg kafkago.Message) error {
	switch msg.Topic {
	case events.TopicReactionUpdated:
		return f.handleReactionUpdated(ctx, msg)
	case events.TopicReadUpdated:
		return f.handleReadUpdated(ctx, msg)
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
		Type:      "message.created",
		ChannelID: payload.ChannelID,
		MessageID: payload.MessageID,
		Sequence:  payload.Sequence,
		SenderID:  payload.SenderID,
		Body:      payload.Body,
		CreatedAt: payload.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	})
	if err != nil {
		return fmt.Errorf("fanout: marshal delivery frame: %w", err)
	}

	if err := f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, uuid.Nil); err != nil {
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

	return f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, uuid.Nil)
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

	return f.delivery.ToChannelMembers(ctx, payload.ChannelID, frame, uuid.Nil)
}
