package main

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/quota"
)

type markReadRequest struct {
	// Sequence is optional — omitted/zero marks the channel read up to
	// whatever its latest message currently is (internal/readstate.Repo).
	Sequence int64 `json:"sequence"`
}

type readStateResponse struct {
	UserID           string `json:"user_id"`
	LastReadSequence int64  `json:"last_read_sequence"`
}

// handleMarkRead backs POST /channels/{id}/read. Same membership/app-scope/
// region-forwarding/rate-limit shape as handleAddReaction: read state lives
// on the channel's home-region shard, so it's a write like any other.
// Idempotent and monotonic — internal/readstate.Repo.MarkRead never lets the
// watermark regress, so this is safe to call repeatedly (e.g. once per
// channel open) without needing the client to track whether it already
// marked this sequence read.
func (a *App) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	route, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	// Gated on this app's "read_events" channel capability — read live
	// (never cached), same discipline as MessageEditEnabled's check in
	// handleEditMessage. Only the write side (marking read) is gated;
	// handleListReadState still reads back whatever watermark already
	// exists, the same way a disabled capability never retroactively
	// hides state a client already committed while it was on.
	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for read_events capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to mark read")
		return
	}
	if !app.ChannelCapabilities.ReadEvents {
		writeError(w, http.StatusForbidden, "read events are not enabled for this app")
		return
	}

	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, quota.CapabilityReadUpdate, "rate:read:user:"+identity.UserID.String())
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(quota.CapabilityReadUpdate).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return
	}

	if route.HomeRegion != a.cfg.Region {
		a.forwardToHomeRegion(w, r, route.HomeRegion, nil)
		return
	}

	var req markReadRequest
	// The body is optional (an empty POST just means "mark read up to
	// latest"), so a missing/empty body isn't a validation error the way it
	// would be for e.g. sending a message.
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	newSequence, _, err := a.readStateRepo.MarkRead(r.Context(), pool, channelID, identity.UserID, req.Sequence)
	if err != nil {
		a.log.Error("mark read", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to mark read")
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusOK, readStateResponse{UserID: identity.UserID.String(), LastReadSequence: newSequence})
}

// handleListReadState backs GET /channels/{id}/read-state — every member's
// current watermark, the snapshot a client loads once on opening a channel
// (after that, read.updated realtime events keep it current). Reads don't
// forward to the home region, matching handleListMessages/handleListMembers:
// shard storage is reachable from any instance in this local topology.
func (a *App) handleListReadState(w http.ResponseWriter, r *http.Request) {
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

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	states, err := a.readStateRepo.ListState(r.Context(), pool, channelID)
	if err != nil {
		a.log.Error("list read state", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list read state")
		return
	}

	out := make([]readStateResponse, len(states))
	for i, s := range states {
		out[i] = readStateResponse{UserID: s.UserID.String(), LastReadSequence: s.LastReadSequence}
	}
	writeJSON(w, http.StatusOK, out)
}
