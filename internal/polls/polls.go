// Package polls owns the per-shard poll entity: creation, reading, and
// voting. A poll is deliberately NOT denormalized onto the message row the
// way reactions are (internal/reactions) — it's a genuinely separate
// resource with its own primary key, fetched with its own GET, that a
// message merely points at via messages.poll_id (like a reply points at
// its parent via parent_id). Every call takes an explicit *pgxpool.Pool for
// the physical shard internal/routing resolved — this package never decides
// which shard to talk to, same convention as internal/messages and
// internal/reactions.
package polls

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/events"
)

var (
	ErrNotFound       = errors.New("poll not found")
	ErrOptionNotFound = errors.New("option not found in this poll")
	ErrPollClosed     = errors.New("poll is closed")
	// ErrNoOptionsSelected and ErrSingleSelectViolation are Vote's own
	// defense-in-depth checks — cmd/api's handler validates option_ids the
	// same way before ever calling Vote (so it can return a helpful 400
	// message), but Vote re-checks against the poll's actual multi_select
	// column rather than trusting the caller passed a value consistent with
	// it, the same "never rely on a caller-supplied invariant implicitly"
	// discipline used elsewhere in this codebase (see
	// checkChannelWriteAccess's app_id re-check).
	ErrNoOptionsSelected    = errors.New("at least one option_id is required")
	ErrSingleSelectViolation = errors.New("this poll only allows selecting one option")
)

// querier is the narrow read surface both *pgxpool.Pool and pgx.Tx satisfy —
// lets listOptions/countDistinctVoters/votesFor run either as a plain read
// (Get, called with a pool) or inside an already-open write transaction
// (ClearVotes' no-op path, called with a tx) without duplicating the query.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Option struct {
	OptionID  uuid.UUID
	Position  int
	Label     string
	VoteCount int
}

type Poll struct {
	ChannelID   uuid.UUID
	PollID      uuid.UUID
	CreatorID   uuid.UUID
	Question    string
	MultiSelect bool
	// ClosesAt is nil for a poll that never closes on its own.
	ClosesAt  *time.Time
	CreatedAt time.Time
	Options   []Option
	// TotalVoters is the count of distinct users who have voted at least
	// once, not the sum of per-option vote_count (which double-counts a
	// multi_select voter who picked more than one option).
	TotalVoters int
	// VotedOptionIDs is the calling user's own current vote(s) — populated
	// only by Get (which takes a viewerID), empty from Create/Vote/ClearVotes
	// since the caller there already knows what it just voted.
	VotedOptionIDs []uuid.UUID
}

type Repo struct{}

func NewRepo() *Repo { return &Repo{} }

func isClosed(closesAt *time.Time, now time.Time) bool {
	return closesAt != nil && closesAt.Before(now)
}

