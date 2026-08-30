-- Shard-side columns backing four of the new per-app channel capabilities
-- (migrations/control/0012_channel_capabilities.sql gates all of them):
-- quotes, uploads, url_enrichment, location_sharing, and pending_messages.
-- All nullable/zero-valued by default so existing rows and existing
-- SELECT * callers (see internal/messages.messageColumns) are unaffected
-- until a capability is actually turned on for an app.

-- quoted_message_id: the message being quoted, same channel (validated at
-- the application layer in cmd/api, same as parent_id for replies — no DB
-- FK since messages has no single-column unique index usable as a target
-- across its composare key, matching the existing parent_id column's
-- design).
ALTER TABLE messages ADD COLUMN quoted_message_id UUID NULL;

-- attachments: client-supplied file/media references (this API does not
-- itself host object storage — an app integrates its own S3/GCS/CDN and
-- sends the resulting URLs here, the same division of responsibility most
-- chat platforms use). Array of {url, type, filename, size_bytes}.
ALTER TABLE messages ADD COLUMN attachments JSONB NOT NULL DEFAULT '[]'::jsonb;

-- link_preview: best-effort metadata for the first URL found in the
-- message body, filled in asynchronously after the message is created
-- (see cmd/api's enrichLinkPreview) — null until that fetch completes (or
-- forever, if it fails or times out; enrichment is fire-and-forget, never
-- blocks or fails the send). Shape: {url, title, description}.
ALTER TABLE messages ADD COLUMN link_preview JSONB NULL;

-- location: an optional point shared via the message, {lat, lng}.
ALTER TABLE messages ADD COLUMN location JSONB NULL;

-- status: 'sent' (default, immediately visible/delivered) or 'pending'
-- (visible only to its sender until an app-side moderator approves it —
-- see cmd/api/handlers_moderation.go). Only meaningful when an app has
-- pending_messages turned on; every message sent while it's off is
-- created directly as 'sent'.
ALTER TABLE messages ADD COLUMN status TEXT NOT NULL DEFAULT 'sent';
ALTER TABLE messages ADD CONSTRAINT chk_messages_status CHECK (status IN ('sent', 'pending'));

-- Pending messages must still be found and paged by their sender's own
-- history queries but excluded from other members' ListBefore results —
-- this index serves the moderation queue (all pending messages in a
-- channel) directly.
CREATE INDEX idx_messages_channel_pending ON messages (channel_id, sequence) WHERE status = 'pending';
