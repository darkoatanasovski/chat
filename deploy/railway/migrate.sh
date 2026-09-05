#!/usr/bin/env bash
# Applies migrations to a Railway deployment's databases: the global config DB
# (migrations/config) and each cell DB (migrations/cell). Talks straight to
# Postgres via psql (there's no compose stack on Railway). Runs from CI or by
# hand. See docs/operations/railway-dev.md.
#
# The config DB and every cell DB can be separate Railway Postgres services
# (one per cell is the isolated model) or, for a cheap single-region dev
# deployment, distinct logical databases on one Postgres server. This script
# takes explicit DSNs so it works either way:
#
#   CONFIG_DATABASE_URL   -> the config DB (migrations/config)
#   CELL_DATABASE_URLS    -> comma-separated cell DSNs (migrations/cell each)
set -euo pipefail
cd "$(dirname "$0")/../.."

: "${CONFIG_DATABASE_URL:?CONFIG_DATABASE_URL is required (the global config DB DSN)}"
: "${CELL_DATABASE_URLS:?CELL_DATABASE_URLS is required (comma-separated cell DB DSNs)}"

apply() {
  local dsn="$1"
  local dir="$2"
  echo "==> applying $dir/*.sql to $(echo "$dsn" | sed -E 's#://[^@]+@#://***@#')"
  psql "$dsn" -v ON_ERROR_STOP=1 -c \
    "CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());"

  for f in "$dir"/*.sql; do
    local fname; fname="$(basename "$f")"
    local already; already=$(psql "$dsn" -tAc "SELECT 1 FROM schema_migrations WHERE filename = '$fname'")
    if [ "$already" = "1" ]; then
      echo "    skip $fname (already applied)"
      continue
    fi
    echo "    apply $fname"
    psql "$dsn" -v ON_ERROR_STOP=1 < "$f"
    psql "$dsn" -v ON_ERROR_STOP=1 -c "INSERT INTO schema_migrations (filename) VALUES ('$fname');"
  done
}

apply "$CONFIG_DATABASE_URL" migrations/config

IFS=',' read -ra cells <<< "$CELL_DATABASE_URLS"
for dsn in "${cells[@]}"; do
  apply "$dsn" migrations/cell
done

echo "==> migrations complete"
