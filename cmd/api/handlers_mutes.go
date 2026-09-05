package api

import (
	"net/http"

	"github.com/google/uuid"
)

type muteUserRequest struct {
	UserID string `json:"user_id"`
}

type muteResponse struct {
	MutedUserID string `json:"muted_user_id"`
}

// handleMuteUser backs POST /channels/{id}/mutes — a channel member muting
// another member's messages within this one channel. Unlike blocking
// (internal/blocks, app-wide and enforced bidirectionally at the realtime
// layer), a mute is one-directional and purely advisory: see
// internal/mutes' package doc comment for exactly what this API does and
// doesn't do with it. Idempotent: muting someone already muted here just
// returns the current state rather than erroring.
func (a *App) handleMuteUser(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	if _, ok := a.checkChannelWriteAccess(w, r, channelID, identity); !ok {
		return
	}

	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for mutes capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to mute user")
		return
	}
	if !app.ChannelCapabilities.Mutes {
		writeError(w, http.StatusForbidden, "mutes are not enabled for this app")
		return
	}

	var req muteUserRequest
	if !readJSON(w, r, &req) {
		return
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	if targetID == identity.UserID {
		writeError(w, http.StatusBadRequest, "cannot mute yourself")
		return
	}
	isTargetMember, err := a.membershipRepo.IsMember(r.Context(), channelID, targetID)
	if err != nil {
		a.log.Error("check target membership for mute", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to mute user")
		return
	}
	if !isTargetMember {
		writeError(w, http.StatusBadRequest, "user is not a member of this channel")
		return
	}

	if _, err := a.mutesRepo.Mute(r.Context(), channelID, identity.UserID, targetID); err != nil {
		a.log.Error("mute user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to mute user")
		return
	}

	writeJSON(w, http.StatusCreated, muteResponse{MutedUserID: targetID.String()})
}

// handleUnmuteUser backs DELETE /channels/{id}/mutes/{user_id}. Only the
// user who created the mute may remove it — Repo.Unmute's WHERE clause
// scopes the delete to rows where the caller is the muter, same "the one
// who muted can only unmute" restriction as blocks.Repo.Unblock.
func (a *App) handleUnmuteUser(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	targetID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	if _, ok := a.checkChannelWriteAccess(w, r, channelID, identity); !ok {
		return
	}

	removed, err := a.mutesRepo.Unmute(r.Context(), channelID, identity.UserID, targetID)
	if err != nil {
		a.log.Error("unmute user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to unmute user")
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "mute not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type muteListEntry struct {
	UserID string `json:"user_id"`
}

// handleListMutes backs GET /channels/{id}/mutes — the caller's own list of
// who they've muted in this channel ("who have I muted here"), the set a
// client is expected to filter its own notifications/rendering against.
func (a *App) handleListMutes(w http.ResponseWriter, r *http.Request) {
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

	muted, err := a.mutesRepo.ListMuted(r.Context(), channelID, identity.UserID)
	if err != nil {
		a.log.Error("list mutes", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list mutes")
		return
	}

	out := make([]muteListEntry, len(muted))
	for i, id := range muted {
		out[i] = muteListEntry{UserID: id.String()}
	}
	writeJSON(w, http.StatusOK, out)
}
