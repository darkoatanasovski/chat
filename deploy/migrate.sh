#!/usr/bin/env bash
# Applies additive SQL migrations to the global config DB (migrations/config)
# and to every cell DB (migrations/cell, applied identically to each cell —
# see docs/adr/0006-cell-based-tenant-routing.md). Idempotent: tracks applied
# filenames in a schema_migrations table per database.
set -euo pipefail

cd "$(dirname "$0")/.."

: "${POSTGRES_USER:=chat}"

# Compose Postgres services: the one config DB, then one per cell. Add a
# cell's Postgres service name here when you add a cell to docker-compose.yml.
CONFIG_DB_SERVICE="config-postgres"
CELL_DB_SERVICES=("us-east-1-a-postgres")

apply() {
  local compose_service="$1"
  local dir="$2"
  echo "==> applying $dir/*.sql to $compose_service"
  docker compose -f deploy/docker-compose.yml exec -T "$compose_service" \
    psql -U "$POSTGRES_USER" -d chat -v ON_ERROR_STOP=1 -c \
    "CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());"

  for f in "$dir"/*.sql; do
    name="$(basename "$f")"
    already=$(docker compose -f deploy/docker-compose.yml exec -T "$compose_service" \
      psql -U "$POSTGRES_USER" -d chat -tAc \
      "SELECT 1 FROM schema_migrations WHERE filename = '$name'")
    if [ "$already" = "1" ]; then
      echo "    skip $name (already applied)"
      continue
    fi
    echo "    apply $name"
    docker compose -f deploy/docker-compose.yml exec -T "$compose_service" \
      psql -U "$POSTGRES_USER" -d chat -v ON_ERROR_STOP=1 < "$f"
    docker compose -f deploy/docker-compose.yml exec -T "$compose_service" \
      psql -U "$POSTGRES_USER" -d chat -v ON_ERROR_STOP=1 -c \
      "INSERT INTO schema_migrations (filename) VALUES ('$name');"
  done
}

apply "$CONFIG_DB_SERVICE" migrations/config
for cell_db in "${CELL_DB_SERVICES[@]}"; do
  apply "$cell_db" migrations/cell
done

echo "==> migrations complete"
