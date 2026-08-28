package main

import (
	"net/http"

	"github.com/google/uuid"
)

type dashboardBlockRequest struct {
	BlockerUserID string `json:"blocker_user_id"`
	BlockedUserID string `json:"blocked_user_id"`
}

type dashboardBlockResponse struct {
	BlockerUserID string `json:"blocker_user_id"`
	BlockedUserID string `json:"blocked_user_id"`
}

// handleDashboardBlockUser backs POST /dashboard/apps/{app_id}/blocks — an
// app operator blocking any pair of the app's own end-users directly,
// distinct from handleBlockUser's self-service path: the operator doesn't
// have to *be* either user, they're acting administratively on the app
// they own (requireOwnedApp), the same override shape
// handleDashboardAddChannelMember already has for channel membership.
func (a *App) handleDashboardBlockUser(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	var req dashboardBlockRequest
	if !readJSON(w, r, &req) {
		return
	}
	blockerID, err := uuid.Parse(req.BlockerUserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid blocker_user_id")
		return
	}
	blockedID, err := uuid.Parse(req.BlockedUserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid blocked_user_id")
		return
	}
	if blockerID == blockedID {
		writeError(w, http.StatusBadRequest, "blocker_user_id and blocked_user_id must differ")
		return
	}

	for _, id := range [2]uuid.UUID{blockerID, blockedID} {
		u, err := a.usersSvc.Get(r.Context(), id)
		if err != nil || u.AppID != app.AppID {
			writeError(w, http.StatusBadRequest, "both users must belong to this app")
			return
		}
	}

	if _, err := a.blocksRepo.Block(r.Context(), app.AppID, blockerID, blockedID); err != nil {
		a.log.Error("dashboard block user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to block user")
		return
	}
	if err := a.blocksCache.AddPair(r.Context(), blockerID, blockedID); err != nil {
		a.log.Warn("update blocks cache", "error", err)
	}

	writeJSON(w, http.StatusCreated, dashboardBlockResponse{BlockerUserID: blockerID.String(), BlockedUserID: blockedID.String()})
}

// handleDashboardUnblockUser backs
// DELETE /dashboard/apps/{app_id}/blocks/{blocker_id}/{blocked_id} — the
// operator override of "the one who blocked can only unblock": removes the
// block regardless of who created it, scoped to this app (Repo.UnblockAny).
func (a *App) handleDashboardUnblockUser(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	blockerID, err := uuid.Parse(r.PathValue("blocker_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid blocker id")
		return
	}
	blockedID, err := uuid.Parse(r.PathValue("blocked_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid blocked id")
		return
	}

	removed, err := a.blocksRepo.UnblockAny(r.Context(), app.AppID, blockerID, blockedID)
	if err != nil {
		a.log.Error("dashboard unblock user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to unblock user")
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "block not found")
		return
	}

	if err := a.syncBlocksCacheAfterRemoval(r.Context(), blockerID, blockedID); err != nil {
		a.log.Warn("update blocks cache", "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

type dashboardBlockListEntry struct {
	BlockerUserID string `json:"blocker_user_id"`
	BlockedUserID string `json:"blocked_user_id"`
}

// handleDashboardListBlocks backs GET /dashboard/apps/{app_id}/blocks — an
// app-wide moderation view of every block, not just the caller's own.
func (a *App) handleDashboardListBlocks(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	pairs, err := a.blocksRepo.ListForApp(r.Context(), app.AppID)
	if err != nil {
		a.log.Error("dashboard list blocks", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list blocks")
		return
	}

	out := make([]dashboardBlockListEntry, len(pairs))
	for i, p := range pairs {
		out[i] = dashboardBlockListEntry{BlockerUserID: p.BlockerUserID.String(), BlockedUserID: p.BlockedUserID.String()}
	}
	writeJSON(w, http.StatusOK, out)
}
