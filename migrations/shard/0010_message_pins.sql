-- Pinning: channel-shared, denormalized directly on the message row itself
-- (migrations/shard/0002_reactions.sql, 0008_reply_count.sql, and
-- 0009_message_edit.sql all follow this same "keep it on the row, never
-- join to render it" discipline) rather than a separate pins table the way
-- polls are (migrations/shard/0007_polls.sql). A pin has no lifecycle of
-- its own independent of the message it's on — nothing about "pinned"
-- needs its own id, options, or history beyond "is it pinned right now,
-- and by whom" — so unlike polls it doesn't earn a separate entity.
--
-- pinned_at doubles as both the pin flag (NULL = not pinned) and the sort
-- key for listing a channel's pinned messages newest-pinned-first, the
-- same role edited_at plays for "has this been edited."
ALTER TABLE messages ADD COLUMN pinned_at TIMESTAMPTZ;
ALTER TABLE messages ADD COLUMN pinned_by UUID;

-- Partial index: only rows that are actually pinned are ever indexed here,
-- and every channel's pinned set is small by construction (pinning is a
-- deliberate per-message action, not something that accumulates the way
-- messages themselves do) — so GET /channels/{id}/pinned-messages
-- (internal/messages.Repo.ListPinned) stays a cheap index scan no matter
-- how large the channel's full message history grows.
CREATE INDEX idx_messages_channel_pinned ON messages (channel_id, pinned_at DESC) WHERE pinned_at IS NOT NULL;
