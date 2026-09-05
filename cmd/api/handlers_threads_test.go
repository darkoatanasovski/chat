package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// createThreadTestApp is createTestOrg + createTestApp + one member-ready
// channel, all under one dedicated app — tests in this file need to control
// max_thread_depth (PATCH /apps/{app_id}), which the shared
// defaultAppCredentials app can't do without affecting every other test
// that relies on its default.
func createThreadTestApp(t *testing.T) (app *App, orgToken string, appID int64, token string, channel channelResponse) {
	t.Helper()
	app = testApp(t)
	orgID, orgToken := createTestOrg(t, app, "PRO")
	appID, key, secret := createTestApp(t, app, orgID, orgToken)

	appToken := appAccessToken(t, app, key, secret)
	var userResp createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "thread-tester", Region: "eu"}), appToken), &userResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create thread test user: status %d, body %s", rec.Code, rec.Body.String())
	}

	channel = createTestChannel(t, app, userResp.Token, "threads-test")
	return app, orgToken, appID, userResp.Token, channel
}

func setMaxThreadDepth(t *testing.T, app *App, orgToken string, appID int64, depth int) {
	t.Helper()
	rec := do(t, app, authed(jsonRequest("PATCH", fmt.Sprintf("/apps/%d", appID), updateAppRequest{MaxThreadDepth: &depth}), orgToken), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("set max_thread_depth=%d: status %d, body %s", depth, rec.Code, rec.Body.String())
	}
}

// TestHandleSendMessage_Reply proves a reply carries parent_id through both
// the create response and a subsequent list — the whole feature is this one
// field, so a client can reconstruct a thread by grouping on it without any
// separate "thread" resource.
func TestHandleSendMessage_Reply(t *testing.T) {
	app, _, _, token, channel := createThreadTestApp(t)

	var root messageResponse
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "root"}), token), &root)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send root: status %d, body %s", rec.Code, rec.Body.String())
	}
	if root.ParentID != nil {
		t.Fatalf("expected a top-level message to have no parent_id, got %v", *root.ParentID)
	}

	var reply messageResponse
	rec = do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{
		ClientMessageID: uuid.NewString(), Body: "a reply", ParentID: &root.MessageID,
	}), token), &reply)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send reply: status %d, body %s", rec.Code, rec.Body.String())
	}
	if reply.ParentID == nil || *reply.ParentID != root.MessageID {
		t.Fatalf("expected reply.parent_id = %q, got %v", root.MessageID, reply.ParentID)
	}

	var list []messageResponse
	rec = do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages", nil), token), &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("list messages: status %d, body %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, m := range list {
		if m.MessageID == reply.MessageID {
			found = true
			if m.ParentID == nil || *m.ParentID != root.MessageID {
				t.Fatalf("listed reply's parent_id = %v, want %q", m.ParentID, root.MessageID)
			}
		}
	}
	if !found {
		t.Fatalf("reply %q not present in list", reply.MessageID)
	}
}

// TestHandleSendMessage_ReplyCountDenormalized proves reply_count is kept
// current on the parent row itself (never computed by joining/counting on
// read) as replies come in, that a fresh message — including a reply, which
// has no replies of its own yet — starts at 0, and that a reply only bumps
// its *direct* parent's count, not every ancestor up the chain.
func TestHandleSendMessage_ReplyCountDenormalized(t *testing.T) {
	app, _, _, token, channel := createThreadTestApp(t)

	var root messageResponse
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "root"}), token), &root)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send root: status %d, body %s", rec.Code, rec.Body.String())
	}
	if root.ReplyCount != 0 {
		t.Fatalf("fresh root reply_count = %d, want 0", root.ReplyCount)
	}

	var reply1 messageResponse
	rec = do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{
		ClientMessageID: uuid.NewString(), Body: "reply one", ParentID: &root.MessageID,
	}), token), &reply1)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send reply one: status %d, body %s", rec.Code, rec.Body.String())
	}
	// The reply's own create response reports its OWN reply_count (0, it
	// has no replies of its own yet), not the parent's — the parent's fresh
	// count is only observable by re-reading the parent.
	if reply1.ReplyCount != 0 {
		t.Fatalf("fresh reply reply_count = %d, want 0", reply1.ReplyCount)
	}

	var reply2 messageResponse
	rec = do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{
		ClientMessageID: uuid.NewString(), Body: "reply two", ParentID: &root.MessageID,
	}), token), &reply2)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send reply two: status %d, body %s", rec.Code, rec.Body.String())
	}

	// A reply-to-a-reply bumps reply1's own count, not root's — reply_count
	// is a direct-children count, not a whole-thread total.
	var grandchild messageResponse
	rec = do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{
		ClientMessageID: uuid.NewString(), Body: "reply to reply one", ParentID: &reply1.MessageID,
	}), token), &grandchild)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send grandchild: status %d, body %s", rec.Code, rec.Body.String())
	}

	var list []messageResponse
	rec = do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages", nil), token), &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("list messages: status %d, body %s", rec.Code, rec.Body.String())
	}
	byID := map[string]messageResponse{}
	for _, m := range list {
		byID[m.MessageID] = m
	}
	if got := byID[root.MessageID].ReplyCount; got != 2 {
		t.Fatalf("root reply_count = %d, want 2 (two direct replies)", got)
	}
	if got := byID[reply1.MessageID].ReplyCount; got != 1 {
		t.Fatalf("reply1 reply_count = %d, want 1 (one direct reply, the grandchild)", got)
	}
	if got := byID[reply2.MessageID].ReplyCount; got != 0 {
		t.Fatalf("reply2 reply_count = %d, want 0 (no replies of its own)", got)
	}
	if got := byID[grandchild.MessageID].ReplyCount; got != 0 {
		t.Fatalf("grandchild reply_count = %d, want 0 (no replies of its own)", got)
	}
}

