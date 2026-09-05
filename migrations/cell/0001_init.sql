-- cell DB — one per shard, self-contained (docs/adr/0006-cell-based-tenant-routing.md).
--
-- Holds EVERY tenant-scoped table for the apps pinned to this cell: identity,
-- channels, membership, the user-centric channel index, private per-user
-- state (blocks, mutes, bookmarks), and the full message log with its
-- reactions/polls/threads/pins/translations. No cross-cell reads or writes;
-- an app's data never spans two cells.
--
-- app_id appears on tenant rows for defense-in-depth isolation but carries NO
-- foreign key: the `apps` table lives in the global config DB (a different
-- database — Postgres FKs cannot cross it), so app_id is a plain BIGINT
-- validated at the application layer, the same way message_id references
-- already are across the old shard/control boundary.
--
-- Consolidated fresh schema (was migrations/control/*.sql tenant tables +
-- migrations/shard/*.sql). home_region and virtual_shard are GONE — placement
-- is an app-level fact in the config DB now, not a per-channel one. Applied
-- identically to every cell's Postgres. Additive-only from here forward.

-- ─────────────────────────── identity & channels ───────────────────────────

CREATE TABLE users (
    user_id         UUID PRIMARY KEY,
    app_id          BIGINT NOT NULL,          -- config.apps(app_id), no cross-DB FK
    display_name    TEXT NOT NULL,
    -- Presence: touched only by real activity signals (WS connect/pong/
    -- disconnect, message-send/reaction/read handlers), never every request.
    -- is_online is derived at read time from recency, never stored.
    last_active_at  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_users_app ON users (app_id);

CREATE TABLE channels (
    channel_id   UUID PRIMARY KEY,
    app_id       BIGINT NOT NULL,             -- config.apps(app_id), no cross-DB FK
    name         TEXT NOT NULL,
    created_by   UUID NOT NULL REFERENCES users(user_id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_channels_app ON channels (app_id);

CREATE TABLE channel_members (
    channel_id                    UUID NOT NULL REFERENCES channels(channel_id),
    user_id                       UUID NOT NULL REFERENCES users(user_id),
    added_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- When the unread-reminders worker last notified this member, so its
    -- poll loop doesn't re-notify the same stale membership every cycle.
    last_unread_reminder_sent_at  TIMESTAMPTZ,
    PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX idx_channel_members_user ON channel_members (user_id);

-- user_id-keyed index so GET /users/me/channels hits one table, never a scan
-- across the message tables.
CREATE TABLE user_channels (
    user_id                UUID NOT NULL REFERENCES users(user_id),
    channel_id             UUID NOT NULL REFERENCES channels(channel_id),
    last_message_sequence  BIGINT NOT NULL DEFAULT 0,
    last_message_at        TIMESTAMPTZ,
    joined_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, channel_id)
);
CREATE INDEX idx_user_channels_last_message ON user_channels (user_id, last_message_at DESC);

-- ─────────────────────── private per-user relationships ─────────────────────

-- Blocking: directional ownership, bidirectional enforcement in realtime.
CREATE TABLE user_blocks (
    app_id           BIGINT NOT NULL,
    blocker_user_id  UUID NOT NULL REFERENCES users(user_id),
    blocked_user_id  UUID NOT NULL REFERENCES users(user_id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_user_id, blocked_user_id),
    CHECK (blocker_user_id != blocked_user_id)
);
CREATE INDEX idx_user_blocks_app ON user_blocks (app_id);
CREATE INDEX idx_user_blocks_blocked ON user_blocks (blocked_user_id);

-- Muting: personal, one-directional preference (not enforced in delivery).
CREATE TABLE channel_mutes (
    channel_id     UUID NOT NULL REFERENCES channels(channel_id),
    muter_user_id  UUID NOT NULL REFERENCES users(user_id),
    muted_user_id  UUID NOT NULL REFERENCES users(user_id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, muter_user_id, muted_user_id),
    CHECK (muter_user_id != muted_user_id)
);

-- Bookmarks: private, folder-organized saved messages. message_id has no FK
-- (validated at write time); folder is nullable / ON DELETE SET NULL.
CREATE TABLE bookmark_folders (
    folder_id   UUID PRIMARY KEY,
    app_id      BIGINT NOT NULL,
    user_id     UUID NOT NULL REFERENCES users(user_id),
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
CREATE INDEX idx_bookmark_folders_user ON bookmark_folders (user_id);

CREATE TABLE bookmarks (
    bookmark_id  UUID PRIMARY KEY,
    app_id       BIGINT NOT NULL,
    user_id      UUID NOT NULL REFERENCES users(user_id),
    channel_id   UUID NOT NULL REFERENCES channels(channel_id),
    message_id   UUID NOT NULL,               -- no FK: validated in application code
    folder_id    UUID REFERENCES bookmark_folders(folder_id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, channel_id, message_id)
);
CREATE INDEX idx_bookmarks_user ON bookmarks (user_id, created_at DESC);
CREATE INDEX idx_bookmarks_folder ON bookmarks (folder_id);

-- ─────────────────────────── the message log ───────────────────────────────
-- A channel's messages live entirely in its cell. Denormalized state
-- (reactions, reply/edit/pin flags, quotes, attachments, previews) lives on
-- the row so reads never join. All the incremental shard ALTERs are folded in.

CREATE TABLE messages (
    channel_id          UUID NOT NULL,
    sequence            BIGINT NOT NULL,
    message_id          UUID NOT NULL,
    sender_id           UUID NOT NULL,
    client_message_id   UUID NOT NULL,
    body                TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    reaction_counts     JSONB NOT NULL DEFAULT '{}'::jsonb,
    latest_reactions    JSONB NOT NULL DEFAULT '[]'::jsonb,
    parent_id           UUID,
    poll_id             UUID,
    reply_count         BIGINT NOT NULL DEFAULT 0,
    edited_at           TIMESTAMPTZ,
    pinned_at           TIMESTAMPTZ,
    pinned_by           UUID,
    quoted_message_id   UUID,
    attachments         JSONB NOT NULL DEFAULT '[]'::jsonb,
    link_preview        JSONB,
    location            JSONB,
    status              TEXT NOT NULL DEFAULT 'sent',

    PRIMARY KEY (channel_id, sequence),
    CONSTRAINT chk_messages_status CHECK (status IN ('sent', 'pending'))
);

-- Idempotency: a retried send with the same client_message_id returns the original.
CREATE UNIQUE INDEX idx_messages_client_message_id ON messages (channel_id, client_message_id);
-- message_id lookups + the target of every composite FK below.
CREATE UNIQUE INDEX idx_messages_channel_message ON messages (channel_id, message_id);

-- The reply self-FK is added AFTER idx_messages_channel_message: a composite
-- FK to (channel_id, message_id) requires that unique index to already exist.
ALTER TABLE messages ADD CONSTRAINT fk_messages_parent FOREIGN KEY (channel_id, parent_id) REFERENCES messages (channel_id, message_id);
CREATE INDEX idx_messages_channel_created_at ON messages (channel_id, created_at);
CREATE INDEX idx_messages_parent ON messages (channel_id, parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX idx_messages_poll ON messages (channel_id, poll_id) WHERE poll_id IS NOT NULL;
CREATE INDEX idx_messages_channel_pinned ON messages (channel_id, pinned_at DESC) WHERE pinned_at IS NOT NULL;
CREATE INDEX idx_messages_channel_pending ON messages (channel_id, sequence) WHERE status = 'pending';

-- Per-channel monotonic sequence allocation (SELECT ... FOR UPDATE in the send tx).
CREATE TABLE channel_sequences (
    channel_id     UUID PRIMARY KEY,
    last_sequence  BIGINT NOT NULL DEFAULT 0
);

-- Transactional outbox: written in the send tx, published by `chat worker`, then deleted.
CREATE TABLE outbox_events (
    event_id    UUID PRIMARY KEY,
    event_type  TEXT NOT NULL,
    channel_id  UUID NOT NULL,
    payload     JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_outbox_events_created_at ON outbox_events (created_at);

-- Reactions: canonical string keys, denormalized onto the message row.
CREATE TABLE message_reactions (
    channel_id    UUID NOT NULL,
    message_id    UUID NOT NULL,
    user_id       UUID NOT NULL,
    reaction      TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, message_id, user_id, reaction),
    FOREIGN KEY (channel_id, message_id) REFERENCES messages (channel_id, message_id)
);
CREATE INDEX idx_message_reactions_message ON message_reactions (channel_id, message_id, created_at);

-- Read receipts: a per-(channel, user) watermark.
CREATE TABLE channel_read_state (
    channel_id          UUID NOT NULL,
    user_id             UUID NOT NULL,
    last_read_sequence  BIGINT NOT NULL DEFAULT 0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, user_id)
);

-- Polls: separate entity, message points at it via messages.poll_id.
CREATE TABLE polls (
    channel_id    UUID NOT NULL,
    poll_id       UUID NOT NULL,
    creator_id    UUID NOT NULL,
    question      TEXT NOT NULL,
    multi_select  BOOLEAN NOT NULL DEFAULT false,
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
CREATE INDEX idx_poll_votes_poll ON poll_votes (channel_id, poll_id);
CREATE INDEX idx_poll_votes_poll_user ON poll_votes (channel_id, poll_id, user_id);

-- The message->poll composite FK (added after polls exists).
ALTER TABLE messages ADD CONSTRAINT fk_messages_poll FOREIGN KEY (channel_id, poll_id) REFERENCES polls (channel_id, poll_id);

-- Message reminders: polled and delivered by `chat worker`.
CREATE TABLE message_reminders (
    reminder_id   UUID NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    channel_id    UUID NOT NULL,
    message_id    UUID NOT NULL,
    user_id       UUID NOT NULL,
    remind_at     TIMESTAMPTZ NOT NULL,
    delivered_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_message_reminders_due ON message_reminders (remind_at) WHERE delivered_at IS NULL;

-- Translation cache (per message + target language). Not the billing record.
CREATE TABLE message_translations (
    channel_id       UUID NOT NULL,
    message_id       UUID NOT NULL,
    target_lang      TEXT NOT NULL,
    source_lang      TEXT NOT NULL,
    translated_body  TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, message_id, target_lang),
    FOREIGN KEY (channel_id, message_id) REFERENCES messages (channel_id, message_id)
);
