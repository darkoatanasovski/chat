package main

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// handleListPendingMessages backs GET /channels/{id}/messages/pending — the
// moderation queue for the "pending_messages" capability. Any channel
// member can see it (this codebase has no separate "moderator" role — see
// checkChannelWriteAccess's doc comment on why pinning/similar actions
// follow the same "any member" model); an app that wants moderation
// restricted to specific people enforces that in its own client, the same
// way it would for any other capability this platform exposes uniformly to
// every member.
func (a *App) handleListPendingMessages(w http.ResponseWriter, r *http.Request) {
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

	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for pending_messages capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list pending messages")
		return
	}
	if !app.ChannelCapabilities.PendingMessages {
		writeError(w, http.StatusForbidden, "pending messages are not enabled for this app")
		return
	}

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

	msgs, err := a.messagesRepo.ListPending(r.Context(), pool, channelID, limit)
	if err != nil {
		a.log.Error("list pending messages", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list pending messages")
		return
	}

	out := make([]messageResponse, len(msgs))
	for i, m := range msgs {
		out[i] = messageResponseFrom(messageRow(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApproveMessage backs POST /channels/{id}/messages/{message_id}/approve
// — flips a pending message to sent and, only now, delivers it to the rest
// of the channel (see internal/messages.Repo.Approve's doc comment).
func (a *App) handleApproveMessage(w http.ResponseWriter, r *http.Request) {
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

	route, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for pending_messages capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to approve message")
		return
	}
	if !app.ChannelCapabilities.PendingMessages {
		writeError(w, http.StatusForbidden, "pending messages are not enabled for this app")
		return
	}

	if route.HomeRegion != a.cfg.Region {
		a.forwardToHomeRegion(w, r, route.HomeRegion, nil)
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	msg, approved, err := a.messagesRepo.Approve(r.Context(), pool, channelID, messageID)
	if err != nil {
		a.log.Error("approve message", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to approve message")
		return
	}
	if !approved {
		writeError(w, http.StatusNotFound, "pending message not found")
		return
	}

	writeJSON(w, http.StatusOK, messageResponseFrom(messageRow(msg)))
}

// handleRejectMessage backs POST /channels/{id}/messages/{message_id}/reject
// — permanently discards a pending message (see internal/messages.Repo.Reject's
// doc comment; it was never visible to anyone but its own sender, so there's
// nothing to notify other members about).
func (a *App) handleRejectMessage(w http.ResponseWriter, r *http.Request) {
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

	route, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for pending_messages capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reject message")
		return
	}
	if !app.ChannelCapabilities.PendingMessages {
		writeError(w, http.StatusForbidden, "pending messages are not enabled for this app")
		return
	}

	if route.HomeRegion != a.cfg.Region {
		a.forwardToHomeRegion(w, r, route.HomeRegion, nil)
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	removed, err := a.messagesRepo.Reject(r.Context(), pool, channelID, messageID)
	if err != nil {
		a.log.Error("reject message", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reject message")
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "pending message not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
