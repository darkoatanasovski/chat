package api

import (
	"bytes"
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
}

type channelResponse struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	// Region is the app's cell placement (this instance's region — the
	// router only sends the app's requests to its own cell). There is no
	// per-channel home_region or virtual_shard anymore (ADR 0006).
	Region string `json:"region"`
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

	c, err := a.channelsSvc.CreateChannel(r.Context(), req.Name, identity.UserID, identity.AppID)
	if err != nil {
		a.log.Error("create channel", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create channel")
		return
	}

	if err := a.membershipCache.SetMembers(r.Context(), c.ChannelID, []uuid.UUID{identity.UserID}); err != nil {
		a.log.Warn("seed membership cache", "error", err)
	}

	writeJSON(w, http.StatusCreated, channelResponse{
		ChannelID: c.ChannelID.String(),
		Name:      c.Name,
		Region:    a.cfg.Region,
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
