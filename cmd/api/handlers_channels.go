package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/users"
)

type createChannelRequest struct {
	Name string `json:"name"`
	// Visibility is "public" or "private" (default). Public channels are
	// readable, joinable, and discoverable by any user of the app.
	Visibility string `json:"visibility,omitempty"`
	// Custom is app-defined JSON metadata (searchable).
	Custom json.RawMessage `json:"custom,omitempty"`
}

type channelResponse struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	// Region is the app's cell placement (this instance's region — the
	// router only sends the app's requests to its own cell). There is no
	// per-channel home_region or virtual_shard anymore (ADR 0006).
	Region     string          `json:"region"`
	Visibility string          `json:"visibility"`
	Custom     json.RawMessage `json:"custom,omitempty"`
}

// handleCreateChannel creates the channel in this cell — the only cell the
// app is pinned to. No region choice, no forwarding: the router already
// routed this request to the app's cell.
func (a *App) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	var req createChannelRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 128 {
		writeError(w, http.StatusBadRequest, "name is required (max 128 chars)")
		return
	}

	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	currentCount, err := a.channelsRepo.CountByCreator(r.Context(), identity.UserID)
	if err != nil {
		a.log.Error("count channels", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	decision, err := a.quota.AllowResource(tier, quota.CapabilityChannelCreate, currentCount)
	if err != nil {
		a.log.Error("quota check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	if !decision.Allowed {
		a.metrics.QuotaRejectionsTotal.WithLabelValues(quota.CapabilityChannelCreate).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return
	}

	if req.Visibility != "" && req.Visibility != channels.VisibilityPublic && req.Visibility != channels.VisibilityPrivate {
		writeError(w, http.StatusBadRequest, "visibility must be 'public' or 'private'")
		return
	}

	c, err := a.channelsSvc.CreateChannel(r.Context(), req.Name, identity.UserID, identity.AppID, req.Visibility, req.Custom)
	if err != nil {
		a.log.Error("create channel", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create channel")
		return
	}

	if err := a.membershipCache.SetMembers(r.Context(), c.ChannelID, []uuid.UUID{identity.UserID}); err != nil {
		a.log.Warn("seed membership cache", "error", err)
	}

	writeJSON(w, http.StatusCreated, channelResponse{
		ChannelID:  c.ChannelID.String(),
		Name:       c.Name,
		Region:     a.cfg.Region,
		Visibility: c.Visibility,
		Custom:     c.Custom,
	})
}

// handleJoinChannel lets any app user self-join a PUBLIC channel (POST
// /channels/{id}/join). Private channels reject the request — a member must add
// them via POST /channels/{id}/members instead. Idempotent.
func (a *App) handleJoinChannel(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())
	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	ch, err := a.channelsRepo.Get(r.Context(), channelID)
	if err != nil || ch.AppID != identity.AppID {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	if ch.Visibility != channels.VisibilityPublic {
		writeError(w, http.StatusForbidden, "this channel is private; ask a member to add you")
		return
	}
	if err := a.channelsRepo.JoinPublic(r.Context(), channelID, identity.UserID); err != nil {
		a.log.Error("join public channel", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to join channel")
		return
	}
	_ = a.membershipCache.AddMember(r.Context(), channelID, identity.UserID)
	writeJSON(w, http.StatusOK, map[string]string{"channel_id": channelID.String(), "status": "joined"})
}

type updateChannelRequest struct {
	Visibility *string         `json:"visibility,omitempty"`
	Custom     json.RawMessage `json:"custom,omitempty"`
}

// handleUpdateChannel lets a channel's creator change its visibility
// (PATCH /channels/{id}).
func (a *App) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())
	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	var req updateChannelRequest
	if !readJSON(w, r, &req) {
		return
	}
	ch, err := a.channelsRepo.Get(r.Context(), channelID)
	if err != nil || ch.AppID != identity.AppID {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	if ch.CreatedBy != identity.UserID {
		writeError(w, http.StatusForbidden, "only the channel creator can change it")
		return
	}
	if req.Visibility != nil {
		if *req.Visibility != channels.VisibilityPublic && *req.Visibility != channels.VisibilityPrivate {
			writeError(w, http.StatusBadRequest, "visibility must be 'public' or 'private'")
			return
		}
		if err := a.channelsRepo.SetVisibility(r.Context(), channelID, *req.Visibility); err != nil {
			a.log.Error("set visibility", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to update channel")
			return
		}
		ch.Visibility = *req.Visibility
	}
	writeJSON(w, http.StatusOK, channelResponse{
		ChannelID:  ch.ChannelID.String(),
		Name:       ch.Name,
		Region:     a.cfg.Region,
		Visibility: ch.Visibility,
		Custom:     ch.Custom,
	})
}

type addMemberRequest struct {
	UserID string `json:"user_id"`
}

// handleAddMember enforces channel membership authorization (only an
// existing member may add another — INSTRUCTIONS.md §43) and forwards to the
// channel's home region if this instance isn't it.
func (a *App) handleAddMember(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	isMember, err := a.membershipRepo.IsMember(r.Context(), channelID, identity.UserID)
	if err != nil {
		a.log.Error("check membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check membership")
		return
	}
	if !isMember {
		writeError(w, http.StatusForbidden, "only channel members can add members")
		return
	}

	route, err := a.region.Resolve(r.Context(), channelID.String())
	if err != nil {
		if errors.Is(err, channels.ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel not found")
			return
		}
		a.log.Error("resolve channel route", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load channel")
		return
	}
	// Defense in depth (INSTRUCTIONS.md §43): membership should only ever
	// exist within one app by construction, but never rely on that
	// invariant implicitly when the row already carries app_id to check
	// explicitly.
	if route.AppID != identity.AppID {
		writeError(w, http.StatusForbidden, "only channel members can add members")
		return
	}

	var req addMemberRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}
	newMemberID, err := uuid.Parse(req.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}

	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	currentCount, err := a.membershipRepo.CountMembers(r.Context(), channelID)
	if err != nil {
		a.log.Error("count members", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	decision, err := a.quota.AllowResource(tier, quota.CapabilityChannelMemberAdd, currentCount)
	if err != nil {
		a.log.Error("quota check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	if !decision.Allowed {
		a.metrics.QuotaRejectionsTotal.WithLabelValues(quota.CapabilityChannelMemberAdd).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return
	}

	if err := a.membershipRepo.AddMember(r.Context(), channelID, newMemberID); err != nil {
		a.log.Error("add member", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add member")
		return
	}
	if err := a.membershipCache.AddMember(r.Context(), channelID, newMemberID); err != nil {
		a.log.Warn("update membership cache", "error", err)
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"channel_id": channelID.String(),
		"user_id":    newMemberID.String(),
	})
}

// statusResponse is the "chat users have online status" shape shared by
// every user-listing response that carries presence (channel members, the
// dashboard's end-user list): a timestamp plus a boolean derived from it,
// never a separately tracked "connected" flag — see internal/users.IsOnline.
type statusResponse struct {
	// LastActiveAt is omitted entirely for a user with no tracked activity
	// yet (never connected, never sent a message/reaction/read-state
	// update) rather than serialized as null.
	LastActiveAt string `json:"last_active_at,omitempty"`
	IsOnline     bool   `json:"is_online"`
}

func buildStatus(lastActiveAt *time.Time) statusResponse {
	out := statusResponse{IsOnline: users.IsOnline(lastActiveAt)}
	if lastActiveAt != nil {
		out.LastActiveAt = lastActiveAt.Format(rfc3339Milli)
	}
	return out
}

type memberResponse struct {
	UserID      string         `json:"user_id"`
	DisplayName string         `json:"display_name"`
	Status      statusResponse `json:"status"`
}

// handleListMembers backs GET /channels/{id}/members — the UI's source of
// truth for "who's already in this channel" (as opposed to CountMembers,
// which only the quota check needs). Membership-gated like every other read
// on a channel's contents (INSTRUCTIONS.md §43).
// handleChannelAccess backs GET /channels/{id}/access — a lightweight
// membership check for the current bearer identity, used by the edge realtime
// worker (infra/cloudflare/realtime) to authorize a WebSocket connect before
// it terminates the socket at the edge. Auth is the caller's own user token
// (requireAuth), so it needs no internal key: a user is only ever told whether
// they themselves may join the channel. Returns 200 {user_id, member:true} for
// a member, 403 for a non-member (so the worker can reject the socket).
func (a *App) handleChannelAccess(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, map[string]any{"user_id": identity.UserID.String(), "member": true})
}

func (a *App) handleListMembers(w http.ResponseWriter, r *http.Request) {
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

	members, err := a.membershipRepo.ListMembersWithNames(r.Context(), channelID)
	if err != nil {
		a.log.Error("list members", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	out := make([]memberResponse, len(members))
	for i, m := range members {
		out[i] = memberResponse{UserID: m.UserID.String(), DisplayName: m.DisplayName, Status: buildStatus(m.LastActiveAt)}
	}
	writeJSON(w, http.StatusOK, out)
}

type userChannelResponse struct {
	ChannelID           string `json:"channel_id"`
	Name                string `json:"name"`
	Region              string `json:"region"`
	LastMessageSequence int64  `json:"last_message_sequence"`
	LastMessageAt       string `json:"last_message_at,omitempty"`
}

// handleListMyChannels backs GET /users/me/channels: one cell-local query
// keyed by user_id, never a scatter/gather across shards (INSTRUCTIONS.md §13).
func (a *App) handleListMyChannels(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	rows, err := a.membershipRepo.ListChannelsForUser(r.Context(), identity.UserID)
	if err != nil {
		a.log.Error("list channels for user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}

	out := make([]userChannelResponse, 0, len(rows))
	for _, row := range rows {
		item := userChannelResponse{
			ChannelID:           row.ChannelID.String(),
			Name:                row.ChannelName,
			Region:              a.cfg.Region,
			LastMessageSequence: row.LastMessageSequence,
		}
		if row.LastMessageAt != nil {
			item.LastMessageAt = row.LastMessageAt.Format(rfc3339Milli)
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"
