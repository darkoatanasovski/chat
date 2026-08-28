-- Reactions on messages, plus denormalized counts/latest-reactions on the
-- message row itself so the UI never needs a join to render them
-- (recomputed transactionally on every add/remove — see internal/reactions).

ALTER TABLE messages ADD COLUMN reaction_counts JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE messages ADD COLUMN latest_reactions JSONB NOT NULL DEFAULT '[]'::jsonb;

-- messages' primary key is (channel_id, sequence); reaction handlers only
-- ever have message_id (from the URL). This unique index is both what makes
-- every reaction query an index lookup instead of a per-shard seq scan, and
-- what message_reactions' foreign key below references, so a reaction can
-- never be recorded against a message_id that doesn't actually exist.
CREATE UNIQUE INDEX idx_messages_channel_message ON messages (channel_id, message_id);

CREATE TABLE message_reactions (
    channel_id    UUID NOT NULL,
    message_id    UUID NOT NULL,
    user_id       UUID NOT NULL,
    emoji         TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, message_id, user_id, emoji),
    FOREIGN KEY (channel_id, message_id) REFERENCES messages (channel_id, message_id)
);

-- Backs both "recompute this message's counts" and "recompute this
-- message's latest N reactions" (ORDER BY created_at DESC LIMIT N).
CREATE INDEX idx_message_reactions_message ON message_reactions (channel_id, message_id, created_at);
