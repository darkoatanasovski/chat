-- Threads/replies: a message can reply to at most one other message, in
-- the same channel, via parent_id. There is no separate "thread" entity —
-- a thread is just the set of messages reachable by following parent_id
-- links from some root (a message with parent_id IS NULL), matching how
-- little INSTRUCTIONS.md's additive-package precedent (reactions, presence)
-- needed for features that hang off an existing message rather than
-- introducing a new one.
--
-- FK is composite (channel_id, parent_id) -> messages(channel_id,
-- message_id), reusing idx_messages_channel_message
-- (migrations/shard/0002_reactions.sql) rather than a bare message_id FK —
-- the same reasoning message_reactions already uses: a reply can only ever
-- point at a message in its own channel, and the composite FK makes that a
-- database-enforced invariant instead of an application-level assumption.
--
-- How deep a reply chain is allowed to go is NOT enforced here: nesting
-- itself is unbounded at the schema level (parent_id can chain arbitrarily
-- far), and how far a given app's callers may actually go is a runtime
-- check against apps.max_thread_depth (migrations/control's app_thread
-- migration) — see internal/messages.Repo.Send's parent-depth walk. This
-- mirrors how quota limits are enforced in application code against
-- config, never baked into the schema itself.
ALTER TABLE messages ADD COLUMN parent_id UUID;
ALTER TABLE messages ADD CONSTRAINT fk_messages_parent
    FOREIGN KEY (channel_id, parent_id) REFERENCES messages (channel_id, message_id);

-- Partial (parent_id IS NOT NULL costs nothing on the majority of rows,
-- which are top-level messages) — supports both "does this parent exist /
-- what's its chain" walks in Send and, later, an efficient "list replies to
-- this message" query if one gets added.
CREATE INDEX idx_messages_parent ON messages (channel_id, parent_id) WHERE parent_id IS NOT NULL;
