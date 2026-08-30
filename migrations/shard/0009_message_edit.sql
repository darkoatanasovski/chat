-- edited_at is NULL for a message that's never been edited, or the UTC
-- timestamp of its most recent edit (internal/messages.Repo.Edit overwrites
-- body and this column together, in one UPDATE, every time). No edit
-- history is kept — only the current body and when it last changed, the
-- same "denormalize the current state, not a log" choice this schema
-- already makes for reaction_counts/latest_reactions rather than replaying
-- message_reactions.
ALTER TABLE messages ADD COLUMN edited_at TIMESTAMPTZ;
