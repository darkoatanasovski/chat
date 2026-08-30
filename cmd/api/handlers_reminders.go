package main

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

const maxReminderLead = 365 * 24 * time.Hour

type createReminderRequest struct {
	// RemindAt is an RFC3339 timestamp in the future.
	RemindAt string `json:"remind_at"`
}

type reminderResponse struct {
	ReminderID string `json:"reminder_id"`
	ChannelID  string `json:"channel_id"`
	MessageID  string `json:"message_id"`
	RemindAt   string `json:"remind_at"`
}

// handleCreateMessageReminder backs
// POST /channels/{id}/messages/{message_id}/reminders — the
// "message_reminders" capability. Delivered later, once, by cmd/worker's
// poll loop (internal/reminders.Repo.DeliverDue) — never synchronously here.
func (a *App) handleCreateMessageReminder(w http.ResponseWriter, r *http.Request) {
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
		a.log.Error("load app for message_reminders capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create reminder")
		return
	}
	if !app.ChannelCapabilities.MessageReminders {
		writeError(w, http.StatusForbidden, "message reminders are not enabled for this app")
		return
	}

	var req createReminderRequest
	if !readJSON(w, r, &req) {
		return
	}
	remindAt, err := time.Parse(time.RFC3339, req.RemindAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "remind_at must be an RFC3339 timestamp")
		return
	}
	now := time.Now().UTC()
	if !remindAt.After(now) {
		writeError(w, http.StatusBadRequest, "remind_at must be in the future")
		return
	}
	if remindAt.After(now.Add(maxReminderLead)) {
		writeError(w, http.StatusBadRequest, "remind_at is too far in the future")
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	exists, err := a.messagesRepo.Exists(r.Context(), pool, channelID, messageID)
	if err != nil {
		a.log.Error("check message exists for reminder", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create reminder")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "message not found in this channel")
		return
	}

	rem, err := a.remindersRepo.Create(r.Context(), pool, channelID, messageID, identity.UserID, remindAt)
	if err != nil {
		a.log.Error("create reminder", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create reminder")
		return
	}

	writeJSON(w, http.StatusCreated, reminderResponse{
		ReminderID: rem.ReminderID.String(),
		ChannelID:  rem.ChannelID.String(),
		MessageID:  rem.MessageID.String(),
		RemindAt:   rem.RemindAt.Format(rfc3339Milli),
	})
}

// handleCancelMessageReminder backs
// DELETE /channels/{id}/messages/{message_id}/reminders — cancels every
// not-yet-delivered reminder the caller holds for this message (see
// internal/reminders.Repo.Cancel's doc comment on why this cancels the
// whole set rather than one reminder_id at a time).
func (a *App) handleCancelMessageReminder(w http.ResponseWriter, r *http.Request) {
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

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	removed, err := a.remindersRepo.Cancel(r.Context(), pool, channelID, messageID, identity.UserID)
	if err != nil {
		a.log.Error("cancel reminder", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to cancel reminder")
		return
	}
	if removed == 0 {
		writeError(w, http.StatusNotFound, "no active reminder found for this message")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
