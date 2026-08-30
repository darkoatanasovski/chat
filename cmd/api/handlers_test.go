package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func testApp(t *testing.T) *App {
	t.Helper()
	testAppOnce.Do(func() {
		sharedApp, testAppErr = buildTestApp()
	})
	if testAppErr != nil {
		t.Skipf("dev stack not reachable on host ports (start it: make up): %v", testAppErr)
	}
	return sharedApp
}

// do sends req through the real handler stack (auth, instrumentation, CORS)
// exactly as the live server would, and decodes a JSON response into out
// (if out is non-nil).
func do(t *testing.T, app *App, req *http.Request, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, req)
	if out != nil && rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
		}
	}
	return rec
}

func jsonRequest(method, path string, body any) *http.Request {
	var r *http.Request
	if body != nil {
		data, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(data))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

func authed(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func basicAuthed(req *http.Request, key, secret string) *http.Request {
	req.SetBasicAuth(key, secret)
	return req
}

// appAccessToken drives POST /apps/token end to end — the key+secret ->
// short-lived Bearer JWT exchange every server-scoped call now goes
// through instead of sending Basic auth directly (see requireAppJWT) —
// and returns the token.
func appAccessToken(t *testing.T, app *App, key, secret string) string {
	t.Helper()
	var resp appTokenResponse
	rec := do(t, app, basicAuthed(jsonRequest("POST", "/apps/token", nil), key, secret), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("exchange app token: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return resp.Token
}

// createTestOrg drives POST /organizations end to end and returns the new
// org's id and org-admin bearer token.
func createTestOrg(t *testing.T, app *App, tier string) (int64, string) {
	t.Helper()
	var resp orgResponse
	rec := do(t, app, jsonRequest("POST", "/organizations", createOrgRequest{Name: "test-org-" + uuid.NewString(), Tier: tier}), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create test org: status %d, body %s", rec.Code, rec.Body.String())
	}
	return resp.OrgID, resp.Token
}

// createTestApp drives POST /organizations/{org_id}/apps end to end and
// returns the new app's id and its first (only) credential.
func createTestApp(t *testing.T, app *App, orgID int64, orgToken string) (int64, string, string) {
	t.Helper()
	var resp createAppResponse
	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/organizations/%d/apps", orgID), createAppRequest{Name: "test-app-" + uuid.NewString()}), orgToken), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create test app: status %d, body %s", rec.Code, rec.Body.String())
	}
	return resp.AppID, resp.Credential.Key, resp.Credential.Secret
}

// createOrgAndApp is createTestOrg + createTestApp combined, for tests that
// only care about the resulting app credentials.
func createOrgAndApp(t *testing.T, app *App, tier string) (appID int64, key, secret string) {
	t.Helper()
	orgID, orgToken := createTestOrg(t, app, tier)
	return createTestApp(t, app, orgID, orgToken)
}

// defaultAppCredentials returns one FREE-tier app's credentials, created
// once per test binary and shared by every createTestUser call — this
// mirrors how these handler tests worked before app-scoping existed
// (every test user implicitly shared one namespace), which several existing
// tests rely on (e.g. adding one createTestUser's id as a member of
// another's channel).
var (
	defaultAppOnce   sync.Once
	defaultAppKey    string
	defaultAppSecret string
)

func defaultAppCredentials(t *testing.T, app *App) (string, string) {
	t.Helper()
	defaultAppOnce.Do(func() {
		_, defaultAppKey, defaultAppSecret = createOrgAndApp(t, app, "FREE")
	})
	return defaultAppKey, defaultAppSecret
}

// createTestUser drives POST /users end to end (authenticating with a
// shared default FREE-tier app's credentials) and returns the new user's id
// and bearer token, for use as a fixture in other handler tests.
func createTestUser(t *testing.T, app *App, displayName string) (uuid.UUID, string) {
	t.Helper()
	key, secret := defaultAppCredentials(t, app)
	appToken := appAccessToken(t, app, key, secret)
	var resp createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: displayName, Region: "eu"}), appToken), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create test user %q: status %d, body %s", displayName, rec.Code, rec.Body.String())
	}
	id, err := uuid.Parse(resp.UserID)
	if err != nil {
		t.Fatalf("parse user id %q: %v", resp.UserID, err)
	}
	return id, resp.Token
}

func createTestChannel(t *testing.T, app *App, token, name string) channelResponse {
	t.Helper()
	var resp channelResponse
	rec := do(t, app, authed(jsonRequest("POST", "/channels", createChannelRequest{Name: name}), token), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create test channel %q: status %d, body %s", name, rec.Code, rec.Body.String())
	}
	return resp
}

// --- POST /users ---

