package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func editMessagePath(channelID, messageID string) string {
	return fmt.Sprintf("/channels/%s/messages/%s", channelID, messageID)
}

// createMessageEditTestApp is createTestOrg + createTestApp + two members
// (alice, the channel's creator; bob, a second member) on one channel, all
// under one dedicated app — these tests need to toggle
// message_edit_enabled (PATCH /apps/{app_id}), which the shared
// defaultAppCredentials app can't do without affecting every other test
// that relies on its default, and need a second user in the same channel to
// prove editing is sender-only.
func createMessageEditTestApp(t *testing.T) (app *App, orgToken string, appID int64, aliceToken, bobToken string, channel channelResponse) {
	t.Helper()
	app = testApp(t)
	orgID, orgToken := createTestOrg(t, app, "PRO")
	appID, key, secret := createTestApp(t, app, orgID, orgToken)
	appAccess := appAccessToken(t, app, key, secret)

	var aliceResp, bobResp createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "edit-alice", Region: "eu"}), appAccess), &aliceResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create alice: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "edit-bob", Region: "eu"}), appAccess), &bobResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create bob: status %d, body %s", rec.Code, rec.Body.String())
	}

	channel = createTestChannel(t, app, aliceResp.Token, "edit-test")
	bobID, err := uuid.Parse(bobResp.UserID)
	if err != nil {
		t.Fatalf("parse bob id: %v", err)
	}
	rec = do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/members", addMemberRequest{UserID: bobID.String()}), aliceResp.Token), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add bob to channel: status %d, body %s", rec.Code, rec.Body.String())
	}

	return app, orgToken, appID, aliceResp.Token, bobResp.Token, channel
}

// TestHandleEditMessage_OwnerCanEdit proves the sender can edit their own
// message: the response and a subsequent list both carry the new body and
// a freshly-set edited_at, with created_at/sequence unchanged.
func TestHandleEditMessage_OwnerCanEdit(t *testing.T) {
	app, _, _, aliceToken, _, channel := createMessageEditTestApp(t)
	msg := sendTestMessage(t, app, aliceToken, channel.ChannelID, "original body")
	if msg.EditedAt != nil {
		t.Fatalf("fresh message edited_at = %v, want nil", msg.EditedAt)
	}

	var edited messageResponse
	rec := do(t, app, authed(jsonRequest("PATCH", editMessagePath(channel.ChannelID, msg.MessageID), editMessageRequest{Body: "edited body"}), aliceToken), &edited)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: status %d, body %s", rec.Code, rec.Body.String())
	}
	if edited.Body != "edited body" {
		t.Fatalf("edited.Body = %q, want %q", edited.Body, "edited body")
	}
	if edited.EditedAt == nil {
		t.Fatalf("edited.EditedAt = nil, want set")
	}
	if edited.Sequence != msg.Sequence || edited.CreatedAt != msg.CreatedAt {
		t.Fatalf("edit must not change sequence/created_at: got seq=%d created_at=%s, want seq=%d created_at=%s",
			edited.Sequence, edited.CreatedAt, msg.Sequence, msg.CreatedAt)
	}

	var list []messageResponse
	rec = do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages", nil), aliceToken), &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d, body %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, m := range list {
		if m.MessageID == msg.MessageID {
			found = true
			if m.Body != "edited body" || m.EditedAt == nil {
				t.Fatalf("listed message not updated: body=%q edited_at=%v", m.Body, m.EditedAt)
			}
		}
	}
	if !found {
		t.Fatalf("edited message %q not present in list", msg.MessageID)
	}
}

