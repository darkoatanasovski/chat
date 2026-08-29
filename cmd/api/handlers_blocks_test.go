package main

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestHandleBlockUser_Valid(t *testing.T) {
	app := testApp(t)
	_, tokenA := createTestUser(t, app, "block-a")
	idB, _ := createTestUser(t, app, "block-b")

	var resp blockResponse
	rec := do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: idB.String()}), tokenA), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("block user: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.BlockedUserID != idB.String() {
		t.Fatalf("unexpected block response: %+v", resp)
	}

	var list []blockListEntry
	rec = do(t, app, authed(jsonRequest("GET", "/blocks", nil), tokenA), &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].UserID != idB.String() {
		t.Fatalf("list blocks: status = %d, list = %+v", rec.Code, list)
	}
}

func TestHandleBlockUser_IsIdempotent(t *testing.T) {
	app := testApp(t)
	_, tokenA := createTestUser(t, app, "block-idem-a")
	idB, _ := createTestUser(t, app, "block-idem-b")

	for range 2 {
		rec := do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: idB.String()}), tokenA), nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("block user: status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	var list []blockListEntry
	rec := do(t, app, authed(jsonRequest("GET", "/blocks", nil), tokenA), &list)
	if rec.Code != http.StatusOK || len(list) != 1 {
		t.Fatalf("expected exactly one block after blocking twice, got %+v", list)
	}
}

func TestHandleBlockUser_CannotBlockSelf(t *testing.T) {
	app := testApp(t)
	idA, tokenA := createTestUser(t, app, "block-self")

	rec := do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: idA.String()}), tokenA), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("block self: status = %d, want 400", rec.Code)
	}
}

func TestHandleBlockUser_UnknownUser(t *testing.T) {
	app := testApp(t)
	_, tokenA := createTestUser(t, app, "block-unknown")

	rec := do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: "00000000-0000-0000-0000-000000000000"}), tokenA), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("block unknown user: status = %d, want 400", rec.Code)
	}
}

func TestHandleBlockUser_CrossAppIsolation(t *testing.T) {
	app := testApp(t)
	_, tokenA := createTestUser(t, app, "block-cross-a")

	_, keyB, secretB := createOrgAndApp(t, app, "FREE")
	var outsider createUserResponse
	rec := do(t, app, basicAuthed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "block-cross-b", Region: "eu"}), keyB, secretB), &outsider)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app-b user: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: outsider.UserID}), tokenA), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("block user in another app: status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUnblockUser_OnlyBlockerCanUnblock(t *testing.T) {
	app := testApp(t)
	idA, tokenA := createTestUser(t, app, "unblock-only-a")
	idB, tokenB := createTestUser(t, app, "unblock-only-b")

	rec := do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: idB.String()}), tokenA), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("block: status = %d", rec.Code)
	}

	// B was never the blocker of this pair — B trying to unblock must fail,
	// matching "the one who blocked can only unblock."
	rec = do(t, app, authed(jsonRequest("DELETE", "/blocks/"+idA.String(), nil), tokenB), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-blocker unblock: status = %d, want 404", rec.Code)
	}

	// A, the actual blocker, can unblock.
	rec = do(t, app, authed(jsonRequest("DELETE", "/blocks/"+idB.String(), nil), tokenA), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("blocker unblock: status = %d, want 204, body = %s", rec.Code, rec.Body.String())
	}

	// Unblocking again (already gone) is a 404, not an error.
	rec = do(t, app, authed(jsonRequest("DELETE", "/blocks/"+idB.String(), nil), tokenA), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("re-unblock: status = %d, want 404", rec.Code)
	}
}