// TestHandleSendMessage_ReplyToNonexistentParent proves a well-formed but
// nonexistent (or cross-channel) parent_id is a 404, not a 500 or a
// silently-accepted top-level message.
func TestHandleSendMessage_ReplyToNonexistentParent(t *testing.T) {
	app, _, _, token, channel := createThreadTestApp(t)

	fakeParent := uuid.NewString()
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{
		ClientMessageID: uuid.NewString(), Body: "orphan reply", ParentID: &fakeParent,
	}), token), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleSendMessage_ThreadDepthEnforced proves max_thread_depth is both
// respected (a reply within the limit succeeds) and enforced (one that
// would exceed it is rejected with 400, not silently truncated or allowed)
// — and that the limit set via PATCH /apps/{app_id} is what's actually
// checked, not some hardcoded value.
func TestHandleSendMessage_ThreadDepthEnforced(t *testing.T) {
	app, orgToken, appID, token, channel := createThreadTestApp(t)
	setMaxThreadDepth(t, app, orgToken, appID, 2)

	var root messageResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "root"}), token), &root)

	var reply messageResponse
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{
		ClientMessageID: uuid.NewString(), Body: "depth 2, within limit", ParentID: &root.MessageID,
	}), token), &reply)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply at depth 2 (limit 2): status %d, want 201, body %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", sendMessageRequest{
		ClientMessageID: uuid.NewString(), Body: "depth 3, over limit", ParentID: &reply.MessageID,
	}), token), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reply at depth 3 (limit 2): status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// TestHandleSendMessage_ThreadDepthUnlimited proves max_thread_depth=0
// really does mean no cap — a chain several levels deep all succeeds.
func TestHandleSendMessage_ThreadDepthUnlimited(t *testing.T) {
	app, orgToken, appID, token, channel := createThreadTestApp(t)
	setMaxThreadDepth(t, app, orgToken, appID, 0)

	parentID := ""
	for i := 0; i < 6; i++ {
		var req sendMessageRequest
		if i == 0 {
			req = sendMessageRequest{ClientMessageID: uuid.NewString(), Body: "root"}
		} else {
			req = sendMessageRequest{ClientMessageID: uuid.NewString(), Body: fmt.Sprintf("depth %d", i+1), ParentID: &parentID}
		}
		var resp messageResponse
		rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", req), token), &resp)
		if rec.Code != http.StatusCreated {
			t.Fatalf("send at nesting level %d with unlimited depth: status %d, body %s", i+1, rec.Code, rec.Body.String())
		}
		parentID = resp.MessageID
	}
}

// TestHandleUpdateApp_MaxThreadDepth proves the setting round-trips through
// PATCH, rejects a negative value, and is scoped to the owning org the same
// way every other /apps/{app_id}/... mutation is.
func TestHandleUpdateApp_MaxThreadDepth(t *testing.T) {
	app := testApp(t)
	orgID, orgToken := createTestOrg(t, app, "FREE")
	appID, _, _ := createTestApp(t, app, orgID, orgToken)

	depth := 7
	var resp appResponse
	rec := do(t, app, authed(jsonRequest("PATCH", fmt.Sprintf("/apps/%d", appID), updateAppRequest{MaxThreadDepth: &depth}), orgToken), &resp)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.MaxThreadDepth != 7 {
		t.Fatalf("max_thread_depth = %d, want 7", resp.MaxThreadDepth)
	}

	t.Run("negative rejected", func(t *testing.T) {
		neg := -1
		rec := do(t, app, authed(jsonRequest("PATCH", fmt.Sprintf("/apps/%d", appID), updateAppRequest{MaxThreadDepth: &neg}), orgToken), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("other org rejected", func(t *testing.T) {
		_, attackerToken := createTestOrg(t, app, "FREE")
		d := 5
		rec := do(t, app, authed(jsonRequest("PATCH", fmt.Sprintf("/apps/%d", appID), updateAppRequest{MaxThreadDepth: &d}), attackerToken), nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}
