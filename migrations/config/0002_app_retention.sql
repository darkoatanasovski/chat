-- Per-app message retention override (days). The worker's retention sweep uses
-- this instead of the tier default, so retention is a per-app setting: default
-- 730 (2 years); 0 (or negative) means keep forever. Additive/idempotent.
ALTER TABLE apps ADD COLUMN IF NOT EXISTS retention_days INT NOT NULL DEFAULT 730;
