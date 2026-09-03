-- channel_mutes: the "mutes" capability. Structurally identical to
-- user_blocks (migrations/control/0004) — control-plane, since it's a
-- (channel, user, user) relationship exactly like channel_members — but
-- deliberately NOT enforced bidirectionally or wired into delivery
-- filtering the way blocks are: muting is a personal, one-directional
-- preference ("I don't want to see their messages"), not a mutual
-- barrier, and doesn't stop the muted user from seeing or being delivered
-- your own messages. internal/mutes only owns the relationship; a client
-- uses ListMutedFor to filter its own view (or ignore the noise from a
-- muted sender's push/badge counts).
CREATE TABLE channel_mutes (
    channel_id     UUID NOT NULL REFERENCES channels(channel_id),
    muter_user_id  UUID NOT NULL REFERENCES users(user_id),
    muted_user_id  UUID NOT NULL REFERENCES users(user_id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, muter_user_id, muted_user_id),
    CHECK (muter_user_id != muted_user_id)
);

-- last_unread_reminder_sent_at: when this member was last sent an
-- "unread.reminder" realtime event by the unread-reminders worker (see
-- cmd/worker/reminders.go) — lets that poll loop avoid re-notifying the
-- same stale membership every cycle. Null until the first reminder fires.
ALTER TABLE channel_members ADD COLUMN last_unread_reminder_sent_at TIMESTAMPTZ NULL;
