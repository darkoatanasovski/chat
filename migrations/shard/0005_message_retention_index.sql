-- Backs the per-shard retention sweep's DeleteExpiredBefore (cmd/worker):
-- find/delete a channel's oldest-first messages older than its plan's
-- retention cutoff without scanning the whole channel. The existing
-- primary key (channel_id, sequence) doesn't help here since the cutoff is
-- on created_at, not sequence.
CREATE INDEX idx_messages_channel_created_at ON messages (channel_id, created_at);
