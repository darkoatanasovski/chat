-- Translation usage ledger: one row per translate request (cache hit or
-- provider call), living in the control plane rather than a shard
-- (unlike migrations/shard/0013_message_translations.sql's cache) because
-- this is billing/usage bookkeeping scoped to app_id/org_id, the same
-- "usage state lives centrally" choice as everything else in
-- cmd/api/handlers_billing.go — not a hot per-message write, so it doesn't
-- need to be shard-local the way messages/reactions/polls do.
--
-- This is usage *tracking*, not a live charge: this platform's billing
-- (see migrations/control/0005_billing.sql) is subscription-only via Dodo
-- Payments checkout, with no per-call metering integration today.
-- estimated_cost_micros is a display figure computed from
-- internal/translations.CostMicrosPerChar, not something that itself
-- moves money.

CREATE TABLE translation_usage (
    usage_id                UUID PRIMARY KEY,
    app_id                  BIGINT NOT NULL REFERENCES apps (app_id),
    org_id                  BIGINT NOT NULL REFERENCES organizations (org_id),
    channel_id              UUID NOT NULL,
    message_id              UUID NOT NULL,
    source_lang             TEXT NOT NULL,
    target_lang             TEXT NOT NULL,
    char_count              INT NOT NULL DEFAULT 0,
    -- estimated_cost_micros is USD micros (1,000,000 = $1) so this stays
    -- an exact integer rather than a floating-point dollar amount.
    estimated_cost_micros   BIGINT NOT NULL DEFAULT 0,
    cache_hit               BOOLEAN NOT NULL DEFAULT false,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs SummaryForApp's per-app aggregate (the Dashboard tab's translation
-- usage stat).
CREATE INDEX idx_translation_usage_app ON translation_usage (app_id, created_at);
-- Backs a future org-wide rollup the same way apps.CountByOrg's sibling
-- reads do; not used by any handler yet, but cheap to have from day one
-- given every row already carries org_id.
CREATE INDEX idx_translation_usage_org ON translation_usage (org_id, created_at);
