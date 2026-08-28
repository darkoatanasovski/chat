---
name: platform-up
description: Start or stop the local chat platform stack (Postgres control + shard-a/b, Kafka, Valkey, 3 api + 3 gateway + 2 worker instances) via Docker Compose, run migrations, and seed demo data. Use when the user wants to run, restart, or tear down the platform locally, or check whether it's healthy.
---

# platform-up

Brings up the full architecture described in `.claude/INSTRUCTIONS.md` on one
machine via `deploy/docker-compose.yml`: three simulated regions (eu/us/asia),
each with its own `api` + `gateway`, two Postgres message shards, one
control-plane Postgres, one Kafka broker (KRaft, no Zookeeper), one Valkey
instance, and one outbox-publisher worker per shard.

## Start everything

```bash
cd <repo root>
cp -n .env.example .env   # only if .env doesn't exist yet
make up                   # docker compose build + up -d
./deploy/migrate.sh       # idempotent — safe to re-run
./deploy/seed.sh          # optional: creates demo users/channel
```

`make up` builds images from `deploy/docker/Dockerfile` (one Dockerfile, a
`SERVICE` build arg selects which `cmd/*` binary to build) and starts every
service defined in `deploy/docker-compose.yml`.

**Always run `./deploy/migrate.sh` after the first `make up`** (or after
adding a new migration) — the api/gateway/worker containers will crash-loop
with `relation "..." does not exist` until the schema exists. They auto-retry
on Postgres connection failures for up to 30s, but they do **not** wait for
migrations, which is a separate step by design (see
`docs/operations/migrations.md`).

## Check health

```bash
curl -s localhost:8081/healthz | jq   # api-eu   (also 8082=us, 8083=asia)
curl -s localhost:8091/healthz | jq   # gateway-eu (also 8092=us, 8093=asia)
docker compose -f deploy/docker-compose.yml ps -a
docker compose -f deploy/docker-compose.yml logs -f <service>
```

Port map: control DB `5433`, shard-a `5434`, shard-b `5435`, Kafka `9092`,
Valkey `6379`, api `8081/8082/8083` (eu/us/asia), gateway `8091/8092/8093`,
worker metrics `9101/9102`.

## Stop everything

```bash
make down
```

This stops and removes containers but preserves the named Postgres volumes
(`control-data`, `shard-a-data`, `shard-b-data`) — data survives a restart.
To wipe data entirely, additionally run `docker compose -f
deploy/docker-compose.yml down -v`.

## Common failure: containers exit immediately after a fresh `make up`

That's almost always missing migrations — run `./deploy/migrate.sh`. If
containers still fail, check `docker compose logs <service>` for the actual
connection error before assuming code is broken.
