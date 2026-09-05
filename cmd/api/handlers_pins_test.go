package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func pinPath(channelID, messageID string) string {
	return fmt.Sprintf("/channels/%s/messages/%s/pin", channelID, messageID)
}

func pinnedListPath(channelID string) string {
	return fmt.Sprintf("/channels/%s/pinned-messages", channelID)
}

// TestHandlePinMessage_PinAndList proves pinning sets pinned_at/pinned_by on
// the message (pinned_by matching the caller), that both a subsequent list
// and GET .../pinned-messages reflect it, and that an unpinned message
// never appears in the pinned list.
func TestHandlePinMessage_PinAndList(t *testing.T) {
	app := testApp(t)
	aliceID, aliceToken := createTestUser(t, app, "pin-sender")
	channel := createTestChannel(t, app, aliceToken, "pin-test")
	pinned := sendTestMessage(t, app, aliceToken, channel.ChannelID, "pin me")
	unpinned := sendTestMessage(t, app, aliceToken, channel.ChannelID, "leave me alone")

	var resp messageResponse
	rec := do(t, app, authed(jsonRequest("POST", pinPath(channel.ChannelID, pinned.MessageID), nil), aliceToken), &resp)
	if rec.Code != http.StatusOK {
		t.Fatalf("pin: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.PinnedAt == nil {
		t.Fatalf("pinned.PinnedAt = nil, want set")
	}
	if resp.PinnedBy == nil || *resp.PinnedBy != aliceID.String() {
		t.Fatalf("pinned.PinnedBy = %v, want %s", resp.PinnedBy, aliceID)
	}

	var list []messageResponse
	rec = do(t, app, authed(jsonRequest("GET", pinnedListPath(channel.ChannelID), nil), aliceToken), &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("list pinned: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(list) != 1 || list[0].MessageID != pinned.MessageID {
		t.Fatalf("expected exactly the pinned message in the pinned list, got %+v", list)
	}
	for _, m := range list {
		if m.MessageID == unpinned.MessageID {
			t.Fatalf("unpinned message unexpectedly appeared in pinned list")
		}
	}
}

// TestHandlePinMessage_IsIdempotent proves pinning an already-pinned
// message is a no-op: the second call succeeds but doesn't change
// pinned_at/pinned_by.
func TestHandlePinMessage_IsIdempotent(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "pin-idempotent")
	channel := createTestChannel(t, app, token, "pin-idempotent-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "pin twice")

	var first, second messageResponse
	do(t, app, authed(jsonRequest("POST", pinPath(channel.ChannelID, msg.MessageID), nil), token), &first)
	rec := do(t, app, authed(jsonRequest("POST", pinPath(channel.ChannelID, msg.MessageID), nil), token), &second)
	if rec.Code != http.StatusOK {
		t.Fatalf("second pin: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if first.PinnedAt == nil || second.PinnedAt == nil || *first.PinnedAt != *second.PinnedAt {
		t.Fatalf("re-pinning changed pinned_at: first=%v second=%v", first.PinnedAt, second.PinnedAt)
	}
}

// TestHandleUnpinMessage_AnyMemberCanUnpin proves there's no "only the
// pinner can unpin" restriction: bob, who never pinned the message, can
// still unpin what alice pinned.
func TestHandleUnpinMessage_AnyMemberCanUnpin(t *testing.T) {
	app := testApp(t)
	_, aliceToken := createTestUser(t, app, "pin-unpin-alice")
	bobID, bobToken := createTestUser(t, app, "pin-unpin-bob")
	channel := createTestChannel(t, app, aliceToken, "pin-unpin-test")

	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/members", addMemberRequest{UserID: bobID.String()}), aliceToken), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add bob to channel: status %d, body %s", rec.Code, rec.Body.String())
	}

	msg := sendTestMessage(t, app, aliceToken, channel.ChannelID, "pinned by alice")
	do(t, app, authed(jsonRequest("POST", pinPath(channel.ChannelID, msg.MessageID), nil), aliceToken), nil)

	var resp messageResponse
	rec = do(t, app, authed(jsonRequest("DELETE", pinPath(channel.ChannelID, msg.MessageID), nil), bobToken), &resp)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob unpin: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.PinnedAt != nil {
		t.Fatalf("PinnedAt = %v after unpin, want nil", resp.PinnedAt)
	}
}

// TestHandlePinMessage_NonexistentMessage proves a well-formed but
// nonexistent message_id is a 404.
func TestHandlePinMessage_NonexistentMessage(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "pin-404")
	channel := createTestChannel(t, app, token, "pin-404-test")

	rec := do(t, app, authed(jsonRequest("POST", pinPath(channel.ChannelID, uuid.NewString()), nil), token), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandlePinMessage_RequiresMembership proves a non-member is rejected
// with 403 rather than being able to pin a message in a channel they can't
// even read.
func TestHandlePinMessage_RequiresMembership(t *testing.T) {
	app := testApp(t)
	_, ownerToken := createTestUser(t, app, "pin-owner")
	_, outsiderToken := createTestUser(t, app, "pin-outsider")
	channel := createTestChannel(t, app, ownerToken, "pin-membership-test")
	msg := sendTestMessage(t, app, ownerToken, channel.ChannelID, "members only")

	rec := do(t, app, authed(jsonRequest("POST", pinPath(channel.ChannelID, msg.MessageID), nil), outsiderToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}
