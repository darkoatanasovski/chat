package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// signUpDashboardOrg drives POST /dashboard/signup end to end and returns
// the new owner's session token, org_id, and email — a fixture for every
// other dashboard test. Self-serve signup always creates a FREE org (see
// handleDashboardSignup) — there's no tier to pass in.
func signUpDashboardOrg(t *testing.T, app *App, orgName string) (token string, orgID int64, email string) {
	t.Helper()
	email = "owner-" + uuid.NewString() + "@example.com"
	var resp dashboardAuthResponse
	rec := do(t, app, jsonRequest("POST", "/dashboard/signup", dashboardSignupRequest{
		OrgName: orgName, Email: email, Password: "hunter22222",
	}), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.User.Role != "owner" {
		t.Fatalf("expected the first signup user to be owner, got %q", resp.User.Role)
	}
	return resp.Token, resp.Org.OrgID, email
}

// bumpOrgTierForTest raises an org's tier directly in the control DB,
// bypassing the API entirely — the only way to get a non-FREE org out of
// dashboard signup now that it's hard-locked to FREE, needed by the rare
// test that exercises quota-gated behavior (e.g. a second app in one org)
// unrelated to what it's actually testing.
func bumpOrgTierForTest(t *testing.T, app *App, orgID int64, tier string) {
	t.Helper()
	if _, err := app.configPool.Exec(context.Background(), `UPDATE organizations SET tier = $1 WHERE org_id = $2`, tier, orgID); err != nil {
		t.Fatalf("bump org tier: %v", err)
	}
}

func TestDashboardSignup_ValidCreatesOwner(t *testing.T) {
	app := testApp(t)
	token, orgID, email := signUpDashboardOrg(t, app, "Acme Inc")
	if token == "" || orgID == 0 {
		t.Fatalf("expected a non-empty token and org id")
	}

	var me struct {
		User dashboardUserResponse `json:"user"`
		Org  dashboardOrgResponse  `json:"org"`
	}
	rec := do(t, app, authed(jsonRequest("GET", "/dashboard/me", nil), token), &me)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if me.User.Email != email || me.Org.OrgID != orgID || me.Org.Tier != "FREE" {
		t.Fatalf("unexpected /dashboard/me response: %+v", me)
	}
}

func TestDashboardSignup_Validation(t *testing.T) {
	app := testApp(t)
	cases := []struct {
		name string
		req  any
	}{
		{"empty org name", dashboardSignupRequest{OrgName: "  ", Email: "a@example.com", Password: "longenough"}},
		{"invalid email", dashboardSignupRequest{OrgName: "Acme", Email: "not-an-email", Password: "longenough"}},
		{"short password", dashboardSignupRequest{OrgName: "Acme", Email: "a@example.com", Password: "short"}},
		// dashboardSignupRequest has no Tier field at all anymore — self-serve
		// signup is FREE-only, full stop. The strict JSON decoder
		// (DisallowUnknownFields, see readJSON) means a request that still
		// includes a "tier" field — an old client, or a hostile one probing
		// for a way to mint a paid-tier org — is rejected outright rather
		// than silently ignored.
		{"unknown tier field rejected", map[string]any{
			"org_name": "Acme", "tier": "ENTERPRISE", "email": "a@example.com", "password": "longenough",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, app, jsonRequest("POST", "/dashboard/signup", tc.req), nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestDashboardSignup_DuplicateEmailRejected(t *testing.T) {
	app := testApp(t)
	email := "dup-" + uuid.NewString() + "@example.com"
	req := dashboardSignupRequest{OrgName: "First Org", Email: email, Password: "hunter22222"}
	rec := do(t, app, jsonRequest("POST", "/dashboard/signup", req), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first signup: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req2 := dashboardSignupRequest{OrgName: "Second Org", Email: email, Password: "differentpass"}
	rec = do(t, app, jsonRequest("POST", "/dashboard/signup", req2), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate signup: status = %d, want 409", rec.Code)
	}
}

func TestDashboardLogin_ValidAndInvalid(t *testing.T) {
	app := testApp(t)
	_, _, email := signUpDashboardOrg(t, app, "Login Test Org")

	var resp dashboardAuthResponse
	rec := do(t, app, jsonRequest("POST", "/dashboard/login", dashboardLoginRequest{Email: email, Password: "hunter22222"}), &resp)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.Token == "" {
		t.Fatalf("expected a non-empty session token")
	}

	rec = do(t, app, jsonRequest("POST", "/dashboard/login", dashboardLoginRequest{Email: email, Password: "wrong-password"}), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", rec.Code)
	}

	rec = do(t, app, jsonRequest("POST", "/dashboard/login", dashboardLoginRequest{Email: "nobody-" + uuid.NewString() + "@example.com", Password: "whatever12"}), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown email: status = %d, want 401", rec.Code)
	}
}

func TestDashboardOrgAuth_AcceptsBothOrgAdminAndOrgUserTokens(t *testing.T) {
	app := testApp(t)

	// Org-admin token path (existing, unattributed).
	_, orgAdminToken := createTestOrg(t, app, "FREE")
	rec := do(t, app, authed(jsonRequest("GET", "/organizations/1/apps", nil), orgAdminToken), nil)
	// org_id in the path won't match this brand-new org's real id, so this
	// specifically checks the token TYPE is accepted (not rejected as
	// wrong-claims-type) — a 403 (org id mismatch) proves the token passed
	// requireOrgAuth's type check and reached the ownership check inside.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("org-admin token against mismatched org_id: status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}

	// Dashboard session path (new, attributed to a person).
	dashToken, orgID, _ := signUpDashboardOrg(t, app, "Auth Path Org")
	var appsResp []appResponse
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/organizations/%d/apps", orgID), nil), dashToken), &appsResp)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard session against its own org: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardTeam_InviteAcceptAndList(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, ownerEmail := signUpDashboardOrg(t, app, "Team Org")
	_ = ownerEmail

	inviteeEmail := "invitee-" + uuid.NewString() + "@example.com"
	var invite inviteResponse
	rec := do(t, app, authed(jsonRequest("POST", "/dashboard/team/invites", createInviteRequest{Email: inviteeEmail, Role: "member"}), ownerToken), &invite)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invite: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if invite.Token == "" {
		t.Fatalf("expected the invite's one-time token to be returned at creation")
	}

	var pending []inviteResponse
	rec = do(t, app, authed(jsonRequest("GET", "/dashboard/team/invites", nil), ownerToken), &pending)
	if rec.Code != http.StatusOK || len(pending) != 1 {
		t.Fatalf("list invites: status = %d, body = %+v", rec.Code, pending)
	}
	if pending[0].Token != "" {
		t.Fatalf("expected the listed invite to never re-expose its token, got %+v", pending[0])
	}

	var accepted dashboardAuthResponse
	rec = do(t, app, jsonRequest("POST", fmt.Sprintf("/dashboard/invites/%s/accept", invite.Token), acceptInviteRequest{Password: "newmember123"}), &accepted)
	if rec.Code != http.StatusCreated {
		t.Fatalf("accept invite: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if accepted.User.Role != "member" || accepted.Org.OrgID != orgID {
		t.Fatalf("unexpected accepted-invite response: %+v", accepted)
	}

	var team []teamMemberResponse
	rec = do(t, app, authed(jsonRequest("GET", "/dashboard/team", nil), ownerToken), &team)
	if rec.Code != http.StatusOK || len(team) != 2 {
		t.Fatalf("expected 2 team members (owner + accepted invite), got %d: %+v", len(team), team)
	}

	// The invite is now consumed — accepting it again must fail, not
	// silently create a second account.
	rec = do(t, app, jsonRequest("POST", fmt.Sprintf("/dashboard/invites/%s/accept", invite.Token), acceptInviteRequest{Password: "anotherpass1"}), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("re-accept: status = %d, want 404", rec.Code)
	}
}

func TestDashboardTeam_InviteRequiresOwnerRole(t *testing.T) {
	app := testApp(t)
	ownerToken, _, _ := signUpDashboardOrg(t, app, "Owner Gate Org")

	var invite inviteResponse
	memberEmail := "member-" + uuid.NewString() + "@example.com"
	do(t, app, authed(jsonRequest("POST", "/dashboard/team/invites", createInviteRequest{Email: memberEmail, Role: "member"}), ownerToken), &invite)

	var memberSession dashboardAuthResponse
	do(t, app, jsonRequest("POST", fmt.Sprintf("/dashboard/invites/%s/accept", invite.Token), acceptInviteRequest{Password: "memberpass1"}), &memberSession)

	// A plain member can't invite others.
	rec := do(t, app, authed(jsonRequest("POST", "/dashboard/team/invites", createInviteRequest{Email: "someone-" + uuid.NewString() + "@example.com", Role: "member"}), memberSession.Token), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member inviting: status = %d, want 403", rec.Code)
	}

	// But a plain member CAN read the team list.
	rec = do(t, app, authed(jsonRequest("GET", "/dashboard/team", nil), memberSession.Token), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("member listing team: status = %d, want 200", rec.Code)
	}
}

func TestDashboardTeam_RemoveMember(t *testing.T) {
	app := testApp(t)
	ownerToken, _, _ := signUpDashboardOrg(t, app, "Remove Org")

	var invite inviteResponse
	do(t, app, authed(jsonRequest("POST", "/dashboard/team/invites", createInviteRequest{Email: "removeme-" + uuid.NewString() + "@example.com", Role: "member"}), ownerToken), &invite)
	var memberSession dashboardAuthResponse
	do(t, app, jsonRequest("POST", fmt.Sprintf("/dashboard/invites/%s/accept", invite.Token), acceptInviteRequest{Password: "memberpass1"}), &memberSession)

	rec := do(t, app, authed(jsonRequest("DELETE", "/dashboard/team/"+memberSession.User.UserID, nil), ownerToken), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove member: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var team []teamMemberResponse
	do(t, app, authed(jsonRequest("GET", "/dashboard/team", nil), ownerToken), &team)
	if len(team) != 1 {
		t.Fatalf("expected 1 remaining team member (the owner), got %d", len(team))
	}
}

func TestDashboardTeam_CannotRemoveLastOwner(t *testing.T) {
	app := testApp(t)
	ownerToken, _, _ := signUpDashboardOrg(t, app, "Last Owner Org")

	var invite inviteResponse
	do(t, app, authed(jsonRequest("POST", "/dashboard/team/invites", createInviteRequest{Email: "co-owner-" + uuid.NewString() + "@example.com", Role: "owner"}), ownerToken), &invite)
	var coOwnerSession dashboardAuthResponse
	do(t, app, jsonRequest("POST", fmt.Sprintf("/dashboard/invites/%s/accept", invite.Token), acceptInviteRequest{Password: "coownerpass1"}), &coOwnerSession)

	// With two owners, removing one is fine.
	rec := do(t, app, authed(jsonRequest("DELETE", "/dashboard/team/"+coOwnerSession.User.UserID, nil), ownerToken), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove co-owner: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Re-invite the co-owner back so we can attempt to remove the ORIGINAL
	// owner and confirm the "last owner" guard triggers from the other side.
	var invite2 inviteResponse
	do(t, app, authed(jsonRequest("POST", "/dashboard/team/invites", createInviteRequest{Email: "co-owner2-" + uuid.NewString() + "@example.com", Role: "owner"}), ownerToken), &invite2)
	var coOwner2 dashboardAuthResponse
	do(t, app, jsonRequest("POST", fmt.Sprintf("/dashboard/invites/%s/accept", invite2.Token), acceptInviteRequest{Password: "coowner2pass"}), &coOwner2)

	rec = do(t, app, authed(jsonRequest("DELETE", "/dashboard/team/"+coOwner2.User.UserID, nil), coOwner2.Token), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("removing self should already be rejected: status = %d, want 400", rec.Code)
	}
}

func TestDashboardUsage_ReflectsAppsAndPlanLimit(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Usage Org")

	var usage usageResponse
	rec := do(t, app, authed(jsonRequest("GET", "/dashboard/usage", nil), ownerToken), &usage)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if usage.Tier != "FREE" || usage.Apps.Limit != 1 || usage.Apps.Used != 0 {
		t.Fatalf("unexpected initial usage: %+v", usage)
	}

	createTestApp(t, app, orgID, ownerToken)

	do(t, app, authed(jsonRequest("GET", "/dashboard/usage", nil), ownerToken), &usage)
	if usage.Apps.Used != 1 {
		t.Fatalf("expected apps.used=1 after creating an app, got %+v", usage)
	}
}

// TestDashboardRegions_CountsEndUsersByRegionScopedToOwnOrg exercises the
// world-map data source end to end: real end-users created against two
// different orgs' apps must never leak into each other's region counts, and
// every region in dashboardRegionOrder must always be present (0 if empty).
func TestDashboardRegions_CountsEndUsersByRegionScopedToOwnOrg(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Regions Org")
	appID, key, secret := createTestApp(t, app, orgID, ownerToken)

	for _, region := range []string{"eu", "eu", "us"} {
		rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "u-" + uuid.NewString(), Region: region}), appAccessToken(t, app, key, secret)), nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create end user in %s: status = %d, body = %s", region, rec.Code, rec.Body.String())
		}
	}

	var regions []regionUsageResponse
	rec := do(t, app, authed(jsonRequest("GET", "/dashboard/regions", nil), ownerToken), &regions)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := map[string]int{}
	for _, r := range regions {
		got[r.Region] = r.Users
	}
	if got["eu"] != 2 || got["us"] != 1 || got["asia"] != 0 {
		t.Fatalf("unexpected region counts for app %d: %+v", appID, got)
	}

	// A second org's dashboard token must see nothing from the first org's app.
	otherToken, _, _ := signUpDashboardOrg(t, app, "Other Regions Org")
	var otherRegions []regionUsageResponse
	do(t, app, authed(jsonRequest("GET", "/dashboard/regions", nil), otherToken), &otherRegions)
	for _, r := range otherRegions {
		if r.Users != 0 {
			t.Fatalf("expected a brand-new org to see zero users everywhere, got %+v", otherRegions)
		}
	}
}

// TestDashboardAppMessages_SumsSentMessagesByRegionScopedToOwnApp exercises
// the app Dashboard tab's messages-sent stat end to end: it reads exact
// per-channel message counts off channel_sequences on the shard databases
// (never scanning the messages table itself — see
// messages.Repo.SumSequencesByChannels) and must never leak another app's
// message counts into the total — whether that other app belongs to a
// different org entirely, or is simply a second app within the SAME org
// (the whole point of moving this from an org-wide aggregate to a
// per-app view).
func TestDashboardAppMessages_SumsSentMessagesByRegionScopedToOwnApp(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Messages Org")
	appID, key, secret := createTestApp(t, app, orgID, ownerToken)

	var user createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "Sender", Region: "eu"}), appAccessToken(t, app, key, secret)), &user)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create test user: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channel := createTestChannel(t, app, user.Token, "general")
	for i := range 3 {
		sendTestMessage(t, app, user.Token, channel.ChannelID, fmt.Sprintf("message %d", i))
	}

	appMessagesPath := fmt.Sprintf("/dashboard/apps/%d/messages", appID)
	var messages dashboardMessagesResponse
	rec = do(t, app, authed(jsonRequest("GET", appMessagesPath, nil), ownerToken), &messages)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if messages.Total != 3 {
		t.Fatalf("expected total=3, got %+v", messages)
	}
	got := map[string]int64{}
	for _, r := range messages.ByRegion {
		got[r.Region] = r.Messages
	}
	// Test channels always home to the test API instance's own region (see
	// buildTestApp's Region: "eu") since home_region isn't caller-supplied.
	if got["eu"] != 3 || got["us"] != 0 || got["asia"] != 0 {
		t.Fatalf("unexpected region counts: %+v", got)
	}

	// A second app in the SAME org must see none of the first app's
	// messages — this is the per-app isolation this endpoint exists for.
	otherAppID, _, _ := createTestApp(t, app, orgID, ownerToken)
	var otherAppMessages dashboardMessagesResponse
	do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/apps/%d/messages", otherAppID), nil), ownerToken), &otherAppMessages)
	if otherAppMessages.Total != 0 {
		t.Fatalf("expected a sibling app to see zero messages, got %+v", otherAppMessages)
	}

	// A different org's dashboard token can't even reach this app's stat.
	otherOrgToken, _, _ := signUpDashboardOrg(t, app, "Other Messages Org")
	rec = do(t, app, authed(jsonRequest("GET", appMessagesPath, nil), otherOrgToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org access: status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDashboardAppPolls_ScopedToOwnApp proves the app Dashboard tab's polls
// panel only ever shows polls created in ITS OWN channels — not a sibling
// app in the same org, and not another org entirely.
func TestDashboardAppPolls_ScopedToOwnApp(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Polls Org")
	appID, key, secret := createTestApp(t, app, orgID, ownerToken)

	var user createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "Poller", Region: "eu"}), appAccessToken(t, app, key, secret)), &user)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create test user: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	channel := createTestChannel(t, app, user.Token, "polls-general")

	var created pollResponse
	rec = do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Best language?", []string{"Go", "Rust"}, false, nil)), user.Token), &created)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create poll: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	appPollsPath := fmt.Sprintf("/dashboard/apps/%d/polls", appID)
	var listed []dashboardPollResponse
	rec = do(t, app, authed(jsonRequest("GET", appPollsPath, nil), ownerToken), &listed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(listed) != 1 || listed[0].PollID != created.PollID {
		t.Fatalf("expected exactly the one poll just created, got %+v", listed)
	}
	if listed[0].AppID != appID || listed[0].ChannelName != "polls-general" {
		t.Fatalf("unexpected poll metadata: %+v", listed[0])
	}

	// A second app in the SAME org must see none of the first app's polls.
	otherAppID, _, _ := createTestApp(t, app, orgID, ownerToken)
	var otherAppPolls []dashboardPollResponse
	do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/apps/%d/polls", otherAppID), nil), ownerToken), &otherAppPolls)
	if len(otherAppPolls) != 0 {
		t.Fatalf("expected a sibling app to see zero polls, got %+v", otherAppPolls)
	}

	// A different org's dashboard token can't even reach this app's polls.
	otherOrgToken, _, _ := signUpDashboardOrg(t, app, "Other Polls Org")
	rec = do(t, app, authed(jsonRequest("GET", appPollsPath, nil), otherOrgToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org access: status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}