func TestHandleCreateUser_Valid(t *testing.T) {
	app := testApp(t)
	key, secret := defaultAppCredentials(t, app)
	appToken := appAccessToken(t, app, key, secret)
	var resp createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "alice", Region: "eu"}), appToken), &resp)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.Tier != "FREE" {
		t.Fatalf("expected the default app's FREE-tier org to resolve to FREE, got %q", resp.Tier)
	}
	if resp.Token == "" {
		t.Fatalf("expected a non-empty token")
	}
	if _, err := uuid.Parse(resp.UserID); err != nil {
		t.Fatalf("user_id %q is not a valid uuid: %v", resp.UserID, err)
	}
}

// TestHandleCreateUser_TierIsResolvedFromOwningOrg proves tier is never
// chosen per-user: it's resolved live from app_id -> org.tier, so a user
// created under a PRO org's app reports PRO with no tier field on the
// request at all.
func TestHandleCreateUser_TierIsResolvedFromOwningOrg(t *testing.T) {
	app := testApp(t)
	for _, tier := range []string{"FREE", "PRO", "BUSINESS", "ENTERPRISE"} {
		t.Run(tier, func(t *testing.T) {
			_, key, secret := createOrgAndApp(t, app, tier)
			appToken := appAccessToken(t, app, key, secret)
			var resp createUserResponse
			rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "tier-" + tier, Region: "eu"}), appToken), &resp)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if resp.Tier != tier {
				t.Fatalf("expected tier %q from owning org, got %q", tier, resp.Tier)
			}
		})
	}
}

