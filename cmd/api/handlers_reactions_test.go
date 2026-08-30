package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func sendTestMessage(t *testing.T, app *App, token, channelID, body string) messageResponse {
	t.Helper()
	var resp messageResponse
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channelID+"/messages", sendMessageRequest{
		ClientMessageID: uuid.NewString(), Body: body,
	}), token), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send test message: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return resp
}

func reactionPath(channelID, messageID, reaction string) string {
	return fmt.Sprintf("/channels/%s/messages/%s/reactions/%s", channelID, messageID, reaction)
}

func addReactionPath(channelID, messageID string) string {
	return fmt.Sprintf("/channels/%s/messages/%s/reactions", channelID, messageID)
}

func TestHandleAddReaction_ValidAndDenormalizedStateUpdates(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "reaction-sender")
	channel := createTestChannel(t, app, token, "reaction-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "react to me")

	var resp reactionStateResponse
	rec := do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "like"}), token), &resp)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.ReactionCounts["like"] != 1 {
		t.Fatalf("expected reaction_counts[like]=1, got %+v", resp.ReactionCounts)
	}
	if len(resp.LatestReactions) != 1 || resp.LatestReactions[0].Reaction != "like" {
		t.Fatalf("expected latest_reactions to contain the new reaction, got %+v", resp.LatestReactions)
	}

	// GET /channels/{id}/messages must reflect the same denormalized state
	// without any extra join — that's the entire point of denormalizing.
	var listed []messageResponse
	rec = do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages", nil), token), &listed)
	if rec.Code != http.StatusOK || len(listed) == 0 {
		t.Fatalf("list messages: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if listed[0].ReactionCounts["like"] != 1 {
		t.Fatalf("expected listed message to carry reaction_counts, got %+v", listed[0].ReactionCounts)
	}
}

func TestHandleAddReaction_IsIdempotent(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "reaction-idempotent")
	channel := createTestChannel(t, app, token, "reaction-idempotent-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "react twice")

	var first, second reactionStateResponse
	do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "celebrate"}), token), &first)
	rec := do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "celebrate"}), token), &second)
	if rec.Code != http.StatusOK {
		t.Fatalf("second add: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if second.ReactionCounts["celebrate"] != 1 {
		t.Fatalf("expected reacting twice with the same key to stay at count 1, got %d", second.ReactionCounts["celebrate"])
	}
}

func TestHandleAddReaction_RequiresMembership(t *testing.T) {
	app := testApp(t)
	_, ownerToken := createTestUser(t, app, "reaction-owner")
	channel := createTestChannel(t, app, ownerToken, "reaction-membership-test")
	msg := sendTestMessage(t, app, ownerToken, channel.ChannelID, "members only")
	_, outsiderToken := createTestUser(t, app, "reaction-outsider")

	rec := do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "eyes"}), outsiderToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestHandleAddReaction_UnknownMessageIs404(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "reaction-404")
	channel := createTestChannel(t, app, token, "reaction-404-test")

	rec := do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, uuid.NewString()), addReactionRequest{Reaction: "like"}), token), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body would help debug", rec.Code)
	}
}

func TestHandleAddReaction_Validation(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "reaction-validation")
	channel := createTestChannel(t, app, token, "reaction-validation-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "validate me")

	cases := []struct {
		name     string
		reaction string
	}{
		{"empty reaction", ""},
		{"unrecognized reaction key", "thumbsup"},
		{"raw emoji is rejected, not a canonical key", "👍"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: tc.reaction}), token), nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestHandleAddReaction_RateLimitEnforced(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "reaction-rate-limit")
	channel := createTestChannel(t, app, token, "reaction-rate-limit-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "rate limit me")

	// FREE tier's reactions_per_minute is 40 (deploy/tiers.yaml). The rate
	// limit is checked before idempotency, so repeatedly "adding" the same
	// already-present reaction still consumes a token each time.
	var lastStatus int
	for range 41 {
		rec := do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "like"}), token), nil)
		lastStatus = rec.Code
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("expected the 41st reaction write within a minute to be rate-limited, got %d", lastStatus)
	}
}

func TestHandleRemoveReaction_ValidAndIdempotent(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "reaction-remover")
	channel := createTestChannel(t, app, token, "reaction-remove-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "add then remove")

	do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "love"}), token), nil)

	var resp reactionStateResponse
	rec := do(t, app, authed(jsonRequest("DELETE", reactionPath(channel.ChannelID, msg.MessageID, "love"), nil), token), &resp)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, present := resp.ReactionCounts["love"]; present {
		t.Fatalf("expected love to be gone from reaction_counts entirely once its count hits 0, got %+v", resp.ReactionCounts)
	}

	// Removing again (already gone) must stay a no-op 200, not error.
	rec = do(t, app, authed(jsonRequest("DELETE", reactionPath(channel.ChannelID, msg.MessageID, "love"), nil), token), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-remove: status = %d, want 200 (idempotent no-op)", rec.Code)
	}
}

func TestHandleRemoveReaction_UnknownReactionKeyIs400(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "reaction-remove-invalid")
	channel := createTestChannel(t, app, token, "reaction-remove-invalid-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "remove invalid")

	rec := do(t, app, authed(jsonRequest("DELETE", reactionPath(channel.ChannelID, msg.MessageID, "not-a-real-reaction"), nil), token), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRemoveReaction_OnlyRemovesOwnReaction(t *testing.T) {
	app := testApp(t)
	ownerID, ownerToken := createTestUser(t, app, "reaction-remove-owner")
	channel := createTestChannel(t, app, ownerToken, "reaction-remove-own-test")
	msg := sendTestMessage(t, app, ownerToken, channel.ChannelID, "shared reactions")
	memberID, memberToken := createTestUser(t, app, "reaction-remove-member")
	do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/channels/%s/members", channel.ChannelID), addMemberRequest{UserID: memberID.String()}), ownerToken), nil)

	do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "fire"}), ownerToken), nil)
	var afterOwner reactionStateResponse
	do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "fire"}), memberToken), &afterOwner)
	if afterOwner.ReactionCounts["fire"] != 2 {
		t.Fatalf("expected both users' fire reactions counted, got %d", afterOwner.ReactionCounts["fire"])
	}

	var afterRemove reactionStateResponse
	rec := do(t, app, authed(jsonRequest("DELETE", reactionPath(channel.ChannelID, msg.MessageID, "fire"), nil), memberToken), &afterRemove)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if afterRemove.ReactionCounts["fire"] != 1 {
		t.Fatalf("expected only the member's own fire reaction removed (owner's stays), got count %d", afterRemove.ReactionCounts["fire"])
	}
	_ = ownerID
}

func TestReactions_CrossAppIsolation(t *testing.T) {
	app := testApp(t)

	_, keyA, secretA := createOrgAndApp(t, app, "FREE")
	var ownerResp createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "reaction-app-a-owner", Region: "eu"}), appAccessToken(t, app, keyA, secretA)), &ownerResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app-a owner: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	channel := createTestChannel(t, app, ownerResp.Token, "reaction-cross-app-channel")
	msg := sendTestMessage(t, app, ownerResp.Token, channel.ChannelID, "cross-app target")

	_, keyB, secretB := createOrgAndApp(t, app, "FREE")
	var outsiderResp createUserResponse
	rec = do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "reaction-app-b-outsider", Region: "eu"}), appAccessToken(t, app, keyB, secretB)), &outsiderResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app-b outsider: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("POST", addReactionPath(channel.ChannelID, msg.MessageID), addReactionRequest{Reaction: "like"}), outsiderResp.Token), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("react across apps: status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}
