package main

import (
	"fmt"
	"net/http"
	"testing"
)

func TestDashboardEndUsers_CreateAndList(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "End Users Org")
	appID, _, _ := createTestApp(t, app, orgID, ownerToken)

	var created dashboardEndUserResponse
	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/users", appID), dashboardCreateEndUserRequest{
		DisplayName: "Ada Lovelace", Region: "eu",
	}), ownerToken), &created)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create end user: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if created.DisplayName != "Ada Lovelace" || created.Region != "eu" || created.UserID == "" {
		t.Fatalf("unexpected create response: %+v", created)
	}

	var list []dashboardEndUserResponse
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/apps/%d/users", appID), nil), ownerToken), &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].UserID != created.UserID {
		t.Fatalf("list end users: status = %d, list = %+v", rec.Code, list)
	}

	// Invalid region rejected.
	rec = do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/users", appID), dashboardCreateEndUserRequest{
		DisplayName: "Bad Region", Region: "mars",
	}), ownerToken), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid region: status = %d, want 400", rec.Code)
	}
}

func TestDashboardEndUsers_CrossOrgIsolation(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Owner Org")
	appID, _, _ := createTestApp(t, app, orgID, ownerToken)

	otherToken, _, _ := signUpDashboardOrg(t, app, "Other Org")
	rec := do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/apps/%d/users", appID), nil), otherToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org list end users: status = %d, want 403", rec.Code)
	}
	rec = do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/users", appID), dashboardCreateEndUserRequest{
		DisplayName: "Intruder", Region: "eu",
	}), otherToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org create end user: status = %d, want 403", rec.Code)
	}
}

// createDashboardEndUser is a fixture: mints one end-user in appID via the
// dashboard's own create-user route and returns its id.
func createDashboardEndUser(t *testing.T, app *App, ownerToken string, appID int64, name string) string {
	t.Helper()
	var u dashboardEndUserResponse
	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/users", appID), dashboardCreateEndUserRequest{
		DisplayName: name, Region: "eu",
	}), ownerToken), &u)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create dashboard end user %q: status = %d, body = %s", name, rec.Code, rec.Body.String())
	}
	return u.UserID
}

func TestDashboardChannels_CreateListAndMembers(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Channels Org")
	// FREE (the only tier self-serve signup can produce) allows one app per
	// org; this test needs a second app in the SAME org below to exercise
	// the same-org cross-app rejection, so bump it past that limit.
	bumpOrgTierForTest(t, app, orgID, "PRO")
	appID, _, _ := createTestApp(t, app, orgID, ownerToken)

	creatorID := createDashboardEndUser(t, app, ownerToken, appID, "Creator")
	memberID := createDashboardEndUser(t, app, ownerToken, appID, "Member")

	var ch dashboardChannelResponse
	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/channels", appID), dashboardCreateChannelRequest{
		Name: "general", CreatorUserID: creatorID,
	}), ownerToken), &ch)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create channel: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ch.Name != "general" || ch.CreatorName != "Creator" || ch.MemberCount != 1 {
		t.Fatalf("unexpected create channel response: %+v", ch)
	}

	var list []dashboardChannelResponse
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/apps/%d/channels", appID), nil), ownerToken), &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].ChannelID != ch.ChannelID {
		t.Fatalf("list channels: status = %d, list = %+v", rec.Code, list)
	}

	// A creator from a different app is rejected.
	otherAppID, _, _ := createTestApp(t, app, orgID, ownerToken)
	otherAppUserID := createDashboardEndUser(t, app, ownerToken, otherAppID, "Wrong App User")
	rec = do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/channels", appID), dashboardCreateChannelRequest{
		Name: "leak", CreatorUserID: otherAppUserID,
	}), ownerToken), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-app creator: status = %d, want 400", rec.Code)
	}

	// Add a member from the same app.
	var addResp memberResponse
	rec = do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/channels/%s/members", ch.ChannelID), addMemberRequest{
		UserID: memberID,
	}), ownerToken), &addResp)
	if rec.Code != http.StatusCreated || addResp.DisplayName != "Member" {
		t.Fatalf("add member: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var members []memberResponse
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/channels/%s/members", ch.ChannelID), nil), ownerToken), &members)
	if rec.Code != http.StatusOK || len(members) != 2 {
		t.Fatalf("list members after add: status = %d, members = %+v", rec.Code, members)
	}

	// Adding a member from a different app is rejected.
	rec = do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/channels/%s/members", ch.ChannelID), addMemberRequest{
		UserID: otherAppUserID,
	}), ownerToken), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-app member add: status = %d, want 400", rec.Code)
	}

	// Remove the member.
	rec = do(t, app, authed(jsonRequest("DELETE", fmt.Sprintf("/dashboard/channels/%s/members/%s", ch.ChannelID, memberID), nil), ownerToken), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove member: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/channels/%s/members", ch.ChannelID), nil), ownerToken), &members)
	if rec.Code != http.StatusOK || len(members) != 1 || members[0].UserID != creatorID {
		t.Fatalf("list members after remove: status = %d, members = %+v", rec.Code, members)
	}
}

func TestDashboardChannels_CrossOrgIsolation(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Channel Owner Org")
	appID, _, _ := createTestApp(t, app, orgID, ownerToken)
	creatorID := createDashboardEndUser(t, app, ownerToken, appID, "Creator")

	var ch dashboardChannelResponse
	do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/channels", appID), dashboardCreateChannelRequest{
		Name: "private", CreatorUserID: creatorID,
	}), ownerToken), &ch)

	otherToken, _, _ := signUpDashboardOrg(t, app, "Channel Other Org")
	rec := do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/channels/%s/members", ch.ChannelID), nil), otherToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org list members: status = %d, want 403", rec.Code)
	}
	rec = do(t, app, authed(jsonRequest("DELETE", fmt.Sprintf("/dashboard/channels/%s/members/%s", ch.ChannelID, creatorID), nil), otherToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org remove member: status = %d, want 403", rec.Code)
	}
}