func TestHandleCreateUser_Validation(t *testing.T) {
	app := testApp(t)
	key, secret := defaultAppCredentials(t, app)
	appToken := appAccessToken(t, app, key, secret)
	cases := []struct {
		name string
		req  createUserRequest
	}{
		{"empty display_name", createUserRequest{DisplayName: "  ", Region: "eu"}},
		{"display_name too long", createUserRequest{DisplayName: string(make([]byte, 129)), Region: "eu"}},
		{"invalid region", createUserRequest{DisplayName: "bob", Region: "mars"}},
		{"missing region", createUserRequest{DisplayName: "bob"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, app, authed(jsonRequest("POST", "/users", tc.req), appToken), nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleCreateUser_RejectsUnknownFields(t *testing.T) {
	app := testApp(t)
	key, secret := defaultAppCredentials(t, app)
	appToken := appAccessToken(t, app, key, secret)
	req := httptest.NewRequest("POST", "/users", bytes.NewReader([]byte(`{"display_name":"carol","region":"eu","is_admin":true}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := do(t, app, authed(req, appToken), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown fields to be rejected, status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// --- POST /apps/token and POST /users auth boundaries ---

// TestHandleCreateAppToken_RequiresAppCredentials covers the boundary that
// used to sit directly on POST /users: since requireAppJWT moved there
// instead, this is now the one endpoint that still checks key+secret.
func TestHandleCreateAppToken_RequiresAppCredentials(t *testing.T) {
	app := testApp(t)

	rec := do(t, app, jsonRequest("POST", "/apps/token", nil), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing credentials: status = %d, want 401", rec.Code)
	}

	rec = do(t, app, basicAuthed(jsonRequest("POST", "/apps/token", nil), "key_bogus", "secret_bogus"), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid credentials: status = %d, want 401", rec.Code)
	}
}

func TestHandleCreateUser_RequiresAppCredentials(t *testing.T) {
	app := testApp(t)

	rec := do(t, app, jsonRequest("POST", "/users", createUserRequest{DisplayName: "no-creds", Region: "eu"}), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: status = %d, want 401", rec.Code)
	}

	rec = do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "bad-token", Region: "eu"}), "not-a-real-token"), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token: status = %d, want 401", rec.Code)
	}

	// Basic auth (the old scheme) must no longer work directly against
	// /users — it only mints a token now, at POST /apps/token.
	key, secret := defaultAppCredentials(t, app)
	rec = do(t, app, basicAuthed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "basic-not-accepted", Region: "eu"}), key, secret), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("basic auth directly on /users: status = %d, want 401", rec.Code)
	}
}

// TestHandleCreateUser_RevokedCredentialRejectedImmediately is the whole
// point of live credential verification instead of a signed token: a
// revoked credential must stop working on its very next use, not after some
// expiry window.
func TestHandleCreateUser_RevokedCredentialRejectedImmediately(t *testing.T) {
	app := testApp(t)
	orgID, orgToken := createTestOrg(t, app, "FREE")
	appID, key, secret := createTestApp(t, app, orgID, orgToken)
	appToken := appAccessToken(t, app, key, secret)

	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "before-revoke", Region: "eu"}), appToken), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("before revoke: status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}

	var creds []credentialResponse
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/apps/%d/credentials", appID), nil), orgToken), &creds)
	if rec.Code != http.StatusOK || len(creds) != 1 {
		t.Fatalf("list credentials: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("DELETE", fmt.Sprintf("/apps/%d/credentials/%s", appID, creds[0].CredentialID), nil), orgToken), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke credential: status = %d, want 204, body = %s", rec.Code, rec.Body.String())
	}

	// The already-issued, still-unexpired app token must stop working
	// immediately — this is the whole point of requireAppJWT's live
	// IsActive check (a bare signed JWT couldn't give this guarantee on
	// its own; see internal/apps.CredentialRepo.IsActive).
	rec = do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "after-revoke-cached-token", Region: "eu"}), appToken), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("after revoke, cached app token: status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}

	// And the revoked key+secret can no longer mint a fresh token either.
	rec = do(t, app, basicAuthed(jsonRequest("POST", "/apps/token", nil), key, secret), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("after revoke, re-exchange: status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
}

// --- auth ---

func TestRequireAuth_RejectsMissingAndInvalidTokens(t *testing.T) {
	app := testApp(t)

	rec := do(t, app, jsonRequest("POST", "/channels", createChannelRequest{Name: "no-auth"}), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: status = %d, want 401", rec.Code)
	}

	rec = do(t, app, authed(jsonRequest("POST", "/channels", createChannelRequest{Name: "bad-auth"}), "not-a-real-token"), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token: status = %d, want 401", rec.Code)
	}
}

// --- POST /channels ---

func TestHandleCreateChannel_ValidAndQuotaEnforced(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "channel-owner")

	first := createTestChannel(t, app, token, "general")
	if first.HomeRegion != "eu" {
		t.Fatalf("expected home_region eu, got %q", first.HomeRegion)
	}

	// FREE tier's max_channels is 1 (deploy/tiers.yaml) — a second channel
	// from the same user must be rejected, not silently created.
	rec := do(t, app, authed(jsonRequest("POST", "/channels", createChannelRequest{Name: "second"}), token), nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 once max_channels is reached, status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// --- POST /channels/{id}/members ---

func TestHandleAddMember_QuotaEnforced(t *testing.T) {
	app := testApp(t)
	_, ownerToken := createTestUser(t, app, "member-quota-owner")
	channel := createTestChannel(t, app, ownerToken, "quota-test")

	// FREE tier's max_channel_members is 3 (deploy/tiers.yaml): the owner
	// counts as 1, so exactly 2 more may be added before the 3rd is denied.
	var lastStatus int
	for i := range 3 {
		memberID, _ := createTestUser(t, app, fmt.Sprintf("member-%d", i))
		req := authed(jsonRequest("POST", fmt.Sprintf("/channels/%s/members", channel.ChannelID), addMemberRequest{UserID: memberID.String()}), ownerToken)
		rec := do(t, app, req, nil)
		lastStatus = rec.Code
		if i < 2 && rec.Code != http.StatusCreated {
			t.Fatalf("member %d: expected 201 within quota, status = %d, body = %s", i, rec.Code, rec.Body.String())
		}
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("expected the member that exceeds max_channel_members to be rejected with 429, got %d", lastStatus)
	}
}

func TestHandleAddMember_RequiresExistingMembership(t *testing.T) {
	app := testApp(t)
	_, ownerToken := createTestUser(t, app, "add-member-owner")
	channel := createTestChannel(t, app, ownerToken, "not-your-channel")
	_, outsiderToken := createTestUser(t, app, "outsider")
	newMemberID, _ := createTestUser(t, app, "new-member")

	req := authed(jsonRequest("POST", fmt.Sprintf("/channels/%s/members", channel.ChannelID), addMemberRequest{UserID: newMemberID.String()}), outsiderToken)
	rec := do(t, app, req, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected a non-member adding a member to be forbidden, status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListMembers(t *testing.T) {
	app := testApp(t)
	ownerID, ownerToken := createTestUser(t, app, "list-members-owner")
	channel := createTestChannel(t, app, ownerToken, "list-members-test")
	memberID, _ := createTestUser(t, app, "list-members-added")

	addReq := authed(jsonRequest("POST", fmt.Sprintf("/channels/%s/members", channel.ChannelID), addMemberRequest{UserID: memberID.String()}), ownerToken)
	if rec := do(t, app, addReq, nil); rec.Code != http.StatusCreated {
		t.Fatalf("add member: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var members []memberResponse
	rec := do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/members", nil), ownerToken), &members)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(members) != 2 {
		t.Fatalf("expected owner + added member = 2, got %d: %+v", len(members), members)
	}
	byID := map[string]string{}
	for _, m := range members {
		byID[m.UserID] = m.DisplayName
	}
	if byID[ownerID.String()] != "list-members-owner" {
		t.Fatalf("expected owner's display name in the list, got %+v", byID)
	}
	if byID[memberID.String()] != "list-members-added" {
		t.Fatalf("expected added member's display name in the list, got %+v", byID)
	}
}

func TestHandleListMembers_RequiresMembership(t *testing.T) {
	app := testApp(t)
	_, ownerToken := createTestUser(t, app, "list-members-owner-2")
	channel := createTestChannel(t, app, ownerToken, "list-members-membership-test")
	_, outsiderToken := createTestUser(t, app, "list-members-outsider")

	rec := do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/members", nil), outsiderToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// --- POST /channels/{id}/messages ---

func TestHandleSendMessage_Idempotency(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "idempotency-sender")
	channel := createTestChannel(t, app, token, "idempotency-test")
	clientMessageID := uuid.NewString()

	var first, second messageResponse
	body := sendMessageRequest{ClientMessageID: clientMessageID, Body: "hello, retried"}

	rec1 := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", body), token), &first)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first send: status = %d, body = %s", rec1.Code, rec1.Body.String())
	}

	rec2 := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", body), token), &second)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("retried send: status = %d, body = %s", rec2.Code, rec2.Body.String())
	}

	if first.MessageID != second.MessageID {
		t.Fatalf("expected a retry with the same client_message_id to return the SAME message_id, got %q then %q", first.MessageID, second.MessageID)
	}
	if first.Sequence != second.Sequence {
		t.Fatalf("expected the same sequence on retry, got %d then %d", first.Sequence, second.Sequence)
	}
}

func TestHandleSendMessage_DifferentClientMessageIDsCreateDistinctMessages(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "distinct-sender")
	channel := createTestChannel(t, app, token, "distinct-test")

	var first, second messageResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "one"}), token), &first)
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "two"}), token), &second)

	if first.MessageID == second.MessageID {
		t.Fatalf("two sends with different client_message_ids must not collapse into one message")
	}
	if second.Sequence != first.Sequence+1 {
		t.Fatalf("expected sequence to increment (%d then %d)", first.Sequence, second.Sequence)
	}
}

func TestHandleSendMessage_Validation(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "validation-sender")
	channel := createTestChannel(t, app, token, "validation-test")

	t.Run("not a member", func(t *testing.T) {
		_, outsiderToken := createTestUser(t, app, "validation-outsider")
		rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "hi"}), outsiderToken), nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("invalid channel id", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", "/channels/not-a-uuid/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "hi"}), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("invalid client_message_id", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: "not-a-uuid", Body: "hi"}), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: ""}), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("body too long", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: string(make([]byte, 4001))}), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("channel not found", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", "/channels/"+uuid.NewString()+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "hi"}), token), nil)
		// Membership is checked before existence, so a random channel id an
		// unrelated user was never added to correctly reads as 403, not 404.
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

// --- GET /channels/{id}/messages (cursor pagination) ---

func TestHandleListMessages_CursorPagination(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "pagination-sender")
	channel := createTestChannel(t, app, token, "pagination-test")

	const total = 5
	var sequences []int64
	for i := range total {
		var resp messageResponse
		do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{
			ClientMessageID: uuid.NewString(), Body: fmt.Sprintf("message %d", i),
		}), token), &resp)
		sequences = append(sequences, resp.Sequence)
	}

	// Page 1: newest 2.
	var page1 []messageResponse
	rec := do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages?limit=2", nil), token), &page1)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(page1) != 2 {
		t.Fatalf("expected 2 messages on page 1, got %d", len(page1))
	}
	if page1[0].Sequence != sequences[total-1] || page1[1].Sequence != sequences[total-2] {
		t.Fatalf("expected page 1 newest-first as [%d,%d], got [%d,%d]", sequences[total-1], sequences[total-2], page1[0].Sequence, page1[1].Sequence)
	}

	// Page 2: cursor from the oldest item on page 1, next 2 older.
	var page2 []messageResponse
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/channels/%s/messages?limit=2&before=%d", channel.ChannelID, page1[1].Sequence), nil), token), &page2)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(page2) != 2 {
		t.Fatalf("expected 2 messages on page 2, got %d", len(page2))
	}
	if page2[0].Sequence != sequences[total-3] || page2[1].Sequence != sequences[total-4] {
		t.Fatalf("expected page 2 as [%d,%d], got [%d,%d]", sequences[total-3], sequences[total-4], page2[0].Sequence, page2[1].Sequence)
	}

	// No page overlaps with any other, and no message ID repeats across pages.
	seen := map[string]bool{}
	for _, m := range append(page1, page2...) {
		if seen[m.MessageID] {
			t.Fatalf("message %s appeared on more than one page", m.MessageID)
		}
		seen[m.MessageID] = true
	}
}

func TestHandleListMessages_RequiresMembership(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "list-owner")
	channel := createTestChannel(t, app, token, "list-membership-test")
	_, outsiderToken := createTestUser(t, app, "list-outsider")

	rec := do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages", nil), outsiderToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
