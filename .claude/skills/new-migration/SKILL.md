---
name: new-migration
description: Scaffold a new additive SQL migration for either the control-plane database or the shard database (applied identically to shard-a and shard-b), following this platform's additive-only migration philosophy. Use when the user wants to add or change a database table/column/index.
---

# new-migration

This platform has two schema families under `migrations/`:

- `migrations/control/` — applied once, to the single control-plane Postgres
  instance (`users`, `channels`, `channel_members`, `user_channels`).
- `migrations/shard/` — applied identically to **every** physical message
  shard (`shard-a`, `shard-b`, ...) via `deploy/migrate.sh`. Never write a
  migration that assumes a specific shard.

## Steps

1. Decide which family the change belongs to. If it touches `messages`,
   `channel_sequences`, or `outbox_events` → `migrations/shard/`. Everything
   else (`users`, `channels`, `channel_members`, `user_channels`, future
   control-plane tables) → `migrations/control/`.

2. Find the next sequence number:
   ```bash
   ls migrations/control/   # or migrations/shard/
   ```
   Files are named `NNNN_description.sql`, zero-padded, strictly increasing.
   Pick the next number.

3. Write the migration as **additive only** (INSTRUCTIONS.md §42). At
   billions-of-rows scale:
   - OK: `ADD COLUMN ... DEFAULT NULL` (no table rewrite in Postgres 11+ for a
     nullable/constant default), new tables, new indexes created
     `CONCURRENTLY` if the table is already large and this isn't a fresh
     shard.
   - NOT OK in a single migration: `ALTER COLUMN ... TYPE`, `NOT NULL` added
     to an existing populated column without a backfill step first, dropping
     a column still read by running code, renaming a column in place (do
     add-new-column + dual-write + backfill + drop-old instead, across
     multiple migrations/deploys).
   - Avoid unnecessary indexes on `messages` — it's the highest-write table
     in the system (INSTRUCTIONS.md §9).

4. Apply it locally:
   ```bash
   ./deploy/migrate.sh
   ```
   This is idempotent — it tracks applied filenames in a `schema_migrations`
   table per database and skips ones already applied.

5. If the new column/table is read by application code, update the relevant
   repo in `internal/<package>/` (e.g. `internal/messages`, `internal/channels`)
   in the same change — don't leave schema and code out of sync.

## Example

```bash
# adding a `topic` column to channels (control-plane)
cat > migrations/control/0002_channel_topic.sql <<'EOF'
ALTER TABLE channels ADD COLUMN topic TEXT;
EOF
./deploy/migrate.sh
```
