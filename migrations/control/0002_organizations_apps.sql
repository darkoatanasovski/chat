-- B2B tenancy: Organizations (the customer/business, tier lives here) own
-- Apps (numeric id, isolated chat instance) which own API credentials (used
-- by the business's own backend to call this platform on behalf of its
-- end-users). See docs/platform/security.md for why app credentials are
-- opaque key/secret pairs verified live against Postgres, not signed
-- tokens: that's what makes "revoke" take effect immediately.

CREATE TABLE organizations (
    org_id      BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    tier        TEXT NOT NULL DEFAULT 'FREE',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE apps (
    app_id      BIGSERIAL PRIMARY KEY,
    org_id      BIGINT NOT NULL REFERENCES organizations(org_id),
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs the max_apps resource quota (CountByOrg), same pattern as
-- idx_channel_members_user backing the max_channel_members check.
CREATE INDEX idx_apps_org ON apps (org_id);

CREATE TABLE app_credentials (
    credential_id  UUID PRIMARY KEY,
    app_id         BIGINT NOT NULL REFERENCES apps(app_id),
    key            TEXT NOT NULL UNIQUE,
    secret_hash    TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at     TIMESTAMPTZ
);

CREATE INDEX idx_app_credentials_app ON app_credentials (app_id);
-- The hot-path lookup on every POST /users call is by key; UNIQUE above
-- already gives this an index, but naming it makes the access pattern
-- explicit for anyone reading the schema.
CREATE INDEX idx_app_credentials_key ON app_credentials (key);

-- Every end-user and every channel now belongs to exactly one app — the
-- tenant-isolation boundary. This repo's local/demo data predates the app
-- concept entirely (there is no "correct" app to retroactively assign
-- existing rows to), so this migration assumes an empty users/channels
-- table; see docs/operations/migrations.md for the reset step.
ALTER TABLE users    ADD COLUMN app_id BIGINT NOT NULL REFERENCES apps(app_id);
ALTER TABLE channels ADD COLUMN app_id BIGINT NOT NULL REFERENCES apps(app_id);

CREATE INDEX idx_users_app ON users (app_id);
CREATE INDEX idx_channels_app ON channels (app_id);

-- Tier is resolved live from app_id -> organizations.tier (cached, see
-- internal/apps.TierResolver) so an org's plan change takes effect for all
-- of its existing end-users immediately, not after their next token
-- reissue — the same reasoning that already keeps channel home_region
-- resolution live rather than trusting a cached value as authoritative.
ALTER TABLE users DROP COLUMN tier;
