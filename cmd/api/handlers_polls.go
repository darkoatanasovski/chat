package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/polls"
	"github.com/darkoatanasovski/chat/internal/quota"
)

const (
	minPollOptions        = 2
	maxPollOptions        = 10
	maxPollQuestionLen    = 500
	maxPollOptionLabelLen = 200
)

type pollOptionResponse struct {
	OptionID  string `json:"option_id"`
	Label     string `json:"label"`
	VoteCount int    `json:"vote_count"`
}

func toPollOptionResponse(opts []polls.Option) []pollOptionResponse {
	out := make([]pollOptionResponse, len(opts))
	for i, o := range opts {
		out[i] = pollOptionResponse{OptionID: o.OptionID.String(), Label: o.Label, VoteCount: o.VoteCount}
	}
	return out
}

func toVotedOptionIDs(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// pollResponse is the full poll — creation and GET only. Voting/retracting
// returns pollVoteStateResponse instead (just the tallies that changed),
// same "the write returns only the state it touched" shape as reactions'
// reactionStateResponse.
type pollResponse struct {
	PollID         string                `json:"poll_id"`
	ChannelID      string                `json:"channel_id"`
	CreatorID      string                `json:"creator_id"`
	Question       string                `json:"question"`
	MultiSelect    bool                  `json:"multi_select"`
	ClosesAt       *string               `json:"closes_at,omitempty"`
	CreatedAt      string                `json:"created_at"`
	Options        []pollOptionResponse  `json:"options"`
	TotalVoters    int                   `json:"total_voters"`
	VotedOptionIDs []string              `json:"voted_option_ids,omitempty"`
}

func pollResponseFrom(p polls.Poll) pollResponse {
	var closesAt *string
	if p.ClosesAt != nil {
		s := p.ClosesAt.Format(rfc3339Milli)
		closesAt = &s
	}
	return pollResponse{
		PollID:         p.PollID.String(),
		ChannelID:      p.ChannelID.String(),
		CreatorID:      p.CreatorID.String(),
		Question:       p.Question,
		MultiSelect:    p.MultiSelect,
		ClosesAt:       closesAt,
		CreatedAt:      p.CreatedAt.Format(rfc3339Milli),
		Options:        toPollOptionResponse(p.Options),
		TotalVoters:    p.TotalVoters,
		VotedOptionIDs: toVotedOptionIDs(p.VotedOptionIDs),
	}
}

type pollVoteStateResponse struct {
	Options        []pollOptionResponse `json:"options"`
	TotalVoters    int                  `json:"total_voters"`
	VotedOptionIDs []string             `json:"voted_option_ids"`
}

func pollVoteStateResponseFrom(p polls.Poll) pollVoteStateResponse {
	return pollVoteStateResponse{
		Options:        toPollOptionResponse(p.Options),
		TotalVoters:    p.TotalVoters,
		VotedOptionIDs: toVotedOptionIDs(p.VotedOptionIDs),
	}
}

type createPollRequest struct {
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	MultiSelect bool     `json:"multi_select"`
	ClosesAt    *string  `json:"closes_at,omitempty"`
}

// handleCreatePoll backs POST /channels/{id}/polls. A poll is created as a
// standalone entity — see internal/polls' package doc — and only becomes
// visible to other members once a message attaches it via poll_id
// (POST /channels/{id}/messages), same as any other write against an
// existing channel: membership + app-scope check, rate limit, then forward
// to the channel's home region if this instance isn't it.
func (a *App) handleCreatePoll(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	if !a.checkPollRateLimit(w, r, identity, quota.CapabilityPollCreate) {
		return
	}

	var req createPollRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}

	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" || len(req.Question) > maxPollQuestionLen {
		writeError(w, http.StatusBadRequest, "question is required (max 500 chars)")
		return
	}
	if len(req.Options) < minPollOptions || len(req.Options) > maxPollOptions {
		writeError(w, http.StatusBadRequest, "options must have between 2 and 10 entries")
		return
	}
	seen := make(map[string]bool, len(req.Options))
	labels := make([]string, len(req.Options))
	for i, opt := range req.Options {
		opt = strings.TrimSpace(opt)
		if opt == "" || len(opt) > maxPollOptionLabelLen {
			writeError(w, http.StatusBadRequest, "each option is required (max 200 chars)")
			return
		}
		key := strings.ToLower(opt)
		if seen[key] {
			writeError(w, http.StatusBadRequest, "options must be unique")
			return
		}
		seen[key] = true
		labels[i] = opt
	}

	var closesAt *time.Time
	if req.ClosesAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ClosesAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "closes_at must be an RFC3339 timestamp")
			return
		}
		if !t.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "closes_at must be in the future")
			return
		}
		utc := t.UTC()
		closesAt = &utc
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	p, err := a.pollsRepo.Create(r.Context(), pool, channelID, identity.UserID, req.Question, req.MultiSelect, closesAt, labels)
	if err != nil {
		a.log.Error("create poll", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create poll")
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusCreated, pollResponseFrom(p))
}

