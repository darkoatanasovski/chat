-- max_thread_depth is the first genuinely per-app, owner-configurable
-- setting on the apps table (everything else that limits behavior today —
-- max_apps, max_channels, messages/min, etc., see deploy/tiers.yaml — is
-- deploy-owned, keyed by organization tier, not something an app's own
-- owner sets directly via the API). It caps how many levels deep a reply
-- chain (messages.parent_id, migrations/shard's thread migration) is
-- allowed to nest for this app: 0 means "no cap" (nesting is otherwise
-- architecturally unbounded — see internal/messages.Repo.Send), any
-- positive N caps it there. Defaults to 3, changeable any time via
-- PATCH /apps/{app_id} (cmd/api/handlers_apps.go's handleUpdateApp) and
-- takes effect on the very next reply — never cached, always read live at
-- send time, the same "no surprise staleness" property every other
-- app-scoped check in this codebase already has.
ALTER TABLE apps ADD COLUMN max_thread_depth INT NOT NULL DEFAULT 3;
ALTER TABLE apps ADD CONSTRAINT chk_apps_max_thread_depth CHECK (max_thread_depth >= 0);
