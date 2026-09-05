// handlers_bookmarks.go backs the private, folder-organized bookmarks
// surface (internal/bookmarks) — POST/GET /bookmarks(/folders), PATCH/
// DELETE on a single bookmark or folder. Every one of these is scoped to
// the caller's own user_id (never another user's bookmarks, even within
// the same app) and none of it ever forwards to a channel's home region:
// bookmark_folders/bookmarks live in the control plane, not a shard, the
// same as internal/blocks — see internal/bookmarks' package doc comment
// for why.
package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/bookmarks"
	"github.com/darkoatanasovski/chat/internal/quota"
)

type folderResponse struct {
	FolderID  string `json:"folder_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

func folderResponseFrom(f bookmarks.Folder) folderResponse {
	return folderResponse{FolderID: f.FolderID.String(), Name: f.Name, CreatedAt: f.CreatedAt.Format(rfc3339Milli)}
}

type bookmarkResponse struct {
	BookmarkID string  `json:"bookmark_id"`
	ChannelID  string  `json:"channel_id"`
	MessageID  string  `json:"message_id"`
	FolderID   *string `json:"folder_id,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

func bookmarkResponseFrom(b bookmarks.Bookmark) bookmarkResponse {
	var folderID *string
	if b.FolderID != nil {
		s := b.FolderID.String()
		folderID = &s
	}
	return bookmarkResponse{
		BookmarkID: b.BookmarkID.String(),
		ChannelID:  b.ChannelID.String(),
		MessageID:  b.MessageID.String(),
		FolderID:   folderID,
		CreatedAt:  b.CreatedAt.Format(rfc3339Milli),
	}
}

type createFolderRequest struct {
	Name string `json:"name"`
}

// handleCreateBookmarkFolder backs POST /bookmarks/folders.
func (a *App) handleCreateBookmarkFolder(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	if !a.checkBookmarkRateLimit(w, r, identity) {
		return
	}

	var req createFolderRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, http.StatusBadRequest, "name is required (max 200 chars)")
		return
	}

	f, err := a.bookmarksRepo.CreateFolder(r.Context(), identity.AppID, identity.UserID, req.Name)
	if err != nil {
		if errors.Is(err, bookmarks.ErrFolderNameTaken) {
			writeError(w, http.StatusConflict, "a folder with that name already exists")
			return
		}
		a.log.Error("create bookmark folder", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create folder")
		return
	}

	writeJSON(w, http.StatusCreated, folderResponseFrom(f))
}

// handleListBookmarkFolders backs GET /bookmarks/folders.
func (a *App) handleListBookmarkFolders(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	folders, err := a.bookmarksRepo.ListFolders(r.Context(), identity.UserID)
	if err != nil {
		a.log.Error("list bookmark folders", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list folders")
		return
	}

	out := make([]folderResponse, len(folders))
	for i, f := range folders {
		out[i] = folderResponseFrom(f)
	}
	writeJSON(w, http.StatusOK, out)
}

type renameFolderRequest struct {
	Name string `json:"name"`
}

// handleRenameBookmarkFolder backs PATCH /bookmarks/folders/{folder_id}.
func (a *App) handleRenameBookmarkFolder(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	folderID, err := uuid.Parse(r.PathValue("folder_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid folder id")
		return
	}

	if !a.checkBookmarkRateLimit(w, r, identity) {
		return
	}

	var req renameFolderRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, http.StatusBadRequest, "name is required (max 200 chars)")
		return
	}

	f, err := a.bookmarksRepo.RenameFolder(r.Context(), identity.UserID, folderID, req.Name)
	if err != nil {
		if errors.Is(err, bookmarks.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "folder not found")
			return
		}
		if errors.Is(err, bookmarks.ErrFolderNameTaken) {
			writeError(w, http.StatusConflict, "a folder with that name already exists")
			return
		}
		a.log.Error("rename bookmark folder", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to rename folder")
		return
	}

	writeJSON(w, http.StatusOK, folderResponseFrom(f))
}

