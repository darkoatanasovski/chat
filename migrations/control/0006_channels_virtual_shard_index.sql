-- Backs channels.Repo.ListByVirtualShardRange, which the per-shard retention
-- sweep (cmd/worker) uses to page through exactly the channels whose
-- messages live on its own shard, without loading the whole channels table.
CREATE INDEX idx_channels_virtual_shard ON channels (virtual_shard, channel_id);
