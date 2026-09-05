// Dashboard-driven end-user and channel administration — lets an org owner/
// member create end-users, create channels, and manage channel membership
// directly from the dashboard instead of only through a business's own
// backend calling the app-credentialed /users and /channels routes. Every
// handler here is org-auth scoped (requireOrgUser, wired in server.go) and
// re-verifies the {app_id}/{channel_id} path actually belongs to the caller's
// org before touching anything — the same ownership pattern handlers_apps.go
// already uses for credentials.
package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/users"
)

// ---- end-users ----

type dashboardEndUserResponse struct {
	UserID      string         `json:"user_id"`
	DisplayName string         `json:"display_name"`
	Region      string         `json:"region"`
	CreatedAt   string         `json:"created_at"`
	Status      statusResponse `json:"status"`
}

// region is the app's placement (config DB) — a user's region is its app's
// region now, not a per-user column.
func toDashboardEndUserResponse(u users.User, region string) dashboardEndUserResponse {
	return dashboardEndUserResponse{
		UserID:      u.UserID.String(),
		DisplayName: u.DisplayName,
		Region:      region,
		CreatedAt:   u.CreatedAt.Format(rfc3339Milli),
		Status:      buildStatus(u.LastActiveAt),
	}
}

// handleDashboardListEndUsers backs GET /dashboard/apps/{app_id}/users.
func (a *App) handleDashboardListEndUsers(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	list, err := a.usersSvc.ListByApp(r.Context(), app.AppID)
	if err != nil {
		a.log.Error("list end users", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list end-users")
		return
	}
	out := make([]dashboardEndUserResponse, len(list))
	for i, u := range list {
		out[i] = toDashboardEndUserResponse(u, app.Region)
	}
	writeJSON(w, http.StatusOK, out)
}

type dashboardCreateEndUserRequest struct {
	DisplayName string `json:"display_name"`
}

// handleDashboardCreateEndUser backs POST /dashboard/apps/{app_id}/users —
// the same identity this app's own end-users get via POST /users
// (app-credentialed), just minted by an org operator from the dashboard
// instead of the business's own backend. No quota gate: end-user creation
// itself has never been quota-checked in this platform (channels and
// channel members are — see handleCreateChannel/handleAddMember), only
// app count is.
func (a *App) handleDashboardCreateEndUser(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	var req dashboardCreateEndUserRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" || len(req.DisplayName) > 128 {
		writeError(w, http.StatusBadRequest, "display_name is required (max 128 chars)")
		return
	}
	// No region in the request: an end-user inherits its app's placement
	// region (config DB), so there's nothing for the operator to choose.
	u, err := a.usersSvc.CreateUser(r.Context(), req.DisplayName, app.AppID)
	if err != nil {
		a.log.Error("create end user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create end-user")
		return
	}
	writeJSON(w, http.StatusCreated, toDashboardEndUserResponse(u, app.Region))
}

// ---- playground tokens ----

// playgroundTokenTTL is deliberately shorter than handleCreateUser's
// tokenTTL (24h): a token minted from the dashboard exists so an operator
// can act as one of their app's end-users in the console's Playground for
// a session, not so it can be embedded in a real client. The Playground
// re-mints transparently when one expires.
const playgroundTokenTTL = time.Hour

