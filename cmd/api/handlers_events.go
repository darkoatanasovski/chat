package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/events"
)

type sendCustomEventRequest struct {
	// EventType is a caller-chosen string identifying what kind of custom
	// event this is (e.g. "reaction.custom", "game.move") — see
	// events.CustomEventPayload's doc comment. Required.
	EventType string `json:"event_type"`
	// Data is fully caller-defined and passed through untouched. Optional.
	Data json.RawMessage `json:"data,omitempty"`
}

type customEventResponse struct {
	EventID   string `json:"event_id"`
	ChannelID string `json:"channel_id"`
}

// handleSendCustomEvent backs POST /channels/{id}/events — the
// "custom_events" capability. Unlike a message, a custom event is never
// stored on the messages table or returned by GET .../messages: it only
// ever exists as one row in the transactional outbox
// (events.InsertOutboxWithID) long enough for cmd/worker's Publisher to
// ship it to Kafka, at which point cmd/gateway's Fanout relays it to every
// channel member's socket and the outbox row is deleted — the same
// publish-then-delete lifecycle every other realtime event in this
// codebase already has, just with no accompanying domain row (there's
// nothing else durable about a custom event; the outbox row itself IS the
// domain write here). Same membership/app-scope/region-forwarding shape as
// handleSendMessage, minus the idempotency-key/rate-limit machinery a
// message send needs — a custom event is fire-and-forget by nature, same
// as typing.
func (a *App) handleSendCustomEvent(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for custom event capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send event")
		return
	}
	if !app.ChannelCapabilities.CustomEvents {
		writeError(w, http.StatusForbidden, "custom events are not enabled for this app")
		return
	}

	var req sendCustomEventRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}
	if req.EventType == "" || len(req.EventType) > 128 {
		writeError(w, http.StatusBadRequest, "event_type is required (max 128 chars)")
		return
	}
	if len(req.Data) > maxRequestBody {
		writeError(w, http.StatusBadRequest, "data is too large")
		return
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		a.log.Error("generate custom event id", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send event")
		return
	}
	payload := events.CustomEventPayload{
		EventID:   eventID,
		ChannelID: channelID,
		SenderID:  identity.UserID,
		EventType: req.EventType,
		Data:      req.Data,
		CreatedAt: time.Now().UTC(),
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	// outbox_events lives on the channel's physical shard, same as every
	// other event this codebase publishes (events.InsertOutbox requires a
	// pgx.Tx; every other producer supplies one from whichever transaction
	// wraps its own domain write — a custom event has no other domain
	// write, so a tx exists here purely to host the outbox insert itself).
	tx, err := pool.Begin(r.Context())
	if err != nil {
		a.log.Error("begin tx for custom event", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send event")
		return
	}
	defer tx.Rollback(r.Context())

	if err := events.InsertOutboxWithID(r.Context(), tx, eventID, events.TopicCustomEvent, channelID, payload); err != nil {
		a.log.Error("insert custom event outbox row", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		a.log.Error("commit custom event outbox row", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send event")
		return
	}

	a.touchPresence(identity.UserID)
	writeJSON(w, http.StatusAccepted, customEventResponse{EventID: eventID.String(), ChannelID: channelID.String()})
}