// handleGetPoll backs GET /channels/{id}/polls/{poll_id}. Reads don't
// forward to the home region, same as handleListMessages: poll storage is
// reachable from any instance in this local topology.
func (a *App) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	pollID, err := uuid.Parse(r.PathValue("poll_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid poll id")
		return
	}

	isMember, err := a.membershipRepo.IsMember(r.Context(), channelID, identity.UserID)
	if err != nil {
		a.log.Error("check membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check membership")
		return
	}
	if !isMember {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	p, err := a.pollsRepo.Get(r.Context(), pool, channelID, pollID, &identity.UserID)
	if err != nil {
		if errors.Is(err, polls.ErrNotFound) {
			writeError(w, http.StatusNotFound, "poll not found")
			return
		}
		a.log.Error("get poll", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load poll")
		return
	}

	writeJSON(w, http.StatusOK, pollResponseFrom(p))
}

type votePollRequest struct {
	OptionIDs []string `json:"option_ids"`
}

// handleVotePoll backs POST /channels/{id}/polls/{poll_id}/votes. Replaces
// the caller's ENTIRE vote for this poll with option_ids in one call —
// there's no separate per-option toggle: a single-select poll always sends
// exactly one id, a multi-select poll sends the caller's full current
// selection. To retract a vote entirely, DELETE the same URL instead of
// posting an empty list here.
func (a *App) handleVotePoll(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	pollID, err := uuid.Parse(r.PathValue("poll_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid poll id")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	if !a.checkPollRateLimit(w, r, identity, quota.CapabilityPollVote) {
		return
	}

	var req votePollRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.OptionIDs) == 0 {
		writeError(w, http.StatusBadRequest, "option_ids is required — DELETE this URL instead to retract your vote")
		return
	}
	optionIDs := make([]uuid.UUID, len(req.OptionIDs))
	for i, s := range req.OptionIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid option id")
			return
		}
		optionIDs[i] = id
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	p, err := a.pollsRepo.Vote(r.Context(), pool, channelID, pollID, identity.UserID, optionIDs)
	if err != nil {
		if !a.writePollVoteError(w, err) {
			a.log.Error("vote poll", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record vote")
		}
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusOK, pollVoteStateResponseFrom(p))
}

// handleClearPollVotes backs DELETE /channels/{id}/polls/{poll_id}/votes —
// always retracts the caller's *own* vote(s); there's no concept of
// clearing someone else's, same convention as handleRemoveReaction.
func (a *App) handleClearPollVotes(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	pollID, err := uuid.Parse(r.PathValue("poll_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid poll id")
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	if !a.checkPollRateLimit(w, r, identity, quota.CapabilityPollVote) {
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	p, err := a.pollsRepo.ClearVotes(r.Context(), pool, channelID, pollID, identity.UserID)
	if err != nil {
		if !a.writePollVoteError(w, err) {
			a.log.Error("clear poll votes", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to retract vote")
		}
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusOK, pollVoteStateResponseFrom(p))
}

// writePollVoteError maps Vote/ClearVotes' sentinel errors to the right
// status code and writes the response; returns false (and writes nothing)
// for anything else, so the caller falls through to its own 500 logging.
func (a *App) writePollVoteError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, polls.ErrNotFound):
		writeError(w, http.StatusNotFound, "poll not found")
	case errors.Is(err, polls.ErrPollClosed):
		writeError(w, http.StatusBadRequest, "poll is closed")
	case errors.Is(err, polls.ErrOptionNotFound):
		writeError(w, http.StatusBadRequest, "option not found in this poll")
	case errors.Is(err, polls.ErrSingleSelectViolation):
		writeError(w, http.StatusBadRequest, "this poll only allows selecting one option")
	case errors.Is(err, polls.ErrNoOptionsSelected):
		writeError(w, http.StatusBadRequest, "option_ids is required")
	default:
		return false
	}
	return true
}

// checkPollRateLimit is checkReactionRateLimit's shape for polls, shared by
// create/vote/clear-vote (with different capabilities) — checked before the
// region-forward decision, same as every other rate-limited write.
func (a *App) checkPollRateLimit(w http.ResponseWriter, r *http.Request, identity Identity, capability string) bool {
	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, capability, "rate:"+capability+":user:"+identity.UserID.String())
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(capability).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return false
	}
	return true
}