// Create inserts a poll and its options in one transaction. optionLabels is
// trusted to already be validated (count within bounds, each non-empty, no
// case-insensitive duplicates) — cmd/api owns that check, same division of
// labor as reactions.ValidReactions being checked by the handler, not here.
func (r *Repo) Create(ctx context.Context, pool *pgxpool.Pool, channelID, creatorID uuid.UUID, question string, multiSelect bool, closesAt *time.Time, optionLabels []string) (Poll, error) {
	pollID, err := uuid.NewV7()
	if err != nil {
		return Poll{}, fmt.Errorf("polls: generate poll id: %w", err)
	}
	now := time.Now().UTC()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Poll{}, fmt.Errorf("polls: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO polls (channel_id, poll_id, creator_id, question, multi_select, closes_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, channelID, pollID, creatorID, question, multiSelect, closesAt, now); err != nil {
		return Poll{}, fmt.Errorf("polls: insert poll: %w", err)
	}

	options := make([]Option, len(optionLabels))
	for i, label := range optionLabels {
		optionID, err := uuid.NewV7()
		if err != nil {
			return Poll{}, fmt.Errorf("polls: generate option id: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO poll_options (channel_id, poll_id, option_id, position, label, vote_count)
			VALUES ($1, $2, $3, $4, $5, 0)
		`, channelID, pollID, optionID, i, label); err != nil {
			return Poll{}, fmt.Errorf("polls: insert option: %w", err)
		}
		options[i] = Option{OptionID: optionID, Position: i, Label: label, VoteCount: 0}
	}

	if err := tx.Commit(ctx); err != nil {
		return Poll{}, fmt.Errorf("polls: commit: %w", err)
	}

	return Poll{
		ChannelID: channelID, PollID: pollID, CreatorID: creatorID, Question: question,
		MultiSelect: multiSelect, ClosesAt: closesAt, CreatedAt: now, Options: options,
	}, nil
}

// Exists is the lightweight existence check cmd/api's handleSendMessage
// runs before attaching a poll_id to a message — mirrors the clean 404 that
// internal/messages.checkThreadDepth gets for a bad parent_id, rather than
// relying on the messages.poll_id foreign key violation turning into a
// raw 500.
func (r *Repo) Exists(ctx context.Context, pool *pgxpool.Pool, channelID, pollID uuid.UUID) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM polls WHERE channel_id = $1 AND poll_id = $2)`, channelID, pollID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("polls: exists: %w", err)
	}
	return exists, nil
}

// Get loads a poll with its options (in position order, current
// vote_count), the distinct-voter total, and — when viewerID is non-nil —
// that viewer's own current vote(s).
func (r *Repo) Get(ctx context.Context, pool *pgxpool.Pool, channelID, pollID uuid.UUID, viewerID *uuid.UUID) (Poll, error) {
	var p Poll
	err := pool.QueryRow(ctx, `
		SELECT channel_id, poll_id, creator_id, question, multi_select, closes_at, created_at
		FROM polls WHERE channel_id = $1 AND poll_id = $2
	`, channelID, pollID).Scan(&p.ChannelID, &p.PollID, &p.CreatorID, &p.Question, &p.MultiSelect, &p.ClosesAt, &p.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Poll{}, ErrNotFound
		}
		return Poll{}, fmt.Errorf("polls: get: %w", err)
	}

	options, err := r.listOptions(ctx, pool, channelID, pollID)
	if err != nil {
		return Poll{}, err
	}
	p.Options = options

	totalVoters, err := r.countDistinctVoters(ctx, pool, channelID, pollID)
	if err != nil {
		return Poll{}, err
	}
	p.TotalVoters = totalVoters

	if viewerID != nil {
		voted, err := r.votesFor(ctx, pool, channelID, pollID, *viewerID)
		if err != nil {
			return Poll{}, err
		}
		p.VotedOptionIDs = voted
	}
	return p, nil
}

func (r *Repo) listOptions(ctx context.Context, q querier, channelID, pollID uuid.UUID) ([]Option, error) {
	rows, err := q.Query(ctx, `
		SELECT option_id, position, label, vote_count
		FROM poll_options WHERE channel_id = $1 AND poll_id = $2
		ORDER BY position
	`, channelID, pollID)
	if err != nil {
		return nil, fmt.Errorf("polls: list options: %w", err)
	}
	defer rows.Close()

	var out []Option
	for rows.Next() {
		var o Option
		if err := rows.Scan(&o.OptionID, &o.Position, &o.Label, &o.VoteCount); err != nil {
			return nil, fmt.Errorf("polls: list options: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *Repo) countDistinctVoters(ctx context.Context, q querier, channelID, pollID uuid.UUID) (int, error) {
	var count int
	err := q.QueryRow(ctx, `
		SELECT count(DISTINCT user_id) FROM poll_votes WHERE channel_id = $1 AND poll_id = $2
	`, channelID, pollID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("polls: count distinct voters: %w", err)
	}
	return count, nil
}

func (r *Repo) votesFor(ctx context.Context, q querier, channelID, pollID, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		SELECT option_id FROM poll_votes WHERE channel_id = $1 AND poll_id = $2 AND user_id = $3
	`, channelID, pollID, userID)
	if err != nil {
		return nil, fmt.Errorf("polls: votes for: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("polls: votes for: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Vote replaces userID's entire vote for pollID with optionIDs in one
// transaction: delete whatever they'd previously voted, insert the new
// set, recompute every option's vote_count, write it back, and emit a
// PollVoteUpdated event carrying the fresh tallies — same
// "recompute + write back + emit, all in the write transaction" shape as
// internal/reactions.Repo.Add. optionIDs is trusted to already be
// validated (non-empty, deduplicated, single entry unless multi_select,
// each one actually belonging to this poll — the last of which this
// method still enforces via poll_options' foreign key, surfaced as
// ErrOptionNotFound rather than a raw constraint violation).
func (r *Repo) Vote(ctx context.Context, pool *pgxpool.Pool, channelID, pollID, userID uuid.UUID, optionIDs []uuid.UUID) (Poll, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Poll{}, fmt.Errorf("polls: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if len(optionIDs) == 0 {
		return Poll{}, ErrNoOptionsSelected
	}

	var closesAt *time.Time
	var multiSelect bool
	if err := tx.QueryRow(ctx, `SELECT closes_at, multi_select FROM polls WHERE channel_id = $1 AND poll_id = $2`, channelID, pollID).Scan(&closesAt, &multiSelect); err != nil {
		if err == pgx.ErrNoRows {
			return Poll{}, ErrNotFound
		}
		return Poll{}, fmt.Errorf("polls: load poll: %w", err)
	}
	if isClosed(closesAt, time.Now().UTC()) {
		return Poll{}, ErrPollClosed
	}
	if !multiSelect && len(optionIDs) > 1 {
		return Poll{}, ErrSingleSelectViolation
	}

	if _, err := tx.Exec(ctx, `DELETE FROM poll_votes WHERE channel_id = $1 AND poll_id = $2 AND user_id = $3`, channelID, pollID, userID); err != nil {
		return Poll{}, fmt.Errorf("polls: clear previous votes: %w", err)
	}

	now := time.Now().UTC()
	for _, optionID := range optionIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO poll_votes (channel_id, poll_id, option_id, user_id, created_at)
			VALUES ($1, $2, $3, $4, $5)
		`, channelID, pollID, optionID, userID, now); err != nil {
			if isForeignKeyViolation(err) {
				return Poll{}, ErrOptionNotFound
			}
			return Poll{}, fmt.Errorf("polls: insert vote: %w", err)
		}
	}

	p, err := r.recomputeAndEmit(ctx, tx, channelID, pollID, userID)
	if err != nil {
		return Poll{}, err
	}
	p.VotedOptionIDs = optionIDs

	if err := tx.Commit(ctx); err != nil {
		return Poll{}, fmt.Errorf("polls: commit: %w", err)
	}
	return p, nil
}

// ClearVotes retracts userID's entire vote for pollID (idempotent: no
// existing vote is a no-op, still returns current state).
func (r *Repo) ClearVotes(ctx context.Context, pool *pgxpool.Pool, channelID, pollID, userID uuid.UUID) (Poll, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Poll{}, fmt.Errorf("polls: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var closesAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT closes_at FROM polls WHERE channel_id = $1 AND poll_id = $2`, channelID, pollID).Scan(&closesAt); err != nil {
		if err == pgx.ErrNoRows {
			return Poll{}, ErrNotFound
		}
		return Poll{}, fmt.Errorf("polls: load poll: %w", err)
	}
	if isClosed(closesAt, time.Now().UTC()) {
		return Poll{}, ErrPollClosed
	}

	tag, err := tx.Exec(ctx, `DELETE FROM poll_votes WHERE channel_id = $1 AND poll_id = $2 AND user_id = $3`, channelID, pollID, userID)
	if err != nil {
		return Poll{}, fmt.Errorf("polls: clear votes: %w", err)
	}

	if tag.RowsAffected() == 0 {
		// Nothing changed: return current state without recomputing/writing
		// vote_count again or emitting a redundant event — same
		// "changed=false, unchanged state" shape as reactions.Repo.Remove on
		// a reaction that wasn't there.
		options, err := r.listOptions(ctx, tx, channelID, pollID)
		if err != nil {
			return Poll{}, err
		}
		totalVoters, err := r.countDistinctVoters(ctx, tx, channelID, pollID)
		if err != nil {
			return Poll{}, err
		}
		return Poll{ChannelID: channelID, PollID: pollID, Options: options, TotalVoters: totalVoters}, tx.Commit(ctx)
	}

	p, err := r.recomputeAndEmit(ctx, tx, channelID, pollID, userID)
	if err != nil {
		return Poll{}, err
	}
	p.VotedOptionIDs = nil

	if err := tx.Commit(ctx); err != nil {
		return Poll{}, fmt.Errorf("polls: commit: %w", err)
	}
	return p, nil
}

// recomputeAndEmit recomputes every option's vote_count and the distinct-
// voter total from poll_votes, writes the counts back onto poll_options,
// and inserts the outbox event carrying that fresh state — called by both
// Vote and ClearVotes since both need identical recompute-and-announce
// behavior after touching poll_votes.
func (r *Repo) recomputeAndEmit(ctx context.Context, tx pgx.Tx, channelID, pollID, actorID uuid.UUID) (Poll, error) {
	rows, err := tx.Query(ctx, `
		SELECT o.option_id, o.position, o.label, count(v.user_id)
		FROM poll_options o
		LEFT JOIN poll_votes v ON v.channel_id = o.channel_id AND v.poll_id = o.poll_id AND v.option_id = o.option_id
		WHERE o.channel_id = $1 AND o.poll_id = $2
		GROUP BY o.option_id, o.position, o.label
		ORDER BY o.position
	`, channelID, pollID)
	if err != nil {
		return Poll{}, fmt.Errorf("polls: recompute options: %w", err)
	}
	var options []Option
	for rows.Next() {
		var o Option
		if err := rows.Scan(&o.OptionID, &o.Position, &o.Label, &o.VoteCount); err != nil {
			rows.Close()
			return Poll{}, fmt.Errorf("polls: recompute options: %w", err)
		}
		options = append(options, o)
	}
	if err := rows.Err(); err != nil {
		return Poll{}, fmt.Errorf("polls: recompute options: %w", err)
	}
	rows.Close()

	for _, o := range options {
		if _, err := tx.Exec(ctx, `
			UPDATE poll_options SET vote_count = $1 WHERE channel_id = $2 AND poll_id = $3 AND option_id = $4
		`, o.VoteCount, channelID, pollID, o.OptionID); err != nil {
			return Poll{}, fmt.Errorf("polls: write back vote_count: %w", err)
		}
	}

	var totalVoters int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT user_id) FROM poll_votes WHERE channel_id = $1 AND poll_id = $2
	`, channelID, pollID).Scan(&totalVoters); err != nil {
		return Poll{}, fmt.Errorf("polls: count distinct voters: %w", err)
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return Poll{}, fmt.Errorf("polls: generate event id: %w", err)
	}
	tallies := make([]events.PollOptionTally, len(options))
	for i, o := range options {
		tallies[i] = events.PollOptionTally{OptionID: o.OptionID, VoteCount: o.VoteCount}
	}
	payload := events.PollVoteUpdatedPayload{
		EventID: eventID, ChannelID: channelID, PollID: pollID, ActorID: actorID,
		Options: tallies, TotalVoters: totalVoters,
	}
	if err := events.InsertOutboxWithID(ctx, tx, eventID, events.TopicPollVoteUpdated, channelID, payload); err != nil {
		return Poll{}, err
	}

	return Poll{ChannelID: channelID, PollID: pollID, Options: options, TotalVoters: totalVoters}, nil
}

// ListByChannels backs the dashboard's polls view: the most recent polls
// (with options and current tallies) across a set of channels, newest
// first — same "bounded scatter-gather over the small, fixed number of
// physical shards" exception as internal/messages.SumSequencesByChannels,
// never called on a request path, only this low-frequency admin read.
// Unlike Get, this never populates VotedOptionIDs — there's no single
// "viewer" for an org-wide admin list.
func (r *Repo) ListByChannels(ctx context.Context, pool *pgxpool.Pool, channelIDs []uuid.UUID, limit int) ([]Poll, error) {
	if len(channelIDs) == 0 {
		return nil, nil
	}

	rows, err := pool.Query(ctx, `
		SELECT channel_id, poll_id, creator_id, question, multi_select, closes_at, created_at
		FROM polls WHERE channel_id = ANY($1)
		ORDER BY created_at DESC
		LIMIT $2
	`, channelIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("polls: list by channels: %w", err)
	}
	var out []Poll
	pollIDs := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var p Poll
		if err := rows.Scan(&p.ChannelID, &p.PollID, &p.CreatorID, &p.Question, &p.MultiSelect, &p.ClosesAt, &p.CreatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("polls: list by channels: %w", err)
		}
		out = append(out, p)
		pollIDs = append(pollIDs, p.PollID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("polls: list by channels: %w", err)
	}
	rows.Close()
	if len(out) == 0 {
		return nil, nil
	}

	optionsByPoll, err := r.listOptionsByPolls(ctx, pool, channelIDs, pollIDs)
	if err != nil {
		return nil, err
	}
	votersByPoll, err := r.countDistinctVotersByPolls(ctx, pool, channelIDs, pollIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Options = optionsByPoll[out[i].PollID]
		out[i].TotalVoters = votersByPoll[out[i].PollID]
	}
	return out, nil
}

func (r *Repo) listOptionsByPolls(ctx context.Context, pool *pgxpool.Pool, channelIDs, pollIDs []uuid.UUID) (map[uuid.UUID][]Option, error) {
	rows, err := pool.Query(ctx, `
		SELECT poll_id, option_id, position, label, vote_count
		FROM poll_options WHERE channel_id = ANY($1) AND poll_id = ANY($2)
		ORDER BY poll_id, position
	`, channelIDs, pollIDs)
	if err != nil {
		return nil, fmt.Errorf("polls: list options by polls: %w", err)
	}
	defer rows.Close()

	out := map[uuid.UUID][]Option{}
	for rows.Next() {
		var pollID uuid.UUID
		var o Option
		if err := rows.Scan(&pollID, &o.OptionID, &o.Position, &o.Label, &o.VoteCount); err != nil {
			return nil, fmt.Errorf("polls: list options by polls: %w", err)
		}
		out[pollID] = append(out[pollID], o)
	}
	return out, rows.Err()
}

func (r *Repo) countDistinctVotersByPolls(ctx context.Context, pool *pgxpool.Pool, channelIDs, pollIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	rows, err := pool.Query(ctx, `
		SELECT poll_id, count(DISTINCT user_id)
		FROM poll_votes WHERE channel_id = ANY($1) AND poll_id = ANY($2)
		GROUP BY poll_id
	`, channelIDs, pollIDs)
	if err != nil {
		return nil, fmt.Errorf("polls: count distinct voters by polls: %w", err)
	}
	defer rows.Close()

	out := map[uuid.UUID]int{}
	for rows.Next() {
		var pollID uuid.UUID
		var count int
		if err := rows.Scan(&pollID, &count); err != nil {
			return nil, fmt.Errorf("polls: count distinct voters by polls: %w", err)
		}
		out[pollID] = count
	}
	return out, rows.Err()
}

const pgForeignKeyViolation = "23503"

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation
}