type dashboardEndUserTokenResponse struct {
	UserID    string `json:"user_id"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// handleDashboardMintEndUserToken backs
// POST /dashboard/apps/{app_id}/users/{user_id}/token — mints a client
// bearer token for an EXISTING end-user of one of the caller's org's apps,
// identical in shape and scope to what POST /users hands a business's own
// backend (same signer, same claims), just issued from the dashboard. This
// is what lets the console's Playground drive the end-user API (send
// messages, react, vote, ...) as a chosen end-user without the browser ever
// touching an app secret. A user_id that exists but belongs to a different
// app is reported as 404, not 403, so an org can't probe whether a user id
// exists in an app it doesn't own.
func (a *App) handleDashboardMintEndUserToken(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	userID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	u, err := a.usersSvc.Get(r.Context(), userID)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			writeError(w, http.StatusNotFound, "end-user not found")
			return
		}
		a.log.Error("load end user for token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load end-user")
		return
	}
	if u.AppID != app.AppID {
		writeError(w, http.StatusNotFound, "end-user not found")
		return
	}

	token, err := a.signer.IssueUserToken(u.UserID.String(), app.Region, u.AppID, playgroundTokenTTL)
	if err != nil {
		a.log.Error("issue playground token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusCreated, dashboardEndUserTokenResponse{
		UserID:    u.UserID.String(),
		Token:     token,
		ExpiresAt: time.Now().Add(playgroundTokenTTL).UTC().Format(rfc3339Milli),
	})
}

// ---- channels ----

type dashboardChannelResponse struct {
	ChannelID   string `json:"channel_id"`
	Name        string `json:"name"`
	Region      string `json:"region"`
	CreatorName string `json:"creator_name"`
	MemberCount int    `json:"member_count"`
	CreatedAt   string `json:"created_at"`
}

// handleDashboardListChannels backs GET /dashboard/apps/{app_id}/channels.
func (a *App) handleDashboardListChannels(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	list, err := a.channelsRepo.ListByApp(r.Context(), app.AppID)
	if err != nil {
		a.log.Error("list channels", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}
	out := make([]dashboardChannelResponse, len(list))
	for i, c := range list {
		out[i] = dashboardChannelResponse{
			ChannelID: c.ChannelID.String(), Name: c.Name, Region: app.Region,
			CreatorName: c.CreatorName, MemberCount: c.MemberCount, CreatedAt: c.CreatedAt.Format(rfc3339Milli),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type dashboardCreateChannelRequest struct {
	Name          string `json:"name"`
	CreatorUserID string `json:"creator_user_id"`
}

// handleDashboardCreateChannel backs POST /dashboard/apps/{app_id}/channels.
// Every channel still needs a real end-user creator (channels.created_by is
// NOT NULL, and CapabilityChannelCreate quota is counted per-creator) —
// the dashboard operator picks which of the app's end-users that is, rather
// than the creator authenticating the request themselves like
// handleCreateChannel's normal end-user-driven path.
func (a *App) handleDashboardCreateChannel(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	var req dashboardCreateChannelRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 128 {
		writeError(w, http.StatusBadRequest, "name is required (max 128 chars)")
		return
	}
	creatorID, err := uuid.Parse(req.CreatorUserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "creator_user_id is required")
		return
	}
	creator, err := a.usersSvc.Get(r.Context(), creatorID)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "creator not found in this app")
			return
		}
		a.log.Error("load creator", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create channel")
		return
	}
	if creator.AppID != app.AppID {
		writeError(w, http.StatusBadRequest, "creator not found in this app")
		return
	}

	tier, err := a.appTiers.TierForApp(r.Context(), app.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	currentCount, err := a.channelsRepo.CountByCreator(r.Context(), creatorID)
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

	c, err := a.channelsSvc.CreateChannel(r.Context(), req.Name, creatorID, app.AppID)
	if err != nil {
		a.log.Error("create channel", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create channel")
		return
	}
	if err := a.membershipCache.SetMembers(r.Context(), c.ChannelID, []uuid.UUID{creatorID}); err != nil {
		a.log.Warn("seed membership cache", "error", err)
	}

	writeJSON(w, http.StatusCreated, dashboardChannelResponse{
		ChannelID: c.ChannelID.String(), Name: c.Name, Region: app.Region,
		CreatorName: creator.DisplayName, MemberCount: 1, CreatedAt: c.CreatedAt.Format(rfc3339Milli),
	})
}

// requireOwnedChannel resolves {channel_id} from the path and verifies it
// belongs to an app owned by the authenticated org — the channel-scoped
// analog of requireOwnedApp.
func (a *App) requireOwnedChannel(w http.ResponseWriter, r *http.Request, orgID int64) (channels.Channel, bool) {
	channelID, err := uuid.Parse(r.PathValue("channel_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return channels.Channel{}, false
	}
	ch, err := a.channelsRepo.Get(r.Context(), channelID)
	if err != nil {
		if errors.Is(err, channels.ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel not found")
			return channels.Channel{}, false
		}
		a.log.Error("load channel", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load channel")
		return channels.Channel{}, false
	}
	app, err := a.appsRepo.Get(r.Context(), ch.AppID)
	if err != nil || app.OrgID != orgID {
		writeError(w, http.StatusForbidden, "not authorized for this channel")
		return channels.Channel{}, false
	}
	return ch, true
}

// handleDashboardListChannelMembers backs GET /dashboard/channels/{channel_id}/members.
func (a *App) handleDashboardListChannelMembers(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	ch, ok := a.requireOwnedChannel(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	members, err := a.membershipRepo.ListMembersWithNames(r.Context(), ch.ChannelID)
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

// handleDashboardAddChannelMember backs POST /dashboard/channels/{channel_id}/members.
func (a *App) handleDashboardAddChannelMember(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	ch, ok := a.requireOwnedChannel(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	var req addMemberRequest
	if !readJSON(w, r, &req) {
		return
	}
	newMemberID, err := uuid.Parse(req.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	member, err := a.usersSvc.Get(r.Context(), newMemberID)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "end-user not found in this app")
			return
		}
		a.log.Error("load end user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add member")
		return
	}
	if member.AppID != ch.AppID {
		writeError(w, http.StatusBadRequest, "end-user not found in this app")
		return
	}

	tier, err := a.appTiers.TierForApp(r.Context(), ch.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	currentCount, err := a.membershipRepo.CountMembers(r.Context(), ch.ChannelID)
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

	if err := a.membershipRepo.AddMember(r.Context(), ch.ChannelID, newMemberID); err != nil {
		a.log.Error("add member", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add member")
		return
	}
	if err := a.membershipCache.AddMember(r.Context(), ch.ChannelID, newMemberID); err != nil {
		a.log.Warn("update membership cache", "error", err)
	}
	writeJSON(w, http.StatusCreated, memberResponse{UserID: newMemberID.String(), DisplayName: member.DisplayName, Status: buildStatus(member.LastActiveAt)})
}

// handleDashboardRemoveChannelMember backs
// DELETE /dashboard/channels/{channel_id}/members/{user_id}.
func (a *App) handleDashboardRemoveChannelMember(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	ch, ok := a.requireOwnedChannel(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	targetID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	if err := a.membershipRepo.RemoveMember(r.Context(), ch.ChannelID, targetID); err != nil {
		a.log.Error("remove member", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to remove member")
		return
	}
	if err := a.membershipCache.RemoveMember(r.Context(), ch.ChannelID, targetID); err != nil {
		a.log.Warn("update membership cache", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