// TestHandleEditMessage_NotSenderForbidden proves editing someone else's
// message is a 403, and that the message is left completely untouched.
func TestHandleEditMessage_NotSenderForbidden(t *testing.T) {
	app, _, _, aliceToken, bobToken, channel := createMessageEditTestApp(t)
	msg := sendTestMessage(t, app, aliceToken, channel.ChannelID, "alice's message")

	rec := do(t, app, authed(jsonRequest("PATCH", editMessagePath(channel.ChannelID, msg.MessageID), editMessageRequest{Body: "bob tries to edit"}), bobToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}

	var list []messageResponse
	do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages", nil), aliceToken), &list)
	for _, m := range list {
		if m.MessageID == msg.MessageID && (m.Body != "alice's message" || m.EditedAt != nil) {
			t.Fatalf("message was mutated by a forbidden edit: body=%q edited_at=%v", m.Body, m.EditedAt)
		}
	}
}

// TestHandleEditMessage_NonexistentMessage proves a well-formed but
// nonexistent message_id is a 404, not a 500 or a silently-created row.
func TestHandleEditMessage_NonexistentMessage(t *testing.T) {
	app, _, _, aliceToken, _, channel := createMessageEditTestApp(t)

	rec := do(t, app, authed(jsonRequest("PATCH", editMessagePath(channel.ChannelID, uuid.NewString()), editMessageRequest{Body: "no such message"}), aliceToken), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleEditMessage_EmptyBodyRejected proves the same body validation
// as sending a message (non-empty, <= 4000 chars) applies to editing.
func TestHandleEditMessage_EmptyBodyRejected(t *testing.T) {
	app, _, _, aliceToken, _, channel := createMessageEditTestApp(t)
	msg := sendTestMessage(t, app, aliceToken, channel.ChannelID, "will not be replaced with empty")

	rec := do(t, app, authed(jsonRequest("PATCH", editMessagePath(channel.ChannelID, msg.MessageID), editMessageRequest{Body: ""}), aliceToken), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleEditMessage_DisabledForApp proves flipping message_edit_enabled
// off (PATCH /apps/{app_id}) takes effect immediately: the very next edit
// attempt, even by the message's own sender, is rejected.
func TestHandleEditMessage_DisabledForApp(t *testing.T) {
	app, orgToken, appID, aliceToken, _, channel := createMessageEditTestApp(t)
	msg := sendTestMessage(t, app, aliceToken, channel.ChannelID, "editable while enabled")

	enabled := false
	rec := do(t, app, authed(jsonRequest("PATCH", fmt.Sprintf("/apps/%d", appID), updateAppRequest{MessageEditEnabled: &enabled}), orgToken), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable message editing: status %d, body %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("PATCH", editMessagePath(channel.ChannelID, msg.MessageID), editMessageRequest{Body: "should be rejected"}), aliceToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (editing disabled), body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleUpdateApp_PartialSettings proves PATCH /apps/{app_id} applies
// only the field(s) present in the request body, leaving the other setting
// exactly as it was — and rejects a request with neither field set.
func TestHandleUpdateApp_PartialSettings(t *testing.T) {
	app := testApp(t)
	orgID, orgToken := createTestOrg(t, app, "FREE")
	appID, _, _ := createTestApp(t, app, orgID, orgToken)

	// Defaults: max_thread_depth=3, message_edit_enabled=true.
	depth := 9
	var afterDepth appResponse
	rec := do(t, app, authed(jsonRequest("PATCH", fmt.Sprintf("/apps/%d", appID), updateAppRequest{MaxThreadDepth: &depth}), orgToken), &afterDepth)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if afterDepth.MaxThreadDepth != 9 {
		t.Fatalf("max_thread_depth = %d, want 9", afterDepth.MaxThreadDepth)
	}
	if !afterDepth.MessageEditEnabled {
		t.Fatalf("message_edit_enabled changed to false by an update that never mentioned it")
	}

	enabled := false
	var afterEdit appResponse
	rec = do(t, app, authed(jsonRequest("PATCH", fmt.Sprintf("/apps/%d", appID), updateAppRequest{MessageEditEnabled: &enabled}), orgToken), &afterEdit)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if afterEdit.MessageEditEnabled {
		t.Fatalf("message_edit_enabled = true, want false")
	}
	if afterEdit.MaxThreadDepth != 9 {
		t.Fatalf("max_thread_depth reset to %d by an update that never mentioned it, want unchanged 9", afterEdit.MaxThreadDepth)
	}

	rec = do(t, app, authed(jsonRequest("PATCH", fmt.Sprintf("/apps/%d", appID), updateAppRequest{}), orgToken), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty partial update: status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
