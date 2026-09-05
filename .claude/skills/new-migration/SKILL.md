---
name: new-migration
description: Scaffold a new additive SQL migration for either the global config database or the per-cell database (applied identically to every cell), following this platform's additive-only migration philosophy. Use when the user wants to add or change a database table/column/index.
---

# new-migration

This platform has two schema families under `migrations/` (see
docs/adr/0006-cell-based-tenant-routing.md):

- `migrations/config/` — applied once, to the global **config** Postgres
  (the tenant registry + placement + settings: `organizations`, `apps`,
  `app_credentials`, `org_users`, `org_invites`, `translation_usage`).
- `migrations/cell/` — applied identically to **every** cell's Postgres via
  `deploy/migrate.sh` (all tenant data: `users`, `channels`,
  `channel_members`, `user_channels`, `messages`, `channel_sequences`,
  `outbox_events`, reactions, polls, blocks, mutes, bookmarks, …). Never
  write a migration that assumes a specific cell.

## Steps

1. Decide which family the change belongs to. If it touches tenant data (a
   user/channel/message and anything hanging off them) → `migrations/cell/`.
   If it touches the tenant registry / org-global data (orgs, apps,
   credentials, billing, usage) → `migrations/config/`. Remember `app_id` on
   cell tables carries **no** foreign key — `apps` lives in the config DB, a
   different database.

2. Find the next sequence number:
   ```bash
   ls migrations/config/   # or migrations/cell/
   ```
   Files are named `NNNN_description.sql`, zero-padded, strictly increasing.

3. Write the migration as **additive only** (INSTRUCTIONS.md §42). At
   billions-of-rows scale:
   - OK: `ADD COLUMN ... DEFAULT NULL` (no table rewrite in Postgres 11+ for a
     nullable/constant default), new tables, new indexes created
     `CONCURRENTLY` if the table is already large and this isn't a fresh cell.
   - NOT OK in a single migration: `ALTER COLUMN ... TYPE`, `NOT NULL` added
     to an existing populated column without a backfill step first, dropping
     a column still read by running code, renaming a column in place (do
     add-new-column + dual-write + backfill + drop-old across migrations).
   - Avoid unnecessary indexes on `messages` — the highest-write table (§9).

4. Apply it locally:
   ```bash
   ./deploy/migrate.sh
   ```
   Idempotent — it tracks applied filenames in a `schema_migrations` table per
   database and skips ones already applied. (On Railway, apply via
   `deploy/railway/migrate.sh` with `CONFIG_DATABASE_URL` / `CELL_DATABASE_URLS`.)

5. If the new column/table is read by application code, update the relevant
   repo in `internal/<package>/` in the same change — don't leave schema and
   code out of sync.

## Example

```bash
# adding a `topic` column to channels (tenant data -> cell)
cat > migrations/cell/0002_channel_topic.sql <<'EOF'
ALTER TABLE channels ADD COLUMN topic TEXT;
EOF
./deploy/migrate.sh
```
