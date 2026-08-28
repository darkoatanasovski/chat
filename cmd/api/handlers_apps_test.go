package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestHandleCreateApp_ValidAndFirstCredentialIssued(t *testing.T) {
	app := testApp(t)
	orgID, orgToken := createTestOrg(t, app, "PRO")

	var resp createAppResponse
	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/organizations/%d/apps", orgID), createAppRequest{Name: "my-app"}), orgToken), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.AppID == 0 {
		t.Fatalf("expected a non-zero numeric app_id")
	}
	if resp.Credential.Key == "" || resp.Credential.Secret == "" {
		t.Fatalf("expected the app's first credential to be issued immediately, got %+v", resp.Credential)
	}
}

// TestHandleCreateApp_MaxAppsEnforced exercises the org-level max_apps quota
// (deploy/tiers.yaml: FREE = 1) — the same AllowResource mechanism
// max_channels already uses, just keyed by organization instead of user.
func TestHandleCreateApp_MaxAppsEnforced(t *testing.T) {
	app := testApp(t)
	orgID, orgToken := createTestOrg(t, app, "FREE")

	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/organizations/%d/apps", orgID), createAppRequest{Name: "first-app"}), orgToken), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first app: status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/organizations/%d/apps", orgID), createAppRequest{Name: "second-app"}), orgToken), nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second app on FREE tier: status = %d, want 429, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListApps(t *testing.T) {
	app := testApp(t)
	orgID, orgToken := createTestOrg(t, app, "BUSINESS")
	createTestApp(t, app, orgID, orgToken)
	createTestApp(t, app, orgID, orgToken)

	var list []appResponse
	rec := do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/organizations/%d/apps", orgID), nil), orgToken), &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(list))
	}
}

// TestHandleCreateApp_RejectsOtherOrgsToken proves an org-admin token only
// authorizes actions against its own org, even when the path names a
// different (valid) org_id — otherwise any org could act on any other org's
// apps just by editing the URL.
func TestHandleCreateApp_RejectsOtherOrgsToken(t *testing.T) {
	app := testApp(t)
	victimOrgID, _ := createTestOrg(t, app, "FREE")
	_, attackerToken := createTestOrg(t, app, "FREE")

	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/organizations/%d/apps", victimOrgID), createAppRequest{Name: "hijack-app"}), attackerToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAppCredentials_CreateListRevoke(t *testing.T) {
	app := testApp(t)
	orgID, orgToken := createTestOrg(t, app, "PRO")
	appID, _, _ := createTestApp(t, app, orgID, orgToken)

	var issued credentialResponse
	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/apps/%d/credentials", appID), nil), orgToken), &issued)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create credential: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if issued.Secret == "" {
		t.Fatalf("expected the new credential's secret to be returned once at creation")
	}

	var list []credentialResponse
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/apps/%d/credentials", appID), nil), orgToken), &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("list credentials: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The app's first (auto-issued at creation) credential plus the one just
	// issued above = 2, and neither entry ever carries a secret.
	if len(list) != 2 {
		t.Fatalf("expected 2 credentials, got %d", len(list))
	}
	for _, c := range list {
		if c.Secret != "" {
			t.Fatalf("expected ListByApp to never return a secret, got one on credential %s", c.CredentialID)
		}
	}

	rec = do(t, app, authed(jsonRequest("DELETE", fmt.Sprintf("/apps/%d/credentials/%s", appID, issued.CredentialID), nil), orgToken), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke credential: status = %d, want 204, body = %s", rec.Code, rec.Body.String())
	}

	// Revoking the same credential again must not silently succeed a second
	// time (it's no longer an active, revocable credential).
	rec = do(t, app, authed(jsonRequest("DELETE", fmt.Sprintf("/apps/%d/credentials/%s", appID, issued.CredentialID), nil), orgToken), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("re-revoke: status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAppCredentials_RejectsOtherOrgsApp proves owning *an* org isn't
// enough to manage *any* app's credentials — the app must belong to the
// caller's own org.
func TestHandleAppCredentials_RejectsOtherOrgsApp(t *testing.T) {
	app := testApp(t)
	victimOrgID, victimOrgToken := createTestOrg(t, app, "FREE")
	victimAppID, _, _ := createTestApp(t, app, victimOrgID, victimOrgToken)
	_, attackerToken := createTestOrg(t, app, "FREE")

	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/apps/%d/credentials", victimAppID), nil), attackerToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

// TestCrossAppIsolation proves an end-user created under one App can't
// reach a channel that belongs to a different App, even with a guessed or
// leaked channel_id — the isolation guarantee the whole org->app model
// exists to provide.
func TestCrossAppIsolation(t *testing.T) {
	app := testApp(t)

	_, keyA, secretA := createOrgAndApp(t, app, "FREE")
	var ownerResp createUserResponse
	rec := do(t, app, basicAuthed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "app-a-owner", Region: "eu"}), keyA, secretA), &ownerResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app-a owner: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	channel := createTestChannel(t, app, ownerResp.Token, "app-a-channel")

	_, keyB, secretB := createOrgAndApp(t, app, "FREE")
	var outsiderResp createUserResponse
	rec = do(t, app, basicAuthed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "app-b-outsider", Region: "eu"}), keyB, secretB), &outsiderResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app-b outsider: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	addReq := authed(jsonRequest("POST", fmt.Sprintf("/channels/%s/members", channel.ChannelID), addMemberRequest{UserID: outsiderResp.UserID}), outsiderResp.Token)
	rec = do(t, app, addReq, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("add member across apps: status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}

	sendReq := authed(jsonRequest("POST", fmt.Sprintf("/channels/%s/messages", channel.ChannelID), sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "hi"}), outsiderResp.Token)
	rec = do(t, app, sendReq, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("send message across apps: status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}

	listReq := authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/members", nil), outsiderResp.Token)
	rec = do(t, app, listReq, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("list members across apps: status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}
