-- Read receipts: a per-(channel, user) watermark, not a log of every read
-- event — "Alice has read up to sequence N" is all the UI needs to compute
-- "seen" for any message (compare its sequence against each member's
-- watermark). Lives on the shard DB, same as messages, since it's compared
-- directly against messages.sequence for that channel.
CREATE TABLE channel_read_state (
    channel_id          UUID NOT NULL,
    user_id              UUID NOT NULL,
    last_read_sequence   BIGINT NOT NULL DEFAULT 0,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, user_id)
);
