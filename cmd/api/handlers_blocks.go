package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/users"
)

type blockUserRequest struct {
	UserID string `json:"user_id"`
}

type blockResponse struct {
	BlockedUserID string `json:"blocked_user_id"`
}

// handleBlockUser backs POST /blocks — a user blocking another user of the
// same app. Idempotent: blocking someone already blocked just returns the
// current state rather than erroring. Enforcement is bidirectional (see
// internal/realtime.Delivery.ToChannelMembers and
// internal/messages.Repo.ListBefore's excludeSenders) even though only the
// caller's own outbound block row is created here — "the one who blocked
// can only unblock" governs removal, not who communication is cut off for.
func (a *App) handleBlockUser(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	var req blockUserRequest
	if !readJSON(w, r, &req) {
		return
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	if targetID == identity.UserID {
		writeError(w, http.StatusBadRequest, "cannot block yourself")
		return
	}

	target, err := a.usersSvc.Get(r.Context(), targetID)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "user not found in this app")
			return
		}
		a.log.Error("load user to block", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to block user")
		return
	}
	// Defense in depth (INSTRUCTIONS.md §43): a user_id from the same app
	// should already be guaranteed by usersSvc.Get scoping to the caller's
	// app, but never rely on that implicitly — see checkChannelWriteAccess
	// for the same pattern applied to channels.
	if target.AppID != identity.AppID {
		writeError(w, http.StatusBadRequest, "user not found in this app")
		return
	}

	if _, err := a.blocksRepo.Block(r.Context(), identity.AppID, identity.UserID, targetID); err != nil {
		a.log.Error("block user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to block user")
		return
	}
	if err := a.blocksCache.AddPair(r.Context(), identity.UserID, targetID); err != nil {
		a.log.Warn("update blocks cache", "error", err)
	}

	writeJSON(w, http.StatusCreated, blockResponse{BlockedUserID: targetID.String()})
}

// handleUnblockUser backs DELETE /blocks/{user_id}. Only the user who
// created the block may remove it — Repo.Unblock's WHERE clause scopes the
// delete to rows where the caller is the blocker, so a caller who was never
// the blocker of this pair (never blocked them, or the block runs the
// other direction) simply matches no rows and gets the same 404 as "never
// blocked at all," with no separate ownership check needed.
func (a *App) handleUnblockUser(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	targetID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	removed, err := a.blocksRepo.Unblock(r.Context(), identity.UserID, targetID)
	if err != nil {
		a.log.Error("unblock user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to unblock user")
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "block not found")
		return
	}

	if err := a.syncBlocksCacheAfterRemoval(r.Context(), identity.UserID, targetID); err != nil {
		a.log.Warn("update blocks cache", "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// syncBlocksCacheAfterRemoval re-checks Postgres before clearing the cache
// entry for a pair — unblocking only ever removes the caller's own row, so
// if the other user had independently blocked the caller too, that block
// must keep being enforced. See internal/blocks.Repo.Exists's doc comment.
func (a *App) syncBlocksCacheAfterRemoval(ctx context.Context, userA, userB uuid.UUID) error {
	stillBlocked, err := a.blocksRepo.Exists(ctx, userA, userB)
	if err != nil {
		return err
	}
	if stillBlocked {
		return nil
	}
	return a.blocksCache.RemovePair(ctx, userA, userB)
}

type blockListEntry struct {
	UserID string `json:"user_id"`
}

// handleListBlocks backs GET /blocks — the caller's own outbound block
// list ("who have I blocked"), not the bidirectional enforcement set.
func (a *App) handleListBlocks(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	blocked, err := a.blocksRepo.ListBlocked(r.Context(), identity.UserID)
	if err != nil {
		a.log.Error("list blocks", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list blocks")
		return
	}

	out := make([]blockListEntry, len(blocked))
	for i, id := range blocked {
		out[i] = blockListEntry{UserID: id.String()}
	}
	writeJSON(w, http.StatusOK, out)
}
