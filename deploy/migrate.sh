#!/usr/bin/env bash
# Applies additive SQL migrations to the control-plane DB and every shard DB.
# Idempotent: tracks applied filenames in a schema_migrations table per database.
set -euo pipefail

cd "$(dirname "$0")/.."

: "${POSTGRES_USER:=chat}"

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

apply postgres-control migrations/control
apply postgres-shard-a migrations/shard
apply postgres-shard-b migrations/shard

echo "==> migrations complete"
