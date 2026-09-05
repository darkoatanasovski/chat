---
name: platform-up
description: Start or stop the local chat platform stack (global config Postgres + control-plane service + one self-contained cell — its own Postgres/Kafka/Valkey running api/ws/worker — behind the edge router) via Docker Compose, run migrations, and seed demo data. Use when the user wants to run, restart, or tear down the platform locally, or check whether it's healthy.
---

# platform-up

Brings up the cell-based architecture
(`docs/adr/0006-cell-based-tenant-routing.md`) on one machine via
`deploy/docker-compose.yml`: the global **config** Postgres, the global
**control** plane (`chat control` — org/dashboard/billing), one
self-contained **cell** (`us-east-1-a` — its own Postgres, Kafka broker, and
Valkey, running `chat api` / `chat ws` / `chat worker`), the edge **router**
in front, plus the standalone OG service and Prometheus/Grafana. Everything is
the single `chat` binary; each service just runs a different role. The router
forwards control-plane paths (`/organizations`, `/apps`, `/dashboard`,
`/dodo`) to the control service and everything else to the cell by apikey.

## Start everything

```bash
cd <repo root>
cp -n .env.example .env   # only if .env doesn't exist yet
make up                   # docker compose build + up -d
./deploy/migrate.sh       # idempotent — safe to re-run
./deploy/seed.sh          # optional: creates demo org/app/users/channel
```

`make up` builds one image from `deploy/docker/Dockerfile` (the single `chat`
binary, with `infra/topology.yaml` + `deploy/tiers.yaml` baked in) and starts
every service in `deploy/docker-compose.yml`.

### If `make up` OOMs / crashes Docker

Compiling Go *inside* Docker can exhaust a memory-constrained Docker Desktop
VM (symptom: build ends with `rpc error ... EOF` and the daemon goes
unresponsive). Two fixes:

- Give Docker more memory (Settings → Resources → Memory ≥ 6 GB), then `make up`.
- Or use **`make up-prebuilt`**: builds the `chat` binary on the host
  (`deploy/docker/build-binary.sh`, matched to the Docker arch) and starts the
  stack from `deploy/docker/Dockerfile.prebuilt`, which only *copies* the
  binary — Docker never compiles Go, so it can't OOM on the build.

**Always run `./deploy/migrate.sh` after the first `make up`** (or after
adding a migration) — it applies `migrations/config` to the config DB and
`migrations/cell` to the cell DB. Services crash-loop with
`relation "..." does not exist` until the schema exists; they retry Postgres
connections for ~30s but do not wait for migrations (by design — see
`docs/operations/migrations.md`).

## Check health

```bash
curl -s localhost:8080/healthz         # router (the global endpoint)
curl -s localhost:8082/healthz | jq    # control plane (direct)
curl -s localhost:8081/healthz | jq    # cell api (direct)
curl -s localhost:8091/healthz | jq    # cell ws (direct)
docker compose -f deploy/docker-compose.yml ps -a
docker compose -f deploy/docker-compose.yml logs -f <service>
```

Port map: router `8080`, control `8082`, config DB `5433`, cell DB `5434`,
cell Kafka (host listener) `29092`, cell Valkey `6379`, cell api `8081`,
cell ws `8091`, cell worker metrics `9101`, OG service `8095`,
Prometheus `9090`, Grafana `3003`.

Requests normally go through the router on `8080`, which reads the apikey
(`?api_key=` or a bearer token) and proxies to the cell that owns the app.
The `8081`/`8091` direct ports exist for debugging a cell in isolation.

## Scale a role within the cell

```bash
docker compose -f deploy/docker-compose.yml up -d --scale us-east-1-a-api=2 --scale us-east-1-a-ws=2
```

(Production runs 2+ of each per cell — see `infra/topology.yaml` `replicas`.)

## Stop everything

```bash
make down
```

Stops/removes containers but preserves the named Postgres volumes
(`config-data`, `us-east-1-a-data`) — data survives a restart. To wipe data,
also run `docker compose -f deploy/docker-compose.yml down -v`.

## Adding a second cell

Copy the `us-east-1-a-*` block in `deploy/docker-compose.yml` under a new id
with its own Postgres/Kafka/Valkey, add that Postgres to
`deploy/migrate.sh`'s `CELL_DB_SERVICES`, and add the cell to
`infra/topology.yaml`. No code changes.

## Common failure: containers exit immediately after a fresh `make up`

Almost always missing migrations — run `./deploy/migrate.sh`. If they still
fail, check `docker compose logs <service>` for the actual connection error
before assuming code is broken.
