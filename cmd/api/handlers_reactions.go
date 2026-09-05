package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/events"
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/reactions"
	"github.com/darkoatanasovski/chat/internal/routing"
)

var validReactionsMessage = func() string {
	keys := make([]string, 0, len(reactions.ValidReactions))
	for k := range reactions.ValidReactions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "reaction must be one of: " + strings.Join(keys, ", ")
}()

type addReactionRequest struct {
	Reaction string `json:"reaction"`
}

type reactionSummaryResponse struct {
	Reaction  string `json:"reaction"`
	UserID    string `json:"user_id"`
	CreatedAt string `json:"created_at"`
}

type reactionStateResponse struct {
	ReactionCounts  map[string]int            `json:"reaction_counts"`
	LatestReactions []reactionSummaryResponse `json:"latest_reactions"`
}

func toReactionSummaryResponse(latest []events.ReactionSummary) []reactionSummaryResponse {
	out := make([]reactionSummaryResponse, len(latest))
	for i, s := range latest {
		out[i] = reactionSummaryResponse{Reaction: s.Reaction, UserID: s.UserID.String(), CreatedAt: s.CreatedAt.Format(rfc3339Milli)}
	}
	return out
}

// handleAddReaction backs POST /channels/{id}/messages/{message_id}/reactions.
// Idempotent — reacting again with the same key leaves state unchanged and
// doesn't re-emit a realtime event (internal/reactions.Repo.Add). Same
// membership/app-scope/region-forwarding/rate-limit shape as
// handleSendMessage: a reaction is a write against the channel's home-region
// shard, just like a message.
func (a *App) handleAddReaction(w http.ResponseWriter, r *http.Request) {
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

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	if !a.checkReactionCapability(w, r, identity) {
		return
	}

	if !a.checkReactionRateLimit(w, r, identity) {
		return
	}

	var req addReactionRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}
	req.Reaction = strings.TrimSpace(req.Reaction)
	if !reactions.ValidReactions[req.Reaction] {
		writeError(w, http.StatusBadRequest, validReactionsMessage)
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	counts, latest, _, err := a.reactionsRepo.Add(r.Context(), pool, channelID, messageID, identity.UserID, req.Reaction)
	if err != nil {
		if errors.Is(err, reactions.ErrMessageNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		a.log.Error("add reaction", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusOK, reactionStateResponse{ReactionCounts: counts, LatestReactions: toReactionSummaryResponse(latest)})
}

// handleRemoveReaction backs
// DELETE /channels/{id}/messages/{message_id}/reactions/{reaction} — always
// removes the caller's *own* reaction; there's no concept of removing
// someone else's.
func (a *App) handleRemoveReaction(w http.ResponseWriter, r *http.Request) {
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
	reaction := r.PathValue("reaction")
	if !reactions.ValidReactions[reaction] {
		writeError(w, http.StatusBadRequest, validReactionsMessage)
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	if !a.checkReactionCapability(w, r, identity) {
		return
	}

	if !a.checkReactionRateLimit(w, r, identity) {
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	counts, latest, _, err := a.reactionsRepo.Remove(r.Context(), pool, channelID, messageID, identity.UserID, reaction)
	if err != nil {
		if errors.Is(err, reactions.ErrMessageNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		a.log.Error("remove reaction", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusOK, reactionStateResponse{ReactionCounts: counts, LatestReactions: toReactionSummaryResponse(latest)})
}

// checkReactionCapability gates both add and remove on this app's
// "reactions" channel capability — read live (never cached) so flipping it
// via PATCH /apps/{app_id} takes effect on the very next attempt, same
// discipline as MessageEditEnabled's check in handleEditMessage. Removal is
// gated too, not just adding: with reactions off, a client shouldn't be
// able to touch reaction state at all, including clearing its own past
// reaction.
func (a *App) checkReactionCapability(w http.ResponseWriter, r *http.Request, identity Identity) bool {
	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for reaction capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check reaction capability")
		return false
	}
	if !app.ChannelCapabilities.Reactions {
		writeError(w, http.StatusForbidden, "reactions are not enabled for this app")
		return false
	}
	return true
}

// checkReactionRateLimit enforces reactions_per_minute, shared by add and
// remove (CapabilityReactionWrite) so spam-toggling a reaction on/off isn't
// a free end-run around a limit gating only one direction. Checked before
// the region-forward decision, same as handleSendMessage, so an
// over-limit request is rejected locally instead of paying a forward round
// trip first.
func (a *App) checkReactionRateLimit(w http.ResponseWriter, r *http.Request, identity Identity) bool {
	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, quota.CapabilityReactionWrite, fmt.Sprintf("rate:reaction:user:%s", identity.UserID))
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(quota.CapabilityReactionWrite).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return false
	}
	return true
}

// checkChannelWriteAccess is the membership + app-scope + route-resolution
// check shared by every write against an existing channel (send message,
// add member, add/remove reaction) — factored out here since reactions
// brought the third copy of this exact sequence. On failure it has already
// written the response; the caller just needs to return.
func (a *App) checkChannelWriteAccess(w http.ResponseWriter, r *http.Request, channelID uuid.UUID, identity Identity) (routing.ChannelRoute, bool) {
	isMember, err := a.membershipRepo.IsMember(r.Context(), channelID, identity.UserID)
	if err != nil {
		a.log.Error("check membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check membership")
		return routing.ChannelRoute{}, false
	}
	if !isMember {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return routing.ChannelRoute{}, false
	}

	route, err := a.region.Resolve(r.Context(), channelID.String())
	if err != nil {
		if errors.Is(err, channels.ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel not found")
			return routing.ChannelRoute{}, false
		}
		a.log.Error("resolve channel route", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load channel")
		return routing.ChannelRoute{}, false
	}
	// Defense in depth (INSTRUCTIONS.md §43): membership should only ever
	// exist within one app by construction, but never rely on that
	// invariant implicitly when the row already carries app_id to check
	// explicitly.
	if route.AppID != identity.AppID {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return routing.ChannelRoute{}, false
	}
	return route, true
}
