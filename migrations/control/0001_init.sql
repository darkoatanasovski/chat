-- Control-plane schema: users, channels, membership, user-centric channel index.
-- Additive-only migrations from here forward (see docs/operations/migrations.md).

CREATE TABLE users (
    user_id       UUID PRIMARY KEY,
    display_name  TEXT NOT NULL,
    home_region   TEXT NOT NULL,
    tier          TEXT NOT NULL DEFAULT 'FREE',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE channels (
    channel_id     UUID PRIMARY KEY,
    name           TEXT NOT NULL,
    home_region    TEXT NOT NULL,
    virtual_shard  INT NOT NULL,
    created_by     UUID NOT NULL REFERENCES users(user_id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Lookup path: channel_id -> home_region / virtual_shard. Small, hot, cached in
-- Redis by internal/routing; this table is the fallback-of-record on cache miss.
CREATE INDEX idx_channels_home_region ON channels (home_region);

CREATE TABLE channel_members (
    channel_id  UUID NOT NULL REFERENCES channels(channel_id),
    user_id     UUID NOT NULL REFERENCES users(user_id),
    added_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, user_id)
);

-- Reverse index so membership cache invalidation / fanout membership checks
-- don't require a channel-shard scan.
CREATE INDEX idx_channel_members_user ON channel_members (user_id);

-- user_id-keyed access pattern (see internal/routing UserIndex + INSTRUCTIONS.md §13):
-- GET /users/me/channels must hit this table only, never scatter across message shards.
CREATE TABLE user_channels (
    user_id                UUID NOT NULL REFERENCES users(user_id),
    channel_id             UUID NOT NULL REFERENCES channels(channel_id),
    last_message_sequence  BIGINT NOT NULL DEFAULT 0,
    last_message_at        TIMESTAMPTZ,
    joined_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX idx_user_channels_last_message ON user_channels (user_id, last_message_at DESC);
