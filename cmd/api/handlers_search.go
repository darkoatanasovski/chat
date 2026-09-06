package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// canReadChannel reports whether identity may read channelID: a member of it,
// or any user of the app when the channel is public (migrations/cell/0002).
func (a *App) canReadChannel(r *http.Request, channelID uuid.UUID, identity Identity) (bool, error) {
	isMember, err := a.membershipRepo.IsMember(r.Context(), channelID, identity.UserID)
	if err != nil {
		return false, err
	}
	if isMember {
		return true, nil
	}
	ch, err := a.channelsRepo.Get(r.Context(), channelID)
	if err != nil {
		return false, nil // not found / not readable
	}
	return ch.AppID == identity.AppID && ch.Visibility == "public", nil
}

// parseCustomFilter reads the optional ?custom= JSON-object filter (containment
// match against the custom column). Returns nil when absent, an error when the
// value isn't a valid JSON object.
func parseCustomFilter(r *http.Request) (json.RawMessage, bool) {
	raw := r.URL.Query().Get("custom")
	if raw == "" {
		return nil, true
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

func searchLimit(r *http.Request) int {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return limit
}

func (a *App) searchEnabled(w http.ResponseWriter, r *http.Request, identity Identity) bool {
	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for search capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search")
		return false
	}
	if !app.ChannelCapabilities.Search {
		writeError(w, http.StatusForbidden, "search is not enabled for this app")
		return false
	}
	return true
}

// handleSearchMessages backs GET /channels/{id}/messages/search?q=&custom=&limit=
// — the "search" capability. Full-text on the body and/or a custom-field
// containment filter; at least one of q/custom is required. Readable by members
// and, for public channels, any app user.
func (a *App) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	ok, err := a.canReadChannel(r, channelID, identity)
	if err != nil {
		a.log.Error("check channel read access", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search messages")
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	if !a.searchEnabled(w, r, identity) {
		return
	}

	query := r.URL.Query().Get("q")
	if len(query) > 256 {
		writeError(w, http.StatusBadRequest, "q is too long (max 256 chars)")
		return
	}
	custom, valid := parseCustomFilter(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "custom must be a JSON object")
		return
	}
	if query == "" && custom == nil {
		writeError(w, http.StatusBadRequest, "q or custom is required")
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	var rows []messageRow
	err = a.metrics.TimePostgres("search_messages", func() error {
		msgs, searchErr := a.messagesRepo.Search(r.Context(), pool, channelID, query, custom, searchLimit(r))
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

// handleSearchAppMessages backs GET /search/messages?q=&custom=&limit= — search
// across every channel in the app the caller can see (their channels + public
// ones).
func (a *App) handleSearchAppMessages(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())
	if !a.searchEnabled(w, r, identity) {
		return
	}

	query := r.URL.Query().Get("q")
	if len(query) > 256 {
		writeError(w, http.StatusBadRequest, "q is too long (max 256 chars)")
		return
	}
	custom, valid := parseCustomFilter(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "custom must be a JSON object")
		return
	}
	if query == "" && custom == nil {
		writeError(w, http.StatusBadRequest, "q or custom is required")
		return
	}

	var rows []messageRow
	err := a.metrics.TimePostgres("search_app_messages", func() error {
		msgs, searchErr := a.messagesRepo.SearchApp(r.Context(), a.cellPool, identity.AppID, identity.UserID, query, custom, searchLimit(r))
		rows = make([]messageRow, len(msgs))
		for i, m := range msgs {
			rows[i] = messageRow(m)
		}
		return searchErr
	})
	if err != nil {
		a.log.Error("search app messages", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search messages")
		return
	}

	out := make([]messageResponse, len(rows))
	for i, m := range rows {
		out[i] = messageResponseFrom(m)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSearchChannels backs GET /search/channels?q=&limit= — find channels in
// the app by name, restricted to public channels and the caller's own.
func (a *App) handleSearchChannels(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())
	if !a.searchEnabled(w, r, identity) {
		return
	}

	query := r.URL.Query().Get("q")
	if len(query) > 256 {
		writeError(w, http.StatusBadRequest, "q is too long (max 256 chars)")
		return
	}

	results, err := a.channelsRepo.Search(r.Context(), identity.AppID, identity.UserID, query, searchLimit(r))
	if err != nil {
		a.log.Error("search channels", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search channels")
		return
	}

	out := make([]channelResponse, len(results))
	for i, c := range results {
		out[i] = channelResponse{
			ChannelID:  c.ChannelID.String(),
			Name:       c.Name,
			Region:     a.cfg.Region,
			Visibility: c.Visibility,
			Custom:     c.Custom,
		}
	}
	writeJSON(w, http.StatusOK, out)
}
