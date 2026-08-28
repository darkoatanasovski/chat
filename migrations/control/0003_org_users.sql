-- Individual human accounts for the customer-facing dashboard, distinct
-- from the existing org-admin token (internal/organizations,
-- POST /organizations) which mints one shared, unattributed credential for
-- the whole org — that path stays as-is for programmatic/automation use
-- (e.g. tools/loadtest). org_users is real per-person auth: email +
-- bcrypt-hashed password, more than one person per organization, with a
-- role. Role is deliberately just two values (owner/member) — see
-- cmd/api/handlers_dashboard.go for what each can do.
CREATE TABLE org_users (
    user_id       UUID PRIMARY KEY,
    org_id        BIGINT NOT NULL REFERENCES organizations(org_id),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_org_users_org ON org_users (org_id);

-- Invites stand in for real email delivery, which this platform has no
-- infrastructure for: creating an invite returns its link/token directly to
-- the inviting owner to share manually, rather than sending mail. token_hash
-- (not the raw token) is stored, same reasoning as app_credentials'
-- secret_hash — the raw token is only ever shown once, at creation.
CREATE TABLE org_invites (
    invite_id   UUID PRIMARY KEY,
    org_id      BIGINT NOT NULL REFERENCES organizations(org_id),
    email       TEXT NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,
    role        TEXT NOT NULL,
    invited_by  UUID NOT NULL REFERENCES org_users(user_id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ
);
CREATE INDEX idx_org_invites_org ON org_invites (org_id);
