package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// createPollTestApp is createTestOrg + createTestApp + one member-ready
// channel, all under one dedicated PRO-tier app — tests in this file send
// enough poll creates/votes in a row (several sub-tests each) that the
// shared FREE-tier defaultAppCredentials app's tight polls_per_minute (5)
// would make them flaky depending on test order/parallelism; PRO's
// polls_per_minute (50) and poll_votes_per_minute (400) give plenty of
// headroom.
func createPollTestApp(t *testing.T) (app *App, token string, channel channelResponse) {
	t.Helper()
	app = testApp(t)
	orgID, orgToken := createTestOrg(t, app, "PRO")
	_, key, secret := createTestApp(t, app, orgID, orgToken)
	appToken := appAccessToken(t, app, key, secret)

	var userResp createUserResponse
	rec := do(t, app, authed(jsonRequest("POST", "/users", createUserRequest{DisplayName: "poll-tester", Region: "eu"}), appToken), &userResp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create poll test user: status %d, body %s", rec.Code, rec.Body.String())
	}

	channel = createTestChannel(t, app, userResp.Token, "polls-test")
	return app, userResp.Token, channel
}

func newPollBody(question string, options []string, multiSelect bool, closesAt *string) map[string]any {
	body := map[string]any{"question": question, "options": options, "multi_select": multiSelect}
	if closesAt != nil {
		body["closes_at"] = *closesAt
	}
	return body
}

// TestHandleCreatePoll_Valid proves a freshly created poll comes back with
// every option at 0 votes, no closes_at, and multi_select defaulted false
// when omitted.
func TestHandleCreatePoll_Valid(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	var poll pollResponse
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Best language?", []string{"Go", "Rust"}, false, nil)), token), &poll)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create poll: status %d, body %s", rec.Code, rec.Body.String())
	}
	if poll.Question != "Best language?" || poll.MultiSelect || poll.ClosesAt != nil {
		t.Fatalf("unexpected poll shape: %+v", poll)
	}
	if len(poll.Options) != 2 || poll.Options[0].Label != "Go" || poll.Options[0].VoteCount != 0 {
		t.Fatalf("unexpected options: %+v", poll.Options)
	}
	if poll.TotalVoters != 0 {
		t.Fatalf("expected 0 total_voters on a fresh poll, got %d", poll.TotalVoters)
	}
}

