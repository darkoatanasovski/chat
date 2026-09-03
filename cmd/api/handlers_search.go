package main

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// handleSearchMessages backs GET /channels/{id}/messages/search?q=&limit= —
// the "search" capability. Read-only, so (like handleListMessages) it
// doesn't forward to the home region: message storage is reachable from any
// instance in this local topology.
func (a *App) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
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
		a.log.Error("load app for search capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search messages")
		return
	}
	if !app.ChannelCapabilities.Search {
		writeError(w, http.StatusForbidden, "search is not enabled for this app")
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	if len(query) > 256 {
		writeError(w, http.StatusBadRequest, "q is too long (max 256 chars)")
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

	var rows []messageRow
	err = a.metrics.TimePostgres("search_messages", func() error {
		msgs, searchErr := a.messagesRepo.Search(r.Context(), pool, channelID, query, limit)
		rows = make([]messageRow, len(msgs))
		for i, m := range msgs {
			rows[i] = messageRow(m)
		}
		return searchErr
	})
	if err != nil {
		a.log.Error("search messages", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search messages")
		return
	}

	out := make([]messageResponse, len(rows))
	for i, m := range rows {
		out[i] = messageResponseFrom(m)
	}
	writeJSON(w, http.StatusOK, out)
}
