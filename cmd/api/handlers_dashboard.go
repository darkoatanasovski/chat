// Dashboard handlers back the customer-facing dashboard app (separate from
// the demo/ chat test harness) — real per-person accounts (internal/orgusers)
// on top of the same organizations/apps/credentials this platform already
// had. Signup/login/accept-invite are public, matching this V1's existing
// "mint identity immediately, no email verification" trust model
// (docs/platform/security.md); everything else requires a dashboard session
// (requireOrgUser), and team management additionally requires the owner
// role (requireOwnerRole).
package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/darkoatanasovski/chat/internal/organizations"
	"github.com/darkoatanasovski/chat/internal/orgusers"
	"github.com/darkoatanasovski/chat/internal/quota"
)

const (
	dashboardSessionTTL = 7 * 24 * time.Hour
	minPasswordLen      = 8
)

type dashboardUserResponse struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type dashboardOrgResponse struct {
	OrgID int64  `json:"org_id"`
	Name  string `json:"name"`
	Tier  string `json:"tier"`
}

type dashboardAuthResponse struct {
	Token string                `json:"token"`
	User  dashboardUserResponse `json:"user"`
	Org   dashboardOrgResponse  `json:"org"`
}

func toDashboardUserResponse(u orgusers.User) dashboardUserResponse {
	return dashboardUserResponse{UserID: u.UserID.String(), Email: u.Email, Role: u.Role}
}

func toDashboardOrgResponse(o organizations.Org) dashboardOrgResponse {
	return dashboardOrgResponse{OrgID: o.OrgID, Name: o.Name, Tier: o.Tier}
}

func looksLikeEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1 && !strings.ContainsAny(email, " \t\n")
}

// ---- signup / login ----

