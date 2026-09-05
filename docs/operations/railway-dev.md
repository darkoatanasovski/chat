# Railway deployment

A cell-based deployment of the platform on [Railway](https://railway.app),
matching the model in
[docs/adr/0006-cell-based-tenant-routing.md](../adr/0006-cell-based-tenant-routing.md):
a global **config** database, one or more self-contained **cells**, and a
**router** in front that sends each request to the cell its app is pinned to.

Everything is one image — `deploy/docker/Dockerfile` builds the single `chat`
binary — and each Railway service just runs a different **start command**
(`api`, `ws`, `worker`, `router`). There are no per-service Dockerfiles
anymore.

## Topology (one region, one cell — the cheap starting point)

| Railway service | Start command | Notes |
|---|---|---|
| `router` | `router` | Public. The global endpoint (`api.chat.io`). Set `CONTROL_URL` to the control service. |
| `control` | `control` | Global org/dashboard/billing plane. Reads config DB + every cell DB. |
| `config-postgres` | — | Railway Postgres plugin. The global config DB. |
| `cell-a-postgres` | — | Railway Postgres plugin. This cell's own DB. |
| `cell-a-kafka` | — | `apache/kafka:3.8.0` image. This cell's own broker. |
| `cell-a-valkey` | — | Railway Redis plugin / `valkey/valkey:7.2-alpine`. This cell's cache. |
| `cell-a-api` | `api` | REST for the cell (run 2+ for availability). |
| `cell-a-ws` | `ws` | WebSocket edge for the cell (run 2+). |
| `cell-a-worker` | `worker` | Outbox publisher + retention/reminders (run 2+). |

Adding a second cell = a second Postgres/Kafka/Valkey trio plus its own
`api`/`ws`/`worker` services, and a new entry in `infra/topology.yaml`. The
router and config DB stay single and global.

## Provisioning

1. **Create the databases**: two Postgres services (`config-postgres`,
   `cell-a-postgres`) and one Redis/Valkey (`cell-a-valkey`). Deploy Kafka
   (`cell-a-kafka`) from `apache/kafka:3.8.0` with the KRaft env from
   `deploy/docker-compose.yml`'s `us-east-1-a-kafka` (advertise it on its
   Railway private hostname, e.g. `cell-a-kafka.railway.internal:9092`).

2. **Create the app services** (`router`, `cell-a-api`, `cell-a-ws`,
   `cell-a-worker`), each deployed from this repo with **Dockerfile path**
   `deploy/docker/Dockerfile` and a **Custom Start Command** of its role
   (`router` / `api` / `ws` / `worker`). `infra/topology.yaml` and
   `deploy/tiers.yaml` are baked into the image.

3. **Environment variables** (use Railway's `${{Service.VAR}}` references
   instead of literals where possible):

   **router**
   ```
   CONFIG_DSN=${{config-postgres.DATABASE_URL}}
   VALKEY_ADDR=<cell-a-valkey private host>:<port>
   AUTH_SECRET=<openssl rand -hex 32>
   TOPOLOGY_CONFIG=/etc/chat/topology.yaml
   CONTROL_URL=http://control.railway.internal:8080
   ROUTER_ADDR=:8080
   METRICS_ADDR=:9100
   ```

   **control**
   ```
   CONFIG_DSN=${{config-postgres.DATABASE_URL}}
   SHARD_US_EAST_1_A_DSN=${{cell-a-postgres.DATABASE_URL}}   # one per cell, named by topology.yaml's dsn_env
   VALKEY_ADDR=<cell-a-valkey private host>:<port>
   AUTH_SECRET=<same value as router's>
   APP_SECRET_ENCRYPTION_KEY=<openssl rand -base64 32>
   TIERS_CONFIG=/etc/chat/tiers.yaml
   TOPOLOGY_CONFIG=/etc/chat/topology.yaml
   HTTP_ADDR=:8080
   METRICS_ADDR=:9100
   ```

   **cell-a-api / cell-a-ws / cell-a-worker** (shared)
   ```
   REGION=us-east-1
   SHARD_ID=us-east-1-a
   CONFIG_DSN=${{config-postgres.DATABASE_URL}}
   CELL_DSN=${{cell-a-postgres.DATABASE_URL}}
   VALKEY_ADDR=<cell-a-valkey private host>:<port>
   KAFKA_BROKERS=cell-a-kafka.railway.internal:9092
   AUTH_SECRET=<same value as router's>
   APP_SECRET_ENCRYPTION_KEY=<openssl rand -base64 32>   # api only, but harmless everywhere
   TIERS_CONFIG=/etc/chat/tiers.yaml
   TOPOLOGY_CONFIG=/etc/chat/topology.yaml
   HTTP_ADDR=:8080
   METRICS_ADDR=:9100
   ```
   `cell-a-ws` additionally: `KAFKA_CONSUMER_GROUP=ws-us-east-1-a`.

4. **Point topology at the cell**: `infra/topology.yaml`'s `us-east-1-a`
   endpoints must resolve to the cell's services on Railway's private network
   (e.g. `http://cell-a-api.railway.internal:8080`). Edit and redeploy so the
   baked copy matches your service names.

5. **Migrations**: add GitHub Actions secrets `CONFIG_DATABASE_URL` (the
   config DB's *public* DSN) and `CELL_DATABASE_URLS` (comma-separated cell
   DB public DSNs), then run `deploy/railway/migrate.sh` from CI. App services
   use the private DSNs above; only the migration job, running outside
   Railway's network, needs the public ones.

## What's deliberately not here

Multi-cell, multi-region, and autoscaling are all just "more of the same"
(another cell trio + topology entry; more replicas of a role). This single
cell exists to stand the model up cheaply and prove the router → cell path
end to end, exactly as the local `deploy/docker-compose.yml` does.