// handleDeleteBookmarkFolder backs DELETE /bookmarks/folders/{folder_id}.
// Bookmarks filed in the folder are not deleted — they're un-filed back to
// "unfiled" (see internal/bookmarks.Repo.DeleteFolder's doc comment).
func (a *App) handleDeleteBookmarkFolder(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	folderID, err := uuid.Parse(r.PathValue("folder_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid folder id")
		return
	}

	if !a.checkBookmarkRateLimit(w, r, identity) {
		return
	}

	if err := a.bookmarksRepo.DeleteFolder(r.Context(), identity.UserID, folderID); err != nil {
		if errors.Is(err, bookmarks.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "folder not found")
			return
		}
		a.log.Error("delete bookmark folder", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete folder")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type createBookmarkRequest struct {
	ChannelID string  `json:"channel_id"`
	MessageID string  `json:"message_id"`
	FolderID  *string `json:"folder_id,omitempty"`
}

// handleCreateBookmark backs POST /bookmarks. Unlike pinning, this never
// forwards to the message's home region — bookmark storage itself is
// control-plane, not sharded — but it does need that channel's shard pool
// for one thing: confirming the message actually exists there
// (internal/messages.Repo.Exists), since bookmarks.message_id can't carry
// a cross-database foreign key (see internal/bookmarks' package doc
// comment).
func (a *App) handleCreateBookmark(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	if !a.checkBookmarkRateLimit(w, r, identity) {
		return
	}

	var req createBookmarkRequest
	if !readJSON(w, r, &req) {
		return
	}
	channelID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel_id")
		return
	}
	messageID, err := uuid.Parse(req.MessageID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message_id")
		return
	}
	var folderID *uuid.UUID
	if req.FolderID != nil {
		id, err := uuid.Parse(*req.FolderID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid folder_id")
			return
		}
		folderID = &id
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

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}
	exists, err := a.messagesRepo.Exists(r.Context(), pool, channelID, messageID)
	if err != nil {
		a.log.Error("check message exists", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create bookmark")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "message not found in this channel")
		return
	}

	b, _, err := a.bookmarksRepo.Create(r.Context(), identity.AppID, identity.UserID, channelID, messageID, folderID)
	if err != nil {
		if errors.Is(err, bookmarks.ErrFolderNotFound) {
			writeError(w, http.StatusBadRequest, "folder not found")
			return
		}
		a.log.Error("create bookmark", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create bookmark")
		return
	}

	writeJSON(w, http.StatusCreated, bookmarkResponseFrom(b))
}

// handleListBookmarks backs GET /bookmarks?folder_id=. folder_id absent
// lists every bookmark regardless of folder; folder_id=none scopes to
// unfiled bookmarks only; any other value is parsed as a folder id to
// scope to that folder specifically (see
// internal/bookmarks.Repo.ListByFolder's doc comment for why nil there
// means "unfiled" rather than "no filter").
func (a *App) handleListBookmarks(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	q := r.URL.Query()
	var (
		list []bookmarks.Bookmark
		err  error
	)
	switch raw := q.Get("folder_id"); raw {
	case "":
		list, err = a.bookmarksRepo.List(r.Context(), identity.UserID)
	case "none":
		list, err = a.bookmarksRepo.ListByFolder(r.Context(), identity.UserID, nil)
	default:
		id, perr := uuid.Parse(raw)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "invalid folder_id")
			return
		}
		list, err = a.bookmarksRepo.ListByFolder(r.Context(), identity.UserID, &id)
	}
	if err != nil {
		a.log.Error("list bookmarks", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list bookmarks")
		return
	}

	out := make([]bookmarkResponse, len(list))
	for i, b := range list {
		out[i] = bookmarkResponseFrom(b)
	}
	writeJSON(w, http.StatusOK, out)
}

type moveBookmarkRequest struct {
	// FolderID nil (omitted, or explicit JSON null) un-files the bookmark
	// back to "unfiled" — this endpoint has exactly one field to set, so
	// unlike PATCH /apps/{app_id}'s multi-field partial update there's no
	// need to distinguish "not being changed" from "being cleared": every
	// call states the bookmark's full desired folder, one way or the
	// other.
	FolderID *string `json:"folder_id,omitempty"`
}

// handleMoveBookmark backs PATCH /bookmarks/{bookmark_id}.
func (a *App) handleMoveBookmark(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	bookmarkID, err := uuid.Parse(r.PathValue("bookmark_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid bookmark id")
		return
	}

	if !a.checkBookmarkRateLimit(w, r, identity) {
		return
	}

	var req moveBookmarkRequest
	if !readJSON(w, r, &req) {
		return
	}
	var folderID *uuid.UUID
	if req.FolderID != nil {
		id, err := uuid.Parse(*req.FolderID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid folder_id")
			return
		}
		folderID = &id
	}

	b, err := a.bookmarksRepo.Move(r.Context(), identity.UserID, bookmarkID, folderID)
	if err != nil {
		if errors.Is(err, bookmarks.ErrBookmarkNotFound) {
			writeError(w, http.StatusNotFound, "bookmark not found")
			return
		}
		if errors.Is(err, bookmarks.ErrFolderNotFound) {
			writeError(w, http.StatusBadRequest, "folder not found")
			return
		}
		a.log.Error("move bookmark", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to move bookmark")
		return
	}

	writeJSON(w, http.StatusOK, bookmarkResponseFrom(b))
}

// handleDeleteBookmark backs DELETE /bookmarks/{bookmark_id}.
func (a *App) handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	bookmarkID, err := uuid.Parse(r.PathValue("bookmark_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid bookmark id")
		return
	}

	if !a.checkBookmarkRateLimit(w, r, identity) {
		return
	}

	if err := a.bookmarksRepo.Delete(r.Context(), identity.UserID, bookmarkID); err != nil {
		if errors.Is(err, bookmarks.ErrBookmarkNotFound) {
			writeError(w, http.StatusNotFound, "bookmark not found")
			return
		}
		a.log.Error("delete bookmark", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete bookmark")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// checkBookmarkRateLimit mirrors checkReactionRateLimit's shape, gating
// every bookmark-side write (folders and bookmarks alike) under one shared
// budget — see CapabilityBookmarkWrite's doc comment for why they share
// one bucket instead of being split further.
func (a *App) checkBookmarkRateLimit(w http.ResponseWriter, r *http.Request, identity Identity) bool {
	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, quota.CapabilityBookmarkWrite, fmt.Sprintf("rate:bookmark:user:%s", identity.UserID))
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(quota.CapabilityBookmarkWrite).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return false
	}
	return true
}