type dashboardSignupRequest struct {
	OrgName  string `json:"org_name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleDashboardSignup creates a NEW organization together with its first
// human user (owner) in one step — the dashboard's own onboarding path,
// distinct from POST /organizations (which mints an org with no person
// attached, for programmatic/automation use like tools/loadtest and which
// does accept a caller-chosen tier). Self-serve signup always creates a
// FREE-tier org, full stop: there is no client input that can change that.
// A request that still sends a "tier" field (an old client, or a hostile
// one probing for a way to mint a paid-tier org) is rejected outright by
// the strict JSON decoder (readJSON's DisallowUnknownFields), since the
// field no longer exists on this type at all.
func (a *App) handleDashboardSignup(w http.ResponseWriter, r *http.Request) {
	var req dashboardSignupRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.OrgName = strings.TrimSpace(req.OrgName)
	if req.OrgName == "" || len(req.OrgName) > 128 {
		writeError(w, http.StatusBadRequest, "org_name is required (max 128 chars)")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !looksLikeEmail(req.Email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", minPasswordLen))
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		a.log.Error("hash password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create account")
		return
	}

	org, err := a.orgsSvc.CreateOrg(r.Context(), req.OrgName, quota.TierFree)
	if err != nil {
		a.log.Error("create organization", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create organization")
		return
	}

	u, err := a.orgUsersRepo.Create(r.Context(), org.OrgID, req.Email, string(hash), orgusers.RoleOwner)
	if err != nil {
		if errors.Is(err, orgusers.ErrEmailTaken) {
			writeError(w, http.StatusConflict, "an account with this email already exists")
			return
		}
		a.log.Error("create org user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create account")
		return
	}

	token, err := a.signer.IssueOrgUserToken(u.UserID.String(), dashboardSessionTTL)
	if err != nil {
		a.log.Error("issue session token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to issue session")
		return
	}

	writeJSON(w, http.StatusCreated, dashboardAuthResponse{Token: token, User: toDashboardUserResponse(u), Org: toDashboardOrgResponse(org)})
}

type dashboardLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *App) handleDashboardLogin(w http.ResponseWriter, r *http.Request) {
	var req dashboardLoginRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	u, err := a.orgUsersRepo.GetByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, orgusers.ErrNotFound) {
			// Same response as a wrong password — distinguishing "no such
			// email" from "wrong password" would let a caller enumerate
			// registered accounts.
			writeError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		a.log.Error("look up org user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to sign in")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	org, err := a.orgsRepo.Get(r.Context(), u.OrgID)
	if err != nil {
		a.log.Error("load organization", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to sign in")
		return
	}

	token, err := a.signer.IssueOrgUserToken(u.UserID.String(), dashboardSessionTTL)
	if err != nil {
		a.log.Error("issue session token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to issue session")
		return
	}

	writeJSON(w, http.StatusOK, dashboardAuthResponse{Token: token, User: toDashboardUserResponse(u), Org: toDashboardOrgResponse(org)})
}

// handleDashboardMe backs GET /dashboard/me — the dashboard's own "who am I
// signed in as" check, used on every page load.
func (a *App) handleDashboardMe(w http.ResponseWriter, r *http.Request) {
	orgUser, _ := orgUserIdentityFromContext(r.Context())
	orgIdentity, _ := orgIdentityFromContext(r.Context())

	u, err := a.orgUsersRepo.GetByID(r.Context(), orgUser.UserID)
	if err != nil {
		a.log.Error("load org user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load account")
		return
	}
	org, err := a.orgsRepo.Get(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("load organization", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load account")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		User dashboardUserResponse `json:"user"`
		Org  dashboardOrgResponse  `json:"org"`
	}{User: toDashboardUserResponse(u), Org: toDashboardOrgResponse(org)})
}

// ---- team management (owner-only, except listing) ----

type teamMemberResponse struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

// handleListTeam is readable by any org member, not just owners — everyone
// on the team can see who else is on it.
func (a *App) handleListTeam(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	members, err := a.orgUsersRepo.ListByOrg(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("list team", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list team")
		return
	}
	out := make([]teamMemberResponse, len(members))
	for i, u := range members {
		out[i] = teamMemberResponse{UserID: u.UserID.String(), Email: u.Email, Role: u.Role, CreatedAt: u.CreatedAt.Format(rfc3339Milli)}
	}
	writeJSON(w, http.StatusOK, out)
}

type createInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type inviteResponse struct {
	InviteID  string `json:"invite_id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Token     string `json:"token,omitempty"` // only ever present on the creation response
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

// handleCreateInvite mints an invite link and hands its one-time raw token
// straight back to the owner — there's no email infrastructure in this
// platform to send it for you, so the dashboard UI's job is to show it as a
// copyable link for the owner to share manually.
func (a *App) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	orgUser, _ := orgUserIdentityFromContext(r.Context())

	var req createInviteRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !looksLikeEmail(req.Email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if req.Role == "" {
		req.Role = orgusers.RoleMember
	}
	if req.Role != orgusers.RoleOwner && req.Role != orgusers.RoleMember {
		writeError(w, http.StatusBadRequest, "role must be owner or member")
		return
	}

	inv, token, err := a.orgInvitesRepo.Create(r.Context(), orgIdentity.OrgID, req.Email, req.Role, orgUser.UserID)
	if err != nil {
		a.log.Error("create invite", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create invite")
		return
	}

	writeJSON(w, http.StatusCreated, inviteResponse{
		InviteID: inv.InviteID.String(), Email: inv.Email, Role: inv.Role, Token: token,
		ExpiresAt: inv.ExpiresAt.Format(rfc3339Milli), CreatedAt: inv.CreatedAt.Format(rfc3339Milli),
	})
}

func (a *App) handleListInvites(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	invites, err := a.orgInvitesRepo.ListPendingByOrg(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("list invites", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list invites")
		return
	}
	out := make([]inviteResponse, len(invites))
	for i, inv := range invites {
		out[i] = inviteResponse{
			InviteID: inv.InviteID.String(), Email: inv.Email, Role: inv.Role,
			ExpiresAt: inv.ExpiresAt.Format(rfc3339Milli), CreatedAt: inv.CreatedAt.Format(rfc3339Milli),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type acceptInviteRequest struct {
	Password string `json:"password"`
}

// handleAcceptInvite is public/unauthenticated — the invited person doesn't
// have an account yet, that's the entire point of this call.
func (a *App) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	rawToken := r.PathValue("token")
	if rawToken == "" {
		writeError(w, http.StatusBadRequest, "invalid invite link")
		return
	}
	var req acceptInviteRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", minPasswordLen))
		return
	}

	inv, err := a.orgInvitesRepo.GetByToken(r.Context(), rawToken)
	if err != nil {
		if errors.Is(err, orgusers.ErrInviteNotFound) {
			writeError(w, http.StatusNotFound, "invite not found, expired, or already used")
			return
		}
		a.log.Error("look up invite", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to accept invite")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		a.log.Error("hash password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to accept invite")
		return
	}

	u, err := a.orgUsersRepo.Create(r.Context(), inv.OrgID, inv.Email, string(hash), inv.Role)
	if err != nil {
		if errors.Is(err, orgusers.ErrEmailTaken) {
			writeError(w, http.StatusConflict, "an account with this email already exists")
			return
		}
		a.log.Error("create org user from invite", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to accept invite")
		return
	}
	if err := a.orgInvitesRepo.MarkAccepted(r.Context(), inv.InviteID); err != nil {
		// Non-fatal: the account already exists and works. Worst case the
		// invite link stays technically re-usable until it expires — annoying,
		// not a security hole, since re-accepting just hits ErrEmailTaken.
		a.log.Warn("mark invite accepted", "error", err)
	}

	org, err := a.orgsRepo.Get(r.Context(), inv.OrgID)
	if err != nil {
		a.log.Error("load organization", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to accept invite")
		return
	}
	token, err := a.signer.IssueOrgUserToken(u.UserID.String(), dashboardSessionTTL)
	if err != nil {
		a.log.Error("issue session token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to issue session")
		return
	}

	writeJSON(w, http.StatusCreated, dashboardAuthResponse{Token: token, User: toDashboardUserResponse(u), Org: toDashboardOrgResponse(org)})
}

// handleRemoveTeamMember never lets an owner remove themselves (transfer
// ownership or have another owner do it) and never lets the last owner be
// removed (an org with zero owners could never invite/remove anyone again).
func (a *App) handleRemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	orgUser, _ := orgUserIdentityFromContext(r.Context())

	targetID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if targetID == orgUser.UserID {
		writeError(w, http.StatusBadRequest, "cannot remove yourself")
		return
	}

	target, err := a.orgUsersRepo.GetByID(r.Context(), targetID)
	if err != nil {
		if errors.Is(err, orgusers.ErrNotFound) {
			writeError(w, http.StatusNotFound, "team member not found")
			return
		}
		a.log.Error("load team member", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to remove team member")
		return
	}
	if target.OrgID != orgIdentity.OrgID {
		writeError(w, http.StatusNotFound, "team member not found")
		return
	}

	if target.Role == orgusers.RoleOwner {
		owners, err := a.orgUsersRepo.CountOwners(r.Context(), orgIdentity.OrgID)
		if err != nil {
			a.log.Error("count owners", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to remove team member")
			return
		}
		if owners <= 1 {
			writeError(w, http.StatusBadRequest, "cannot remove the last owner")
			return
		}
	}

	if err := a.orgUsersRepo.RemoveMember(r.Context(), orgIdentity.OrgID, targetID); err != nil {
		if errors.Is(err, orgusers.ErrNotFound) {
			writeError(w, http.StatusNotFound, "team member not found")
			return
		}
		a.log.Error("remove team member", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to remove team member")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- usage ----

type usageCount struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

type appUsageResponse struct {
	AppID    int64  `json:"app_id"`
	Name     string `json:"name"`
	Users    int    `json:"users"`
	Channels int    `json:"channels"`
}

type usageResponse struct {
	Tier string             `json:"tier"`
	Apps usageCount         `json:"apps"`
	List []appUsageResponse `json:"apps_detail"`
}

// handleDashboardUsage shows apps-used-vs-plan-limit (a real org-level cap)
// plus a per-app end-user/channel count. Those per-app counts are shown as
// plain numbers, not "X of Y" — max_channel_members et al. are tier limits
// enforced per end-user, not aggregate per-app caps, so there's no single
// limit to compare an app's total against. Deeper time-series usage lives
// in Grafana (see deploy/grafana) — this is the "at a glance, no separate
// tool" view.
func (a *App) handleDashboardUsage(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())

	org, err := a.orgsRepo.Get(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("load organization", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	limits, err := a.quota.LimitsFor(org.Tier)
	if err != nil {
		a.log.Error("resolve tier limits", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	appList, err := a.appsRepo.ListByOrg(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("list apps", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}

	detail := make([]appUsageResponse, len(appList))
	for i, app := range appList {
		userCount, err := a.usersSvc.CountByApp(r.Context(), app.AppID)
		if err != nil {
			a.log.Error("count app users", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load usage")
			return
		}
		channelCount, err := a.channelsRepo.CountByApp(r.Context(), app.AppID)
		if err != nil {
			a.log.Error("count app channels", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load usage")
			return
		}
		detail[i] = appUsageResponse{AppID: app.AppID, Name: app.Name, Users: userCount, Channels: channelCount}
	}

	writeJSON(w, http.StatusOK, usageResponse{
		Tier: org.Tier,
		Apps: usageCount{Used: len(appList), Limit: limits.MaxApps},
		List: detail,
	})
}

// ---- regions (world-map view) ----

// dashboardRegionOrder fixes the response order (and guarantees every
// region is present, even at 0) so the frontend map never has to guess
// which regions exist — same three regions validRegions accepts on
// POST /users.
var dashboardRegionOrder = []string{"eu", "us", "asia"}

type regionUsageResponse struct {
	Region string `json:"region"`
	Users  int    `json:"users"`
}

// handleDashboardRegions backs the dashboard's world-map widget: real
// end-user counts per home region, scoped to the calling org's own apps
// only (never a cross-tenant view, unlike raw Prometheus/Grafana which
// isn't org-scoped).
func (a *App) handleDashboardRegions(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())

	appList, err := a.appsRepo.ListByOrg(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("list apps", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load region usage")
		return
	}
	appIDs := make([]int64, len(appList))
	for i, app := range appList {
		appIDs[i] = app.AppID
	}

	counts, err := a.usersSvc.CountByRegion(r.Context(), appIDs)
	if err != nil {
		a.log.Error("count users by region", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load region usage")
		return
	}

	out := make([]regionUsageResponse, len(dashboardRegionOrder))
	for i, region := range dashboardRegionOrder {
		out[i] = regionUsageResponse{Region: region, Users: counts[region]}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- messages (overview stat) ----

type regionMessagesEntry struct {
	Region   string `json:"region"`
	Messages int64  `json:"messages"`
}

type dashboardMessagesResponse struct {
	Total    int64                 `json:"total"`
	ByRegion []regionMessagesEntry `json:"by_region"`
}

// dashboardMessagesFor computes total messages sent (broken down by
// region) across every channel belonging to appIDs — the shared core of
// both the per-app dashboard tab's messages stat (a single-element
// appIDs) and, previously, an org-wide aggregate across every app (now
// retired in favor of the per-app view — see handleDashboardAppMessages).
// Message counts live on the shard databases
// (channel_sequences.last_sequence, one row per channel, incremented on
// every send — see internal/messages), not the control plane, so this
// does a bounded scatter-gather over the small, fixed number of physical
// shards. That's a deliberate, documented exception to "no scatter-gather
// on hot paths" (INSTRUCTIONS.md §6): it never runs on the message-send
// path, only on this low-frequency admin read, same class as
// CountByRegion above.
func (a *App) dashboardMessagesFor(r *http.Request, appIDs []int64) (dashboardMessagesResponse, error) {
	routeInfo, err := a.channelsRepo.ListRouteInfoByApps(r.Context(), appIDs)
	if err != nil {
		return dashboardMessagesResponse{}, fmt.Errorf("list channel route info: %w", err)
	}

	channelsByShard := map[string][]uuid.UUID{}
	regionByChannel := map[uuid.UUID]string{}
	for _, c := range routeInfo {
		shardID, err := a.router.PhysicalShardID(c.VirtualShard)
		if err != nil {
			return dashboardMessagesResponse{}, fmt.Errorf("resolve physical shard: %w", err)
		}
		channelsByShard[shardID] = append(channelsByShard[shardID], c.ChannelID)
		regionByChannel[c.ChannelID] = c.HomeRegion
	}

	counts := map[string]int64{}
	var total int64
	for shardID, channelIDs := range channelsByShard {
		pool, err := a.shardPools.Get(shardID)
		if err != nil {
			return dashboardMessagesResponse{}, fmt.Errorf("resolve shard pool: %w", err)
		}
		sums, err := a.messagesRepo.SumSequencesByChannels(r.Context(), pool, channelIDs)
		if err != nil {
			return dashboardMessagesResponse{}, fmt.Errorf("sum message sequences: %w", err)
		}
		for channelID, count := range sums {
			counts[regionByChannel[channelID]] += count
			total += count
		}
	}

	out := make([]regionMessagesEntry, len(dashboardRegionOrder))
	for i, region := range dashboardRegionOrder {
		out[i] = regionMessagesEntry{Region: region, Messages: counts[region]}
	}
	return dashboardMessagesResponse{Total: total, ByRegion: out}, nil
}

// handleDashboardAppMessages backs GET /dashboard/apps/{app_id}/messages —
// this one app's messages-sent stat (total + by-region) for its Dashboard
// tab. Scoped to a single app rather than the org's whole app list, same
// "per app, not per org" shape as handleDashboardListChannels/
// handleDashboardListBlocks.
func (a *App) handleDashboardAppMessages(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	out, err := a.dashboardMessagesFor(r, []int64{app.AppID})
	if err != nil {
		a.log.Error("load app message usage", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load message usage")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- per-app daily messages (Apps grid mini chart) ----

const dashboardDailyWindowDays = 7

type appDailyMessagesEntry struct {
	AppID int64   `json:"app_id"`
	Name  string  `json:"name"`
	Total int64   `json:"total"`
	Today int64   `json:"today"`
	Daily []int64 `json:"daily"`
}

type dashboardMessagesDailyResponse struct {
	Days []string                 `json:"days"`
	Apps []appDailyMessagesEntry `json:"apps"`
}

// handleDashboardAppsMessagesDaily backs the Apps grid's per-app card: an
// all-time total (cheap, off channel_sequences, same source as
// dashboardMessagesFor) plus a message count for each of the last
// dashboardDailyWindowDays UTC calendar days — always exactly that many
// entries, oldest first, even for a day with zero messages, so the
// frontend's sparkline always draws a full week (see console's Sparkline
// component) instead of an empty chart. The daily breakdown scans the
// messages table (CountDailyByChannels) rather than reading
// channel_sequences, so unlike the all-time total this is deliberately
// bounded to one week. This one stays org-wide (every app in one response)
// since the Apps grid is a legitimate multi-app view — unlike the retired
// org-wide messages/polls dashboards, it was never replaced by a per-app
// endpoint.
func (a *App) handleDashboardAppsMessagesDaily(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())

	appList, err := a.appsRepo.ListByOrg(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("list apps", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load message usage")
		return
	}
	appIDs := make([]int64, len(appList))
	for i, app := range appList {
		appIDs[i] = app.AppID
	}

	routeInfo, err := a.channelsRepo.ListRouteInfoByApps(r.Context(), appIDs)
	if err != nil {
		a.log.Error("list channel route info", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load message usage")
		return
	}

	channelsByShard := map[string][]uuid.UUID{}
	appByChannel := map[uuid.UUID]int64{}
	for _, c := range routeInfo {
		shardID, err := a.router.PhysicalShardID(c.VirtualShard)
		if err != nil {
			a.log.Error("resolve physical shard", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load message usage")
			return
		}
		channelsByShard[shardID] = append(channelsByShard[shardID], c.ChannelID)
		appByChannel[c.ChannelID] = c.AppID
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	since := today.AddDate(0, 0, -(dashboardDailyWindowDays - 1))

	days := make([]string, dashboardDailyWindowDays)
	dayIndex := map[string]int{}
	for i := 0; i < dashboardDailyWindowDays; i++ {
		key := since.AddDate(0, 0, i).Format("2006-01-02")
		days[i] = key
		dayIndex[key] = i
	}

	// Pre-sized per app so every app reports a full week of zeros even if
	// it has channels but sent nothing, or no channels at all.
	dailyByApp := make(map[int64][]int64, len(appList))
	totalByApp := make(map[int64]int64, len(appList))
	for _, app := range appList {
		dailyByApp[app.AppID] = make([]int64, dashboardDailyWindowDays)
	}

	for shardID, channelIDs := range channelsByShard {
		pool, err := a.shardPools.Get(shardID)
		if err != nil {
			a.log.Error("resolve shard pool", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load message usage")
			return
		}

		sums, err := a.messagesRepo.SumSequencesByChannels(r.Context(), pool, channelIDs)
		if err != nil {
			a.log.Error("sum message sequences", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load message usage")
			return
		}
		for channelID, count := range sums {
			totalByApp[appByChannel[channelID]] += count
		}

		daily, err := a.messagesRepo.CountDailyByChannels(r.Context(), pool, channelIDs, since)
		if err != nil {
			a.log.Error("count daily messages", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load message usage")
			return
		}
		for _, dc := range daily {
			appID, ok := appByChannel[dc.ChannelID]
			if !ok {
				continue
			}
			idx, ok := dayIndex[dc.Day.Format("2006-01-02")]
			if !ok {
				continue
			}
			dailyByApp[appID][idx] += dc.Count
		}
	}

	out := make([]appDailyMessagesEntry, len(appList))
	for i, app := range appList {
		daily := dailyByApp[app.AppID]
		out[i] = appDailyMessagesEntry{
			AppID: app.AppID,
			Name:  app.Name,
			Total: totalByApp[app.AppID],
			Today: daily[len(daily)-1],
			Daily: daily,
		}
	}

	writeJSON(w, http.StatusOK, dashboardMessagesDailyResponse{Days: days, Apps: out})
}
