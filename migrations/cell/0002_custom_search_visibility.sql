-- Custom metadata, full-text + JSONB search, and channel visibility.
--
-- messages.custom / channels.custom: app-defined JSONB (set via the API/SDK),
-- delivered inline and searchable via a GIN index.
-- messages.body_tsv: a generated tsvector for full-text body search (GIN).
-- channels.visibility: 'public' (any app user may read/join/discover) or
-- 'private' (members only — the existing behaviour, and the default).
-- All statements are idempotent so re-running is safe.

ALTER TABLE messages ADD COLUMN IF NOT EXISTS custom JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS body_tsv tsvector
  GENERATED ALWAYS AS (to_tsvector('simple', body)) STORED;

CREATE INDEX IF NOT EXISTS idx_messages_body_tsv ON messages USING GIN (body_tsv);
CREATE INDEX IF NOT EXISTS idx_messages_custom ON messages USING GIN (custom jsonb_path_ops);

ALTER TABLE channels ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'private';
ALTER TABLE channels ADD COLUMN IF NOT EXISTS custom JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Constrain visibility to the known values (guarded so re-running is safe).
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'channels_visibility_check'
  ) THEN
    ALTER TABLE channels ADD CONSTRAINT channels_visibility_check
      CHECK (visibility IN ('public', 'private'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_channels_app_name ON channels (app_id, name);
CREATE INDEX IF NOT EXISTS idx_channels_app_visibility ON channels (app_id, visibility);
CREATE INDEX IF NOT EXISTS idx_channels_custom ON channels USING GIN (custom jsonb_path_ops);
