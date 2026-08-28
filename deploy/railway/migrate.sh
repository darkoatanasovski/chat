#!/usr/bin/env bash
# Applies additive SQL migrations directly via psql, mirroring
# deploy/migrate.sh's schema_migrations tracking but talking straight to a
# Postgres server instead of through `docker compose exec` — there's no
# compose stack in a Railway deployment. Meant to be run from CI (or by
# hand) against a single Railway Postgres service hosting three logical
# databases: control, shard_a, shard_b — see docs/deploy/railway.md for why
# one Postgres instance instead of three for a dev environment.
set -euo pipefail
cd "$(dirname "$0")/../.."

: "${DATABASE_URL:?DATABASE_URL is required - the Postgres connection string, any database name (this script only uses it to find the server, then connects to control/shard_a/shard_b directly)}"

# Drop whatever database name is already in the URL so the rest of this
# script can connect to control/shard_a/shard_b on the same server.
base_url="${DATABASE_URL%/*}"

ensure_db() {
  local name="$1"
  local exists
  exists=$(psql "$base_url/postgres" -tAc "SELECT 1 FROM pg_database WHERE datname = '$name'")
  if [ "$exists" != "1" ]; then
    echo "==> creating database $name"
    psql "$base_url/postgres" -v ON_ERROR_STOP=1 -c "CREATE DATABASE $name;"
  fi
}

apply() {
  local name="$1"
  local dir="$2"
  local dsn="$base_url/$name"
  echo "==> applying $dir/*.sql to $name"
  psql "$dsn" -v ON_ERROR_STOP=1 -c \
    "CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());"

  for f in "$dir"/*.sql; do
    local fname
    fname="$(basename "$f")"
    local already
    already=$(psql "$dsn" -tAc "SELECT 1 FROM schema_migrations WHERE filename = '$fname'")
    if [ "$already" = "1" ]; then
      echo "    skip $fname (already applied)"
      continue
    fi
    echo "    apply $fname"
    psql "$dsn" -v ON_ERROR_STOP=1 < "$f"
    psql "$dsn" -v ON_ERROR_STOP=1 -c "INSERT INTO schema_migrations (filename) VALUES ('$fname');"
  done
}

ensure_db control
ensure_db shard_a
ensure_db shard_b

apply control migrations/control
apply shard_a migrations/shard
apply shard_b migrations/shard

echo "==> migrations complete"
