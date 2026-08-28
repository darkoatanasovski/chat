-- User blocking (INSTRUCTIONS.md §44 lists user.blocked/user.unblocked as
-- an anticipated future feature). A block is directional for ownership
-- (only the blocker who created it may remove it — see
-- internal/blocks.Repo.Unblock's WHERE clause), but enforcement in
-- internal/realtime is bidirectional: neither side sees the other's
-- messages once either has blocked the other, so a client never needs to
-- distinguish "I blocked them" from "they blocked me" to know
-- communication is cut off.
--
-- app_id is stored redundantly rather than derived by joining through
-- users, matching this platform's existing defense-in-depth convention
-- (e.g. checkChannelWriteAccess's explicit route.AppID != identity.AppID
-- check) — a block should never be usable to reach across app boundaries
-- even if a future bug let mismatched user_ids through.
CREATE TABLE user_blocks (
    app_id          BIGINT NOT NULL REFERENCES apps(app_id),
    blocker_user_id UUID NOT NULL REFERENCES users(user_id),
    blocked_user_id UUID NOT NULL REFERENCES users(user_id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_user_id, blocked_user_id),
    CHECK (blocker_user_id != blocked_user_id)
);
CREATE INDEX idx_user_blocks_app ON user_blocks (app_id);
-- Supports the reverse-direction half of the bidirectional lookup
-- (internal/blocks.Repo.BlockedPairsFor's UNION query) without a seq scan.
CREATE INDEX idx_user_blocks_blocked ON user_blocks (blocked_user_id);
