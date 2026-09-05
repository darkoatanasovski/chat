-- D1 (serverless SQLite) as an alternative config/placement store at the edge
-- — infra/cloudflare/cloudflare-services.md.
--
-- The config DB is small, read-mostly, and global, which makes D1 a fit: a
-- Worker can resolve apikey → {region, shard} straight from D1 with no KV sync
-- and no Postgres round-trip. This mirrors the placement-relevant slice of
-- migrations/config/0001_init.sql in SQLite types (INTEGER ids, TEXT for
-- timestamps/JSON, BLOB for bytes). It is NOT the full config schema — the Go
-- control plane still owns orgs/apps writes on Postgres; treat this as a
-- read-optimized edge projection you sync into, OR as the store of record if
-- you move those reads to Workers.
--
-- Apply: npx wrangler d1 execute chat-config --file=infra/cloudflare/d1/schema.sql

CREATE TABLE IF NOT EXISTS organizations (
  org_id     INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL,
  tier       TEXT NOT NULL DEFAULT 'FREE',
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS apps (
  app_id     INTEGER PRIMARY KEY AUTOINCREMENT,
  org_id     INTEGER NOT NULL REFERENCES organizations(org_id),
  name       TEXT NOT NULL,
  region     TEXT,   -- placement (immutable)
  shard      TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_apps_placement ON apps (region, shard);

CREATE TABLE IF NOT EXISTS app_credentials (
  credential_id TEXT PRIMARY KEY,
  app_id        INTEGER NOT NULL REFERENCES apps(app_id),
  key           TEXT NOT NULL UNIQUE,
  secret_hash   TEXT NOT NULL,
  created_at    TEXT NOT NULL DEFAULT (datetime('now')),
  revoked_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_app_credentials_key ON app_credentials (key);

-- The edge placement query (apikey → cell), the D1 equivalent of the Worker's
-- KV lookup:
--   SELECT a.region, a.shard
--   FROM app_credentials c JOIN apps a ON a.app_id = c.app_id
--   WHERE c.key = ?1 AND c.revoked_at IS NULL;
