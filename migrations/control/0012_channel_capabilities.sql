-- Channel capabilities: a per-app, dashboard-configurable set of feature
-- toggles governing what end-users can do in this app's channels — the
-- third and fourth per-app settings on the apps table after
-- max_thread_depth/message_edit_enabled (0009/0010), but there are now 19
-- independent booleans rather than one or two, so they're consolidated
-- into a single JSONB column instead of 19 more ALTER TABLE ADD COLUMNs.
-- Read live on every request that needs one (same "no surprise staleness"
-- discipline as max_thread_depth/message_edit_enabled) — never cached.
--
-- Defaults mirror what a freshly-provisioned app should reasonably ship
-- with: the capabilities that already existed unconditionally in this
-- codebase before per-app gating was added (reactions, threads & replies,
-- polls, custom events, typing events, read events) default on so existing
-- behavior doesn't change for apps created before this migration;
-- capabilities that are new functionality (search, uploads, quotes,
-- mutes, url enrichment, reminders, pending messages, location sharing,
-- delivery/connection events, strict last message time) default to the
-- same on/off split shown on the reference dashboard mock this schema was
-- modeled on.
ALTER TABLE apps ADD COLUMN channel_capabilities JSONB NOT NULL DEFAULT '{
  "typing_events": true,
  "read_events": true,
  "connection_events": false,
  "custom_events": true,
  "reactions": true,
  "search": false,
  "threads_and_replies": true,
  "quotes": true,
  "mutes": true,
  "uploads": true,
  "url_enrichment": false,
  "message_count": false,
  "message_reminders": false,
  "unread_reminders": false,
  "pending_messages": false,
  "polls": true,
  "strict_last_message_time": false,
  "location_sharing": false,
  "delivery_events": false
}'::jsonb;

-- max_message_length: replaces the previously hardcoded
-- cmd/api.maxMessageBodyLen (4000) with a per-app, lowerable limit.
-- Defaults to 4000 so existing apps see no behavior change; an owner can
-- tighten it (e.g. to 280) from the dashboard.
ALTER TABLE apps ADD COLUMN max_message_length INT NOT NULL DEFAULT 4000;
ALTER TABLE apps ADD CONSTRAINT chk_apps_max_message_length CHECK (max_message_length > 0);

-- enabled_commands: slash-commands end-users may invoke when composing a
-- message (e.g. "/ban @user"). The command *names* enabled here are just
-- config an app owner curates from the dashboard; see
-- cmd/api/handlers_messages.go for which of them this API actually
-- interprets server-side today vs. passes through as a client-side hint.
ALTER TABLE apps ADD COLUMN enabled_commands TEXT[] NOT NULL DEFAULT ARRAY['giphy', 'ban', 'unban', 'mute', 'unmute'];

-- dynamic_partitioning: informational/forward-compatible toggle. This
-- platform already always virtually-shards every channel by hash (see
-- internal/routing.Router.VirtualShard) regardless of this flag — there's
-- no "unpartitioned" mode to fall back to. The toggle is stored and
-- surfaced on the dashboard for parity with the reference product's
-- settings screen, and is available for a future auto-scaling policy
-- (e.g. widening a hot channel's virtual-shard range) to key off; it does
-- not change routing behavior today.
ALTER TABLE apps ADD COLUMN dynamic_partitioning BOOLEAN NOT NULL DEFAULT false;
