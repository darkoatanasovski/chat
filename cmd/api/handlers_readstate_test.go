package main

import (
	"fmt"
	"net/http"
	"testing"
)

func markReadPath(channelID string) string {
	return fmt.Sprintf("/channels/%s/read", channelID)
}

func readStatePath(channelID string) string {
	return fmt.Sprintf("/channels/%s/read-state", channelID)
}

func TestHandleMarkRead_DefaultsToLatestSequence(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "read-state-sender")
	channel := createTestChannel(t, app, token, "read-state-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "mark me read")

	var resp readStateResponse
	rec := do(t, app, authed(jsonRequest("POST", markReadPath(channel.ChannelID), nil), token), &resp)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.LastReadSequence != msg.Sequence {
		t.Fatalf("expected last_read_sequence=%d (the latest message), got %d", msg.Sequence, resp.LastReadSequence)
	}
}

func TestHandleMarkRead_IsMonotonic(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "read-state-monotonic")
	channel := createTestChannel(t, app, token, "read-state-monotonic-test")
	first := sendTestMessage(t, app, token, channel.ChannelID, "one")
	second := sendTestMessage(t, app, token, channel.ChannelID, "two")

	var afterSecond readStateResponse
	do(t, app, authed(jsonRequest("POST", markReadPath(channel.ChannelID), markReadRequest{Sequence: second.Sequence}), token), &afterSecond)
	if afterSecond.LastReadSequence != second.Sequence {
		t.Fatalf("expected %d, got %d", second.Sequence, afterSecond.LastReadSequence)
	}

	// Reporting an OLDER sequence (e.g. a reordered/stale client call) must
	// never regress the stored watermark.
	var afterStale readStateResponse
	rec := do(t, app, authed(jsonRequest("POST", markReadPath(channel.ChannelID), markReadRequest{Sequence: first.Sequence}), token), &afterStale)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if afterStale.LastReadSequence != second.Sequence {
		t.Fatalf("expected watermark to stay at %d (not regress to %d), got %d", second.Sequence, first.Sequence, afterStale.LastReadSequence)
	}
}

func TestHandleMarkRead_RequiresMembership(t *testing.T) {
	app := testApp(t)
	_, ownerToken := createTestUser(t, app, "read-state-owner")
	channel := createTestChannel(t, app, ownerToken, "read-state-membership-test")
	sendTestMessage(t, app, ownerToken, channel.ChannelID, "members only")
	_, outsiderToken := createTestUser(t, app, "read-state-outsider")

	rec := do(t, app, authed(jsonRequest("POST", markReadPath(channel.ChannelID), nil), outsiderToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestHandleMarkRead_RateLimitEnforced(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "read-state-rate-limit")
	channel := createTestChannel(t, app, token, "read-state-rate-limit-test")
	sendTestMessage(t, app, token, channel.ChannelID, "rate limit me")

	// FREE tier's read_updates_per_minute is 30 (deploy/tiers.yaml).
	var lastStatus int
	for range 31 {
		rec := do(t, app, authed(jsonRequest("POST", markReadPath(channel.ChannelID), nil), token), nil)
		lastStatus = rec.Code
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("expected the 31st mark-read within a minute to be rate-limited, got %d", lastStatus)
	}
}

func TestHandleListReadState_ReflectsEachMembersWatermark(t *testing.T) {
	app := testApp(t)
	ownerID, ownerToken := createTestUser(t, app, "read-state-list-owner")
	channel := createTestChannel(t, app, ownerToken, "read-state-list-test")
	msg := sendTestMessage(t, app, ownerToken, channel.ChannelID, "read me")
	memberID, memberToken := createTestUser(t, app, "read-state-list-member")
	do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/channels/%s/members", channel.ChannelID), addMemberRequest{UserID: memberID.String()}), ownerToken), nil)

	do(t, app, authed(jsonRequest("POST", markReadPath(channel.ChannelID), markReadRequest{Sequence: msg.Sequence}), memberToken), nil)

	var states []readStateResponse
	rec := do(t, app, authed(jsonRequest("GET", readStatePath(channel.ChannelID), nil), ownerToken), &states)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	byUser := map[string]int64{}
	for _, s := range states {
		byUser[s.UserID] = s.LastReadSequence
	}
	if byUser[memberID.String()] != msg.Sequence {
		t.Fatalf("expected member's watermark = %d, got %+v", msg.Sequence, byUser)
	}
	if _, ownerMarked := byUser[ownerID.String()]; ownerMarked {
		t.Fatalf("owner never called mark-read, expected no row for them, got %+v", byUser)
	}
}

func TestHandleListReadState_RequiresMembership(t *testing.T) {
	app := testApp(t)
	_, ownerToken := createTestUser(t, app, "read-state-list-owner-2")
	channel := createTestChannel(t, app, ownerToken, "read-state-list-membership-test")
	_, outsiderToken := createTestUser(t, app, "read-state-list-outsider")

	rec := do(t, app, authed(jsonRequest("GET", readStatePath(channel.ChannelID), nil), outsiderToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestReadState_CrossAppIsolation(t *testing.T) {
	app := testApp(t)

	_, keyA, secretA := createOrgAndApp(t, app, "FREE")
	var ownerResp createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "read-state-app-a-owner", Region: "eu"}), appAccessToken(t, app, keyA, secretA)), &ownerResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app-a owner: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	channel := createTestChannel(t, app, ownerResp.Token, "read-state-cross-app-channel")
	sendTestMessage(t, app, ownerResp.Token, channel.ChannelID, "cross-app target")

	_, keyB, secretB := createOrgAndApp(t, app, "FREE")
	var outsiderResp createUserResponse
	rec = do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "read-state-app-b-outsider", Region: "eu"}), appAccessToken(t, app, keyB, secretB)), &outsiderResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app-b outsider: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("POST", markReadPath(channel.ChannelID), nil), outsiderResp.Token), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mark read across apps: status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}
