# Migrations

Implements INSTRUCTIONS.md §42: schema changes must assume tables eventually
hold billions of rows. See the `new-migration` skill for the mechanical
steps of adding one; this page covers the philosophy and the runner.

## Two independent schema families

- `migrations/control/` — applied once, to the single control-plane
  Postgres instance.
- `migrations/shard/` — applied identically to **every** physical shard
  (`shard-a`, `shard-b`, and any future shard). A shard migration must never
  assume which shard it's running against.

Both are numbered `NNNN_description.sql`, strictly increasing, applied in
order.

## The runner

`deploy/migrate.sh`: for each target database, ensures a
`schema_migrations(filename PRIMARY KEY, applied_at)` tracking table exists,
then applies any file in the relevant directory not already recorded there.
Idempotent and safe to run repeatedly — it's the same command whether it's
the first run against a fresh database or a rerun after adding one new file.

```bash
./deploy/migrate.sh
```

There's no separate "rollback" tooling. Consistent with the additive-only
philosophy below: if a migration is wrong, the fix is a new additive
migration that corrects it, not a rollback that could run against a
database that already has application code depending on the new schema.

## Additive-only

At the scale this system is designed for, some operations that are "fine"
on a small table become outages on a billion-row one:

**Safe as a single migration:**
- New tables.
- `ADD COLUMN` with no default, or a constant default (Postgres 11+ doesn't
  rewrite the table for a constant default on a nullable column).
- New indexes — use `CREATE INDEX CONCURRENTLY` on any table that isn't a
  fresh empty shard, to avoid a blocking table lock.

**Not safe as a single migration — needs a multi-step plan:**
- Changing a column's type (`ALTER COLUMN ... TYPE`) — rewrites the table.
- Adding `NOT NULL` to a column with existing rows — requires a backfill
  first, then the constraint as a separate migration once every row is
  populated.
- Renaming a column in place — breaks currently-deployed code mid-rollout.
  Instead: add the new column, dual-write it alongside the old one from
  application code, backfill existing rows, switch reads to the new column,
  then drop the old column in a later migration once nothing reads it.
- Dropping a column or table still referenced by any currently-deployed
  service.

## Avoid unnecessary indexes on `messages`

It's the highest-write table in the system
(INSTRUCTIONS.md §9). It currently has exactly two indexes: the primary key
(`channel_id, sequence` — serves both point lookups and cursor pagination)
and the idempotency unique index (`channel_id, client_message_id`). Adding a
third should be justified by an actual query pattern, not speculative.

## Schema and code changes together

If a migration adds a column or table application code needs to read/write,
land the migration and the corresponding change in
`internal/<package>/*.go` in the same change — don't let schema drift ahead
of or behind the code that depends on it.
