-- Shard schema: applied identically to every physical message shard
-- (shard-a, shard-b, ...). A channel's rows live entirely on one shard,
-- selected by internal/routing via hash(channel_id) % virtual_shard_count.

CREATE TABLE messages (
    channel_id          UUID NOT NULL,
    sequence             BIGINT NOT NULL,
    message_id           UUID NOT NULL,
    sender_id             UUID NOT NULL,
    client_message_id    UUID NOT NULL,
    body                  TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, sequence)
);

-- Idempotency: a retried SendMessage with the same client_message_id must return
-- the original row rather than create a duplicate (INSTRUCTIONS.md §19).
CREATE UNIQUE INDEX idx_messages_client_message_id ON messages (channel_id, client_message_id);

-- Per-channel monotonic sequence allocation. One row per channel that has ever
-- had a message sent; updated with SELECT ... FOR UPDATE inside the send
-- transaction so sequence assignment is shard-local and lock-scoped to a single
-- channel, never a table-wide lock.
CREATE TABLE channel_sequences (
    channel_id      UUID PRIMARY KEY,
    last_sequence   BIGINT NOT NULL DEFAULT 0
);

-- Transactional outbox (INSTRUCTIONS.md §16). Written in the same transaction as
-- the message insert; polled and published by cmd/worker, then deleted.
CREATE TABLE outbox_events (
    event_id      UUID PRIMARY KEY,
    event_type    TEXT NOT NULL,
    channel_id    UUID NOT NULL,
    payload       JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_outbox_events_created_at ON outbox_events (created_at);
