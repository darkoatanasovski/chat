-- message_reminders: the "message_reminders" capability. One row per
-- (message, user) reminder request; shard-scoped like messages/reactions
-- since it's keyed off a single message in a single channel, unlike
-- bookmarks which had to live in the control plane to be listable across
-- channels/regions at once (a reminder is never listed that way — it's
-- only ever polled by the worker that owns delivering it, and read back
-- by its own creator one at a time).
CREATE TABLE message_reminders (
    reminder_id   UUID NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    channel_id    UUID NOT NULL,
    message_id    UUID NOT NULL,
    user_id       UUID NOT NULL,
    remind_at     TIMESTAMPTZ NOT NULL,
    delivered_at  TIMESTAMPTZ NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Poll target for cmd/worker/reminders.go: due, not-yet-delivered
-- reminders, oldest first.
CREATE INDEX idx_message_reminders_due ON message_reminders (remind_at) WHERE delivered_at IS NULL;