// TestHandleCreatePoll_Validation covers every 400 path: too few/too many
// options, a duplicate option (case-insensitive), and a closes_at that
// isn't in the future.
func TestHandleCreatePoll_Validation(t *testing.T) {
	app, token, channel := createPollTestApp(t)
	url := "/channels/" + channel.ChannelID + "/polls"

	t.Run("one option", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", url, newPollBody("Q?", []string{"only one"}, false, nil)), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("eleven options", func(t *testing.T) {
		opts := make([]string, 11)
		for i := range opts {
			opts[i] = fmt.Sprintf("option %d", i)
		}
		rec := do(t, app, authed(jsonRequest("POST", url, newPollBody("Q?", opts, false, nil)), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("duplicate option, case-insensitive", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", url, newPollBody("Q?", []string{"Go", "go"}, false, nil)), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("closes_at in the past", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).Format(time.RFC3339)
		rec := do(t, app, authed(jsonRequest("POST", url, newPollBody("Q?", []string{"a", "b"}, false, &past)), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("empty question", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", url, newPollBody("   ", []string{"a", "b"}, false, nil)), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}

// TestHandleSendMessage_WithPoll proves poll_id round-trips through a
// message the same way parent_id does, and that a poll_id naming no poll in
// this channel is a 404, not a silently-accepted top-level message.
func TestHandleSendMessage_WithPoll(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	var poll pollResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Pick one", []string{"a", "b"}, false, nil)), token), &poll)

	var msg messageResponse
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", map[string]any{
		"client_message_id": uuid.NewString(), "body": "vote below!", "poll_id": poll.PollID,
	}), token), &msg)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send message with poll: status %d, body %s", rec.Code, rec.Body.String())
	}
	if msg.PollID == nil || *msg.PollID != poll.PollID {
		t.Fatalf("expected message.poll_id = %q, got %v", poll.PollID, msg.PollID)
	}

	t.Run("nonexistent poll_id is 404", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/messages", map[string]any{
			"client_message_id": uuid.NewString(), "body": "orphan", "poll_id": uuid.NewString(),
		}), token), nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleGetPoll proves a fetched poll matches what was created and that
// a nonexistent poll_id is a 404.
func TestHandleGetPoll(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	var created pollResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Get me", []string{"x", "y"}, false, nil)), token), &created)

	var fetched pollResponse
	rec := do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/polls/"+created.PollID, nil), token), &fetched)
	if rec.Code != http.StatusOK {
		t.Fatalf("get poll: status %d, body %s", rec.Code, rec.Body.String())
	}
	if fetched.PollID != created.PollID || fetched.Question != "Get me" {
		t.Fatalf("fetched poll doesn't match created poll: %+v", fetched)
	}

	t.Run("nonexistent poll is 404", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("GET", "/channels/"+channel.ChannelID+"/polls/"+uuid.NewString(), nil), token), nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}

// TestHandleVotePoll_SingleSelect proves voting sets a 1/0 tally, re-voting
// for a different option MOVES the vote rather than adding to it (a
// single-select poll only ever has one vote per voter), and sending more
// than one option_id on a single-select poll is a 400.
func TestHandleVotePoll_SingleSelect(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	var poll pollResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Single?", []string{"a", "b"}, false, nil)), token), &poll)
	votesURL := "/channels/" + channel.ChannelID + "/polls/" + poll.PollID + "/votes"

	var state pollVoteStateResponse
	rec := do(t, app, authed(jsonRequest("POST", votesURL, map[string]any{"option_ids": []string{poll.Options[0].OptionID}}), token), &state)
	if rec.Code != http.StatusOK {
		t.Fatalf("vote: status %d, body %s", rec.Code, rec.Body.String())
	}
	if state.Options[0].VoteCount != 1 || state.Options[1].VoteCount != 0 || state.TotalVoters != 1 {
		t.Fatalf("unexpected tallies after first vote: %+v", state.Options)
	}
	if len(state.VotedOptionIDs) != 1 || state.VotedOptionIDs[0] != poll.Options[0].OptionID {
		t.Fatalf("unexpected voted_option_ids: %v", state.VotedOptionIDs)
	}

	// Re-vote for the OTHER option: moves the vote, doesn't add a second one.
	rec = do(t, app, authed(jsonRequest("POST", votesURL, map[string]any{"option_ids": []string{poll.Options[1].OptionID}}), token), &state)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-vote: status %d, body %s", rec.Code, rec.Body.String())
	}
	if state.Options[0].VoteCount != 0 || state.Options[1].VoteCount != 1 || state.TotalVoters != 1 {
		t.Fatalf("unexpected tallies after re-vote: %+v", state.Options)
	}

	t.Run("two options on single-select is 400", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("POST", votesURL, map[string]any{
			"option_ids": []string{poll.Options[0].OptionID, poll.Options[1].OptionID},
		}), token), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleVotePoll_MultiSelect proves a multi_select poll accepts more
// than one option_id in a single vote, all counted, but total_voters still
// counts the caller once (not once per option picked).
func TestHandleVotePoll_MultiSelect(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	var poll pollResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Pick any", []string{"a", "b", "c"}, true, nil)), token), &poll)
	votesURL := "/channels/" + channel.ChannelID + "/polls/" + poll.PollID + "/votes"

	var state pollVoteStateResponse
	rec := do(t, app, authed(jsonRequest("POST", votesURL, map[string]any{
		"option_ids": []string{poll.Options[0].OptionID, poll.Options[2].OptionID},
	}), token), &state)
	if rec.Code != http.StatusOK {
		t.Fatalf("vote: status %d, body %s", rec.Code, rec.Body.String())
	}
	if state.Options[0].VoteCount != 1 || state.Options[1].VoteCount != 0 || state.Options[2].VoteCount != 1 {
		t.Fatalf("unexpected tallies: %+v", state.Options)
	}
	if state.TotalVoters != 1 {
		t.Fatalf("expected total_voters = 1 (one voter, two picks), got %d", state.TotalVoters)
	}
	if len(state.VotedOptionIDs) != 2 {
		t.Fatalf("expected 2 voted_option_ids, got %v", state.VotedOptionIDs)
	}
}

// TestHandleClearPollVotes proves retracting a vote zeroes its tally back
// out and is idempotent (retracting again, with nothing left to retract,
// still returns 200 with unchanged state).
func TestHandleClearPollVotes(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	var poll pollResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Clear me", []string{"a", "b"}, false, nil)), token), &poll)
	votesURL := "/channels/" + channel.ChannelID + "/polls/" + poll.PollID + "/votes"

	do(t, app, authed(jsonRequest("POST", votesURL, map[string]any{"option_ids": []string{poll.Options[0].OptionID}}), token), nil)

	var state pollVoteStateResponse
	rec := do(t, app, authed(jsonRequest("DELETE", votesURL, nil), token), &state)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear votes: status %d, body %s", rec.Code, rec.Body.String())
	}
	if state.Options[0].VoteCount != 0 || state.TotalVoters != 0 || len(state.VotedOptionIDs) != 0 {
		t.Fatalf("expected all-zero state after clearing, got %+v", state)
	}

	t.Run("clearing again is a no-op, not an error", func(t *testing.T) {
		rec := do(t, app, authed(jsonRequest("DELETE", votesURL, nil), token), &state)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleVotePoll_OptionNotFound proves a well-formed but nonexistent
// option_id is a 400, not a raw foreign-key-violation 500.
func TestHandleVotePoll_OptionNotFound(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	var poll pollResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Q?", []string{"a", "b"}, false, nil)), token), &poll)

	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls/"+poll.PollID+"/votes", map[string]any{
		"option_ids": []string{uuid.NewString()},
	}), token), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleVotePoll_PollNotFound proves voting on a nonexistent poll_id is
// a 404.
func TestHandleVotePoll_PollNotFound(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls/"+uuid.NewString()+"/votes", map[string]any{
		"option_ids": []string{uuid.NewString()},
	}), token), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleVotePoll_Closed proves a poll past its closes_at rejects both a
// new vote and a retraction with 400 — a short, comfortably-margined sleep
// (not a mocked clock: cmd/api's handler tests are black-box over HTTP with
// no seam to inject time) rather than a longer one, to keep this fast
// without flaking.
func TestHandleVotePoll_Closed(t *testing.T) {
	app, token, channel := createPollTestApp(t)

	closesAt := time.Now().Add(1200 * time.Millisecond).Format(time.RFC3339)
	var poll pollResponse
	do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/polls", newPollBody("Closes soon", []string{"a", "b"}, false, &closesAt)), token), &poll)

	time.Sleep(1500 * time.Millisecond)

	votesURL := "/channels/" + channel.ChannelID + "/polls/" + poll.PollID + "/votes"
	rec := do(t, app, authed(jsonRequest("POST", votesURL, map[string]any{"option_ids": []string{poll.Options[0].OptionID}}), token), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("vote on closed poll: status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("DELETE", votesURL, nil), token), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("retract on closed poll: status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
