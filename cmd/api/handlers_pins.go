package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/quota"
)

const defaultPinnedListLimit = 100

// handlePinMessage backs POST /channels/{id}/messages/{message_id}/pin.
// Any channel member may pin — this codebase has no channel-owner role
// distinct from member for end-user actions (see
// internal/messages.Repo.Pin's doc comment) — and, like reactions, pinning
// takes no request body: there's nothing to configure about a pin beyond
// which message it's on.
func (a *App) handlePinMessage(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	messageID, err := uuid.Parse(r.PathValue("message_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	if !a.checkPinRateLimit(w, r, identity) {
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	var msg messageRow
	err = a.metrics.TimePostgres("pin_message", func() error {
		m, _, pinErr := a.messagesRepo.Pin(r.Context(), pool, channelID, messageID, identity.UserID)
		msg = messageRow(m)
		return pinErr
	})
	if err != nil {
		if errors.Is(err, messages.ErrNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		a.log.Error("pin message", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to pin message")
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusOK, messageResponseFrom(msg))
}

// handleUnpinMessage backs DELETE /channels/{id}/messages/{message_id}/pin.
// Like unblocking, this has no per-pinner ownership restriction — any
// channel member may unpin regardless of who pinned it, matching Unpin's
// doc comment.
func (a *App) handleUnpinMessage(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	messageID, err := uuid.Parse(r.PathValue("message_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	if !a.checkPinRateLimit(w, r, identity) {
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	var msg messageRow
	err = a.metrics.TimePostgres("unpin_message", func() error {
		m, _, unpinErr := a.messagesRepo.Unpin(r.Context(), pool, channelID, messageID, identity.UserID)
		msg = messageRow(m)
		return unpinErr
	})
	if err != nil {
		if errors.Is(err, messages.ErrNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		a.log.Error("unpin message", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to unpin message")
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusOK, messageResponseFrom(msg))
}

// handleListPinnedMessages backs GET /channels/{id}/pinned-messages?limit=.
// A read, so — like handleListMessages — it never forwards to the home
// region (message storage is reachable from any instance in this local
// topology) and needs no rate limit of its own.
func (a *App) handleListPinnedMessages(w http.ResponseWriter, r *http.Request) {
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

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = defaultPinnedListLimit
	}
	if limit > defaultPinnedListLimit {
		limit = defaultPinnedListLimit
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	var rows []messageRow
	err = a.metrics.TimePostgres("list_pinned_messages", func() error {
		msgs, listErr := a.messagesRepo.ListPinned(r.Context(), pool, channelID, limit)
		rows = make([]messageRow, len(msgs))
		for i, m := range msgs {
			rows[i] = messageRow(m)
		}
		return listErr
	})
	if err != nil {
		a.log.Error("list pinned messages", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list pinned messages")
		return
	}

	out := make([]messageResponse, len(rows))
	for i, m := range rows {
		out[i] = messageResponseFrom(m)
	}
	writeJSON(w, http.StatusOK, out)
}

// checkPinRateLimit mirrors checkReactionRateLimit's shape, covering both
// pin and unpin under one shared budget (CapabilityMessagePin) — see that
// capability's doc comment for why one direction can't be gated without
// the other.
func (a *App) checkPinRateLimit(w http.ResponseWriter, r *http.Request, identity Identity) bool {
	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, quota.CapabilityMessagePin, fmt.Sprintf("rate:pin:user:%s", identity.UserID))
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(quota.CapabilityMessagePin).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return false
	}
	return true
}