// TestBlocking_EnforcedBidirectionallyInMessageHistory is the core
// behavioral test: once A blocks B, neither sees the other's messages in a
// channel they both belong to, even though only A performed the block —
// and a third member unaffected by the block still sees everyone.
func TestBlocking_EnforcedBidirectionallyInMessageHistory(t *testing.T) {
	app := testApp(t)
	_, tokenA := createTestUser(t, app, "block-enforce-a")
	idB, tokenB := createTestUser(t, app, "block-enforce-b")
	idC, tokenC := createTestUser(t, app, "block-enforce-c")

	channel := createTestChannel(t, app, tokenA, "block-enforce-channel")
	addChannelMember(t, app, tokenA, channel.ChannelID, idB)
	addChannelMember(t, app, tokenA, channel.ChannelID, idC)

	sendTestMessage(t, app, tokenA, channel.ChannelID, "from A")
	sendTestMessage(t, app, tokenB, channel.ChannelID, "from B")
	sendTestMessage(t, app, tokenC, channel.ChannelID, "from C")

	rec := do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: idB.String()}), tokenA), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("block: status = %d", rec.Code)
	}

	assertSenders := func(t *testing.T, token string, want map[string]bool) {
		t.Helper()
		var listed []messageResponse
		rec := do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages", nil), token), &listed)
		if rec.Code != http.StatusOK {
			t.Fatalf("list messages: status = %d, body = %s", rec.Code, rec.Body.String())
		}
		got := map[string]bool{}
		for _, m := range listed {
			got[m.Body] = true
		}
		for body, expected := range want {
			if got[body] != expected {
				t.Fatalf("message %q: expected present=%v, got present=%v (full list: %+v)", body, expected, got[body], listed)
			}
		}
	}

	// A blocked B: A doesn't see B's message, but still sees its own and C's.
	assertSenders(t, tokenA, map[string]bool{"from A": true, "from B": false, "from C": true})
	// Enforcement is bidirectional even though B never blocked anyone: B
	// doesn't see A's message either.
	assertSenders(t, tokenB, map[string]bool{"from A": false, "from B": true, "from C": true})
	// C is uninvolved in the block and sees everyone.
	assertSenders(t, tokenC, map[string]bool{"from A": true, "from B": true, "from C": true})

	// Unblocking restores visibility both ways.
	rec = do(t, app, authed(jsonRequest("DELETE", "/blocks/"+idB.String(), nil), tokenA), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unblock: status = %d", rec.Code)
	}
	assertSenders(t, tokenA, map[string]bool{"from A": true, "from B": true, "from C": true})
	assertSenders(t, tokenB, map[string]bool{"from A": true, "from B": true, "from C": true})
}

// TestBlocking_IndependentMutualBlocksBothMustBeRemoved covers the edge
// case internal/blocks.Repo.Exists exists for: if A and B independently
// blocked each other (two separate rows), A unblocking only removes A's
// row — B's still stands, so enforcement must continue until B also
// unblocks.
func TestBlocking_IndependentMutualBlocksBothMustBeRemoved(t *testing.T) {
	app := testApp(t)
	idA, tokenA := createTestUser(t, app, "mutual-block-a")
	idB, tokenB := createTestUser(t, app, "mutual-block-b")

	channel := createTestChannel(t, app, tokenA, "mutual-block-channel")
	addChannelMember(t, app, tokenA, channel.ChannelID, idB)
	sendTestMessage(t, app, tokenA, channel.ChannelID, "from A")
	sendTestMessage(t, app, tokenB, channel.ChannelID, "from B")

	// Both independently block each other.
	rec := do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: idB.String()}), tokenA), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("A blocks B: status = %d", rec.Code)
	}
	rec = do(t, app, authed(jsonRequest("POST", "/blocks", blockUserRequest{UserID: idA.String()}), tokenB), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("B blocks A: status = %d", rec.Code)
	}

	listBodies := func(t *testing.T, token string) map[string]bool {
		t.Helper()
		var listed []messageResponse
		rec := do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/messages", nil), token), &listed)
		if rec.Code != http.StatusOK {
			t.Fatalf("list messages: status = %d", rec.Code)
		}
		out := map[string]bool{}
		for _, m := range listed {
			out[m.Body] = true
		}
		return out
	}

	// A removes only its own block row.
	rec = do(t, app, authed(jsonRequest("DELETE", "/blocks/"+idB.String(), nil), tokenA), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("A unblocks B: status = %d", rec.Code)
	}

	// B's independent block of A must still be enforced.
	if got := listBodies(t, tokenA); got["from B"] {
		t.Fatalf("expected B's message still hidden from A (B's own block still stands), got %v", got)
	}
	if got := listBodies(t, tokenB); got["from A"] {
		t.Fatalf("expected A's message still hidden from B (B's own block still stands), got %v", got)
	}

	// Now B unblocks too — communication fully restored.
	rec = do(t, app, authed(jsonRequest("DELETE", "/blocks/"+idA.String(), nil), tokenB), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("B unblocks A: status = %d", rec.Code)
	}
	if got := listBodies(t, tokenA); !got["from B"] {
		t.Fatalf("expected B's message visible again after both unblocked, got %v", got)
	}
	if got := listBodies(t, tokenB); !got["from A"] {
		t.Fatalf("expected A's message visible again after both unblocked, got %v", got)
	}
}

// addChannelMember is a fixture wrapping POST /channels/{id}/members.
func addChannelMember(t *testing.T, app *App, callerToken, channelID string, userID uuid.UUID) {
	t.Helper()
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channelID+"/members", addMemberRequest{UserID: userID.String()}), callerToken), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add channel member: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
