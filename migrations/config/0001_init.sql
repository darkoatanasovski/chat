-- config DB — the single global control plane (docs/adr/0006-cell-based-tenant-routing.md).
--
-- Small, read-mostly, shared by the router and every cell service, all
-- cache-first (internal/appconfig: invalidate on change, else 1-day TTL).
-- It holds ONLY what is inherently global: the tenant registry (orgs, apps,
-- credentials), dashboard human accounts, per-app placement + settings, and
-- org-level billing/usage. Everything tenant-scoped (users, channels,
-- messages, …) lives in the per-cell DB — see migrations/cell/0001_init.sql.
--
-- This is a consolidated fresh schema, not the incremental control-plane
-- history it descends from: the cell-routing rework is a clean cut (no live
-- data to preserve), so the 14 migrations/control/*.sql files collapse into
-- one readable starting point. Additive-only from here forward, same
-- discipline as before (docs/operations/migrations.md).

-- Organizations: the customer/business. Tier lives here; an org's plan
-- governs every app it owns (resolved live, cached, never trusted from a token).
CREATE TABLE organizations (
    org_id                BIGSERIAL PRIMARY KEY,
    name                  TEXT NOT NULL,
    tier                  TEXT NOT NULL DEFAULT 'FREE',
    -- Self-serve billing via Dodo Payments (hosted checkout). Nullable until
    -- an org first upgrades. dodo_subscription_id guards the downgrade path:
    -- a subscription.cancelled webhook only downgrades if it names the
    -- subscription the org is still on. See cmd/api/handlers_billing.go.
    dodo_customer_id      TEXT,
    dodo_subscription_id  TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Apps: an isolated chat instance (the tenant-isolation boundary every
-- end-user, channel, and message belongs to). PLACEMENT lives here — region
-- + shard say which cell holds this app's tenant data. The router resolves
-- apikey -> app -> {region, shard}; a cell service resolves app_id -> its
-- own settings. Both read this table cache-first via internal/appconfig.
CREATE TABLE apps (
    app_id                BIGSERIAL PRIMARY KEY,
    org_id                BIGINT NOT NULL REFERENCES organizations(org_id),
    name                  TEXT NOT NULL,

    -- Placement (was channels.home_region + hash(channel_id) virtual shard).
    -- An app is pinned to exactly one cell at creation; all its data lives
    -- there. Nullable only so a just-created row can be placed in a second
    -- step; the router treats a null placement as "not yet provisioned".
    region                TEXT,
    shard                 TEXT,

    -- Per-app, owner-configurable settings (were the incremental ALTERs on
    -- control.apps). Read live wherever they gate behavior, never cached as
    -- authoritative — same "no surprise staleness" discipline as before.
    max_thread_depth      INT NOT NULL DEFAULT 3,
    message_edit_enabled  BOOLEAN NOT NULL DEFAULT true,
    max_message_length    INT NOT NULL DEFAULT 4000,
    enabled_commands      TEXT[] NOT NULL DEFAULT ARRAY['giphy', 'ban', 'unban', 'mute', 'unmute'],
    dynamic_partitioning  BOOLEAN NOT NULL DEFAULT false,
    channel_capabilities  JSONB NOT NULL DEFAULT '{
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
    }'::jsonb,

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT chk_apps_max_thread_depth CHECK (max_thread_depth >= 0),
    CONSTRAINT chk_apps_max_message_length CHECK (max_message_length > 0)
);
CREATE INDEX idx_apps_org ON apps (org_id);
-- The router pages apps by placement when reconciling topology; cheap to have.
CREATE INDEX idx_apps_placement ON apps (region, shard);

-- App API credentials: opaque key/secret pairs verified live against this
-- table (not signed tokens) so "revoke" takes effect immediately. The router
-- resolves an inbound ?api_key= / token api_key to app_id here, then reads
-- that app's placement above.
CREATE TABLE app_credentials (
    credential_id     UUID PRIMARY KEY,
    app_id            BIGINT NOT NULL REFERENCES apps(app_id),
    key               TEXT NOT NULL UNIQUE,
    secret_hash       TEXT NOT NULL,
    -- AES-256-GCM ciphertext (internal/platform/secretbox), decryptable only
    -- with APP_SECRET_ENCRYPTION_KEY — lets CredentialRepo.Reveal return a
    -- secret after the one-time creation response is gone. Nullable.
    secret_encrypted  BYTEA,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ
);
CREATE INDEX idx_app_credentials_app ON app_credentials (app_id);
-- Hot path: the router and POST /apps/token look up by key on every call.
CREATE INDEX idx_app_credentials_key ON app_credentials (key);

-- Human accounts for the customer-facing dashboard (distinct from the
-- org-admin programmatic token). Email + bcrypt password, role owner/member.
CREATE TABLE org_users (
    user_id        UUID PRIMARY KEY,
    org_id         BIGINT NOT NULL REFERENCES organizations(org_id),
    email          TEXT NOT NULL UNIQUE,
    password_hash  TEXT NOT NULL,
    role           TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_org_users_org ON org_users (org_id);

-- Dashboard invites (stand-in for email delivery): token_hash stored, raw
-- token shown once at creation.
CREATE TABLE org_invites (
    invite_id    UUID PRIMARY KEY,
    org_id       BIGINT NOT NULL REFERENCES organizations(org_id),
    email        TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    role         TEXT NOT NULL,
    invited_by   UUID NOT NULL REFERENCES org_users(user_id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    accepted_at  TIMESTAMPTZ
);
CREATE INDEX idx_org_invites_org ON org_invites (org_id);

-- Translation usage ledger: billing/usage bookkeeping scoped to app/org, so
-- it stays global here rather than per-cell (it aggregates across an org's
-- apps for the dashboard's usage stat). channel_id/message_id are recorded
-- for provenance but carry no FK — those rows live in a cell DB.
CREATE TABLE translation_usage (
    usage_id                UUID PRIMARY KEY,
    app_id                  BIGINT NOT NULL REFERENCES apps (app_id),
    org_id                  BIGINT NOT NULL REFERENCES organizations (org_id),
    channel_id              UUID NOT NULL,
    message_id              UUID NOT NULL,
    source_lang             TEXT NOT NULL,
    target_lang             TEXT NOT NULL,
    char_count              INT NOT NULL DEFAULT 0,
    estimated_cost_micros   BIGINT NOT NULL DEFAULT 0,
    cache_hit               BOOLEAN NOT NULL DEFAULT false,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_translation_usage_app ON translation_usage (app_id, created_at);
CREATE INDEX idx_translation_usage_org ON translation_usage (org_id, created_at);
