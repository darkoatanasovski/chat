# Local Development

## Prerequisites

- Go 1.24+
- Docker + Docker Compose v2
- Node 20+ (for `demo/`)

## Quickest path

```bash
cp .env.example .env
make up               # docker compose build + up -d (13 containers, see docker-compose.md)
./deploy/migrate.sh   # idempotent, safe to re-run any time
./deploy/seed.sh       # optional: creates demo users + a channel + a message
```

Then either exercise the API directly (see
[../api/rest-api.md](../api/rest-api.md)) or run the demo app:

```bash
cd demo
npm install
npm run dev            # http://localhost:3000
```

See also the `platform-up` skill, which wraps this same sequence.

## Working on Go code without Docker

Every service reads its config from environment variables
(`internal/platform/config`). You can run any of them directly against the
dockerized dependencies (Postgres/Kafka/Redis ports are published to the
host):

```bash
export REGION=eu HTTP_ADDR=:8080 METRICS_ADDR=:9100 \
  CONTROL_DSN="postgres://chat:chat@localhost:5433/chat?sslmode=disable" \
  SHARD_A_DSN="postgres://chat:chat@localhost:5434/chat?sslmode=disable" \
  SHARD_B_DSN="postgres://chat:chat@localhost:5435/chat?sslmode=disable" \
  VALKEY_ADDR=localhost:6379 KAFKA_BROKERS=localhost:9092 \
  AUTH_SECRET=dev-local-only-secret-change-me \
  SHARDS_CONFIG=deploy/shards.yaml TIERS_CONFIG=deploy/tiers.yaml \
  PEER_API_EU_URL=http://localhost:8081 PEER_API_US_URL=http://localhost:8082 PEER_API_ASIA_URL=http://localhost:8083

go run ./cmd/api
```

Useful when iterating on one service without rebuilding its Docker image —
just make sure the Postgres/Kafka/Redis containers from `make up` are
running and migrated first.

## Build, test, lint

```bash
make build   # go build ./...
make test    # go test ./...
gofmt -l .   # should print nothing
go vet ./...
```

## Common gotchas

- **Containers exit immediately after `make up`**: migrations haven't run
  yet. `./deploy/migrate.sh`. (Services retry Postgres/Redis connections for
  30s on startup, but do not wait for schema to exist — that's a separate,
  explicit step.)
- **`go.sum` mismatch in Docker build**: run `go mod tidy` locally first;
  the Dockerfile does `go mod download` against the committed `go.sum`.
- **Port already in use**: this stack claims `5433-5435` (Postgres),
  `9092` (Kafka), `6379` (Valkey), `8081-8083` (api), `8091-8093` (gateway),
  `9101-9102` (worker metrics), `3000` (demo). Adjust
  `deploy/docker-compose.yml` port mappings if any collide with something
  else on your machine.
