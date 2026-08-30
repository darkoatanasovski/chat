-- Bookmarks: private per-user, folder-organized saved messages. Unlike
-- pins (migrations/shard/0010_message_pins.sql), a bookmark is entirely
-- private to the user who made it and never shared or broadcast to other
-- channel members, so it belongs in the control plane rather than
-- denormalized on the message row itself — the same reasoning
-- channels/channel_members/user_channels already live in the control
-- plane (migrations/control/0001_init.sql): listing "all of this user's
-- bookmarks" can span channels homed in different regions/shards, and a
-- control-plane table answers that with one query instead of a cross-shard
-- scatter-gather the way user_channels avoids one for "all of a user's
-- channels."
--
-- Because of that placement, a bookmark's (channel_id, message_id) can't
-- carry a foreign key to messages — that table lives in a different
-- physical database entirely (whichever shard channel_id's home region
-- resolves to). channel_id itself still references the control-plane
-- channels table (so a bookmark can't point at a channel that never
-- existed), but message existence has to be an application-level check
-- instead (internal/messages.Repo.Exists, called by cmd/api's bookmark
-- handlers before insert) — the same layering PollID's existence check
-- already uses one level up (internal/polls.Repo.Exists, checked by
-- cmd/api before internal/messages.Repo.Send stores it), just crossing a
-- database boundary instead of merely a table one.
CREATE TABLE bookmark_folders (
    folder_id   UUID PRIMARY KEY,
    app_id      BIGINT NOT NULL REFERENCES apps(app_id),
    user_id     UUID NOT NULL REFERENCES users(user_id),
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
CREATE INDEX idx_bookmark_folders_user ON bookmark_folders (user_id);

CREATE TABLE bookmarks (
    bookmark_id  UUID PRIMARY KEY,
    app_id       BIGINT NOT NULL REFERENCES apps(app_id),
    user_id      UUID NOT NULL REFERENCES users(user_id),
    channel_id   UUID NOT NULL REFERENCES channels(channel_id),
    -- message_id: intentionally no foreign key — see the package comment
    -- above. Validated at write time by internal/bookmarks.Repo.Create's
    -- caller instead.
    message_id   UUID NOT NULL,
    -- folder_id is nullable ("unfiled") and ON DELETE SET NULL rather than
    -- CASCADE: deleting a folder should never silently delete the
    -- bookmarks that were organized in it, only un-organize them, mirroring
    -- how emptying a real folder doesn't throw away what was inside it.
    folder_id    UUID REFERENCES bookmark_folders(folder_id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, channel_id, message_id)
);
CREATE INDEX idx_bookmarks_user ON bookmarks (user_id, created_at DESC);
CREATE INDEX idx_bookmarks_folder ON bookmarks (folder_id);
