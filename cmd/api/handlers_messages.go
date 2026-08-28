package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/quota"
)

type messageRow messages.Message

type sendMessageRequest struct {
	ClientMessageID string `json:"client_message_id"`
	Body            string `json:"body"`
}

type messageResponse struct {
	MessageID       string                    `json:"message_id"`
	ChannelID       string                    `json:"channel_id"`
	Sequence        int64                     `json:"sequence"`
	SenderID        string                    `json:"sender_id"`
	ClientMessageID string                    `json:"client_message_id"`
	Body            string                    `json:"body"`
	CreatedAt       string                    `json:"created_at"`
	ReactionCounts  map[string]int            `json:"reaction_counts"`
	LatestReactions []reactionSummaryResponse `json:"latest_reactions"`
}

func messageResponseFrom(m messageRow) messageResponse {
	return messageResponse{
		MessageID:       m.MessageID.String(),
		ChannelID:       m.ChannelID.String(),
		Sequence:        m.Sequence,
		SenderID:        m.SenderID.String(),
		ClientMessageID: m.ClientMessageID.String(),
		Body:            m.Body,
		CreatedAt:       m.CreatedAt.Format(rfc3339Milli),
		ReactionCounts:  m.ReactionCounts,
		LatestReactions: toReactionSummaryResponse(m.LatestReactions),
	}
}

const maxMessageBodyLen = 4000

// handleSendMessage enforces the client-generated idempotency key
// (INSTRUCTIONS.md §19), the per-user rate limit, and — for a channel whose
// home region isn't this instance — forwards the write there rather than
// touching Postgres locally (§5/§27).
func (a *App) handleSendMessage(w http.ResponseWriter, r *http.Request) {
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

	isMember, err := a.membershipRepo.IsMember(r.Context(), channelID, identity.UserID)
	if err != nil {
		a.log.Error("check membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check membership")
		return
	}
	if !isMember {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	route, err := a.region.Resolve(r.Context(), channelID.String())
	if err != nil {
		if errors.Is(err, channels.ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel not found")
			return
		}
		a.log.Error("resolve channel route", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load channel")
		return
	}
	// Defense in depth (INSTRUCTIONS.md §43): membership should only ever
	// exist within one app by construction, but never rely on that
	// invariant implicitly when the row already carries app_id to check
	// explicitly.
	if route.AppID != identity.AppID {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, quota.CapabilityMessageSend, "rate:message:user:"+identity.UserID.String())
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(quota.CapabilityMessageSend).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return
	}

	if route.HomeRegion != a.cfg.Region {
		a.forwardToHomeRegion(w, r, route.HomeRegion, body)
		return
	}

	var req sendMessageRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}
	clientMessageID, err := uuid.Parse(req.ClientMessageID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid client_message_id")
		return
	}
	if req.Body == "" || len(req.Body) > maxMessageBodyLen {
		writeError(w, http.StatusBadRequest, "body is required (max 4000 chars)")
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	var msg messageRow
	err = a.metrics.TimePostgres("send_message", func() error {
		m, _, sendErr := a.messagesRepo.Send(r.Context(), pool, channelID, identity.UserID, clientMessageID, req.Body)
		msg = messageRow(m)
		return sendErr
	})
	if err != nil {
		a.log.Error("send message", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send message")
		return
	}
	a.metrics.MessagesSentTotal.WithLabelValues(a.cfg.Region).Inc()

	// Best-effort denormalization: the message is already durably committed
	// on its shard; a failure here only delays "last message" ordering in
	// GET /users/me/channels, never message durability.
	if members, err := a.membershipRepo.ListMembers(r.Context(), channelID); err == nil {
		_ = a.channelsRepo.UpdateLastMessage(r.Context(), channelID, members, msg.Sequence, msg.CreatedAt)
	}

	writeJSON(w, http.StatusCreated, messageResponseFrom(msg))
}

// handleListMessages backs GET /channels/{id}/messages?before=&limit= using
// cursor pagination only — never OFFSET (INSTRUCTIONS.md §11). Reads don't
// forward to the home region: message storage is reachable from any
// instance in this local topology (see docs/adr/0002 and
// docs/platform/multi-region.md for what a real geo-deployment adds here).
func (a *App) handleListMessages(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	isMember, err := a.membershipRepo.IsMember(r.Context(), channelID, identity.UserID)
	if err != nil {
		a.log.Error("check membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check membership")
		return
	}
	if !isMember {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	var rows []messageRow
	err = a.metrics.TimePostgres("list_messages", func() error {
		msgs, listErr := a.messagesRepo.ListBefore(r.Context(), pool, channelID, before, limit)
		rows = make([]messageRow, len(msgs))
		for i, m := range msgs {
			rows[i] = messageRow(m)
		}
		return listErr
	})
	if err != nil {
		a.log.Error("list messages", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}

	out := make([]messageResponse, len(rows))
	for i, m := range rows {
		out[i] = messageResponseFrom(m)
	}
	writeJSON(w, http.StatusOK, out)
}
