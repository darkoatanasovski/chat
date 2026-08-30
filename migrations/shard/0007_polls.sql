-- Polls: a separate entity (not denormalized onto messages, unlike
-- reactions) that a message can optionally point at via messages.poll_id —
-- symmetric to how a reply points at its parent via messages.parent_id, but
-- a poll's own lifecycle (options, votes, tallies) lives entirely in these
-- three tables and is fetched separately (GET .../polls/{poll_id}), never
-- joined into a message read.

CREATE TABLE polls (
    channel_id    UUID NOT NULL,
    poll_id       UUID NOT NULL,
    creator_id    UUID NOT NULL,
    question      TEXT NOT NULL,
    -- multi_select governs whether POST .../votes may carry more than one
    -- option_id — enforced in cmd/api's handler (mirrors reactions.ValidReactions
    -- being validated at the API layer, not the repo).
    multi_select  BOOLEAN NOT NULL DEFAULT false,
    -- NULL means the poll never closes on its own. Once in the past, votes
    -- and vote-retractions are rejected (internal/polls.ErrPollClosed) —
    -- checked live on every vote, never swept/cached.
    closes_at     TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, poll_id)
);

CREATE TABLE poll_options (
    channel_id    UUID NOT NULL,
    poll_id       UUID NOT NULL,
    option_id     UUID NOT NULL,
    position      SMALLINT NOT NULL,
    label         TEXT NOT NULL,
    -- vote_count is denormalized and recomputed transactionally on every
    -- vote/retract (internal/polls.Repo, same "recompute + write back in
    -- the same tx" shape as message_reactions -> messages.reaction_counts)
    -- so reading a poll never has to COUNT(*) poll_votes itself.
    vote_count    INT NOT NULL DEFAULT 0,

    PRIMARY KEY (channel_id, poll_id, option_id),
    FOREIGN KEY (channel_id, poll_id) REFERENCES polls (channel_id, poll_id)
);

CREATE TABLE poll_votes (
    channel_id    UUID NOT NULL,
    poll_id       UUID NOT NULL,
    option_id     UUID NOT NULL,
    user_id       UUID NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, poll_id, option_id, user_id),
    FOREIGN KEY (channel_id, poll_id, option_id) REFERENCES poll_options (channel_id, poll_id, option_id)
);

-- Backs both "how many people voted on this poll" (distinct user_id) and
-- "what did this one user vote for" (GET .../polls/{poll_id}'s
-- voted_option_ids for the caller) without a poll_options join.
CREATE INDEX idx_poll_votes_poll ON poll_votes (channel_id, poll_id);
CREATE INDEX idx_poll_votes_poll_user ON poll_votes (channel_id, poll_id, user_id);

-- A message optionally points at a poll — nullable, never reassigned after
-- creation, always in the same channel (composite FK, same shape as
-- parent_id's). There's no uniqueness constraint tying a poll to exactly
-- one message: a poll can be created standalone and attached later, or
-- (uncommon but harmless) referenced by more than one message.
ALTER TABLE messages ADD COLUMN poll_id UUID;
ALTER TABLE messages ADD CONSTRAINT fk_messages_poll FOREIGN KEY (channel_id, poll_id) REFERENCES polls (channel_id, poll_id);
CREATE INDEX idx_messages_poll ON messages (channel_id, poll_id) WHERE poll_id IS NOT NULL;
