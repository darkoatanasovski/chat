# Railway dev deployment

A single-region deployment of this platform on [Railway](https://railway.app),
meant for fast iteration during development — not a replacement for the
multi-region, Sentinel-backed, horizontally-scaled architecture the rest of
`deploy/` describes. See the "why not just run docker-compose everywhere"
discussion this setup came out of: Railway earns its place here for
git-push deploys and managed Postgres/Redis without needing to run
`docker compose` on a box somewhere, at the cost of the multi-region and HA
properties this platform is otherwise built for.

## Topology

One region's worth of services instead of three, and one Postgres instance
instead of three:

| Railway service | What it runs | Deployed from |
|---|---|---|
| `api` | `cmd/api` | `deploy/railway/api.Dockerfile` |
| `gateway` | `cmd/gateway` | `deploy/railway/gateway.Dockerfile` |
| `worker` | `cmd/worker` | `deploy/railway/worker.Dockerfile` |
| `postgres` | Postgres | Railway's built-in Postgres plugin |
| `redis` | Valkey/Redis | Railway's built-in Redis plugin, or the `valkey/valkey:7.2-alpine` public image |
| `kafka` | Kafka (KRaft, single broker) | the `apache/kafka:3.8.0` public image — same one `deploy/docker-compose.yml` already uses |

`postgres` hosts three logical databases — `control`, `shard_a`, `shard_b`
— instead of three separate Postgres instances. The app doesn't care: it
just needs three distinct connection strings (`CONTROL_DSN`, `SHARD_A_DSN`,
`SHARD_B_DSN`), and the migration script (`deploy/railway/migrate.sh`)
creates all three databases on first run if they don't exist yet.

No Sentinel, no Redis replica, no multiple Kafka partition-consumer
instances: this environment has one of everything. That's the deliberate
trade for "cheap and fast to stand up," not an oversight — round 2 and
round 4 of the load-test/fanout work (see the published report) are what
you'd re-enable by pointing this same app at the full `deploy/docker-compose.yml`
topology instead.

## One-time setup

1. **Create a Railway project** and, inside it, add:
   - A **Postgres** service (Railway's built-in plugin). Note its connection
     string — you'll need it for `RAILWAY_POSTGRES_URL` below.
   - A **Redis** service (built-in plugin, or deploy the `valkey/valkey:7.2-alpine`
     image directly — either works, this app only needs standard Redis
     commands).
   - A **Kafka** service deployed from the public image `apache/kafka:3.8.0`.
     Name it `kafka` (Railway's private networking exposes it as
     `kafka.railway.internal`, which the env vars below assume). Set its
     environment to match `deploy/docker-compose.yml`'s `kafka` service,
     minus the host-facing `PLAINTEXT_HOST` listener (nothing outside the
     Railway project needs to reach it directly):
     ```
     KAFKA_NODE_ID=1
     KAFKA_PROCESS_ROLES=broker,controller
     KAFKA_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093
     KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://kafka.railway.internal:9092
     KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER
     KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
     KAFKA_CONTROLLER_QUORUM_VOTERS=1@kafka.railway.internal:9093
     KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1
     KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1
     KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1
     KAFKA_AUTO_CREATE_TOPICS_ENABLE=true
     KAFKA_NUM_PARTITIONS=6
     CLUSTER_ID=Q2hhdFBsYXRmb3JtS1JhZnQx
     ```

2. **Create the three app services** (`api`, `gateway`, `worker`), each
   deployed from this GitHub repo with its "Config File Path" / Dockerfile
   path (whichever your Railway UI version calls it) set to its file under
   `deploy/railway/`:
   - `api` → `deploy/railway/api.Dockerfile`
   - `gateway` → `deploy/railway/gateway.Dockerfile`
   - `worker` → `deploy/railway/worker.Dockerfile`

   Set each service's environment variables from the table below — Railway
   lets you reference another service's connection info directly (e.g.
   `${{Postgres.DATABASE_URL}}`) instead of copy-pasting values; use that
   wherever available instead of the literal values shown here.

   **api**
   ```
   REGION=eu
   HTTP_ADDR=:8080
   METRICS_ADDR=:9100
   CONTROL_DSN=<postgres DSN>/control
   SHARD_A_DSN=<postgres DSN>/shard_a
   SHARD_B_DSN=<postgres DSN>/shard_b
   VALKEY_ADDR=<redis private host>:<port>
   KAFKA_BROKERS=kafka.railway.internal:9092
   AUTH_SECRET=<generate: openssl rand -hex 32>
   APP_SECRET_ENCRYPTION_KEY=<generate: openssl rand -base64 32>
   SHARDS_CONFIG=/etc/chat/shards.yaml
   TIERS_CONFIG=/etc/chat/tiers.yaml
   PEER_API_EU_URL=https://<api's public Railway domain>
   CORS_ALLOWED_ORIGINS=<your frontend's origin>
   ```
   `PEER_API_US_URL`/`PEER_API_ASIA_URL` can stay unset — this deployment
   only has one region, so there's nothing to forward cross-region writes
   to.

   **gateway**
   ```
   REGION=eu
   HTTP_ADDR=:8080
   METRICS_ADDR=:9100
   CONTROL_DSN=<postgres DSN>/control
   VALKEY_ADDR=<redis private host>:<port>
   KAFKA_BROKERS=kafka.railway.internal:9092
   KAFKA_CONSUMER_GROUP=gateway-fanout
   AUTH_SECRET=<same value as api's>
   SHARDS_CONFIG=/etc/chat/shards.yaml
   TIERS_CONFIG=/etc/chat/tiers.yaml
   ```
   `FANOUT_SHARDS` can stay unset (defaults to 16) unless you have a
   specific reason to change it.

   **worker** — one instance per shard, so add it twice (e.g. `worker-a`
   and `worker-b`), or run one instance and accept that only one shard's
   outbox gets published (fine for early dev, not for anything real):
   ```
   SHARD_ID=shard-a
   SHARD_DSN=<postgres DSN>/shard_a
   KAFKA_BROKERS=kafka.railway.internal:9092
   METRICS_ADDR=:9100
   ```
   (swap `shard-a`/`shard_a` for `shard-b`/`shard_b` on the second instance)

3. **Generate a Railway API token** scoped to this project (Railway's
   dashboard: project settings → Tokens) and add it to this repo's GitHub
   Actions secrets as `RAILWAY_TOKEN`.

4. **Add `RAILWAY_POSTGRES_URL`** as a GitHub Actions secret too — the
   Postgres service's *public* connection string (something like
   `postgres://user:pass@containers-us-west-x.railway.app:port/railway`).
   The migration job runs from a GitHub-hosted runner, outside Railway's
   private network, so it needs the public endpoint; the app services
   themselves should still use the private one (`CONTROL_DSN` etc. above)
   since it's faster and doesn't leave Railway's network.

## Deploying

Push to `master` (or merge a PR into it) and
`.github/workflows/deploy-railway.yml` takes it from there: build + vet as
a gate, then `deploy/railway/migrate.sh` against the real database, then
`railway up --service <name>` for `api`, `gateway`, and `worker` in turn.

To deploy by hand instead (e.g. before the GitHub Actions secrets are set
up): install the Railway CLI (`npm i -g @railway/cli`), `railway login`,
`railway link` to this project, then run `railway up --service <name>` for
each service from the repo root, and `DATABASE_URL=<public postgres URL>
bash deploy/railway/migrate.sh` for migrations.

## What's deliberately not here

No Sentinel, no Redis replica, no multi-partition-consumer fan-out
exercise, no cross-region routing, no autoscaling policy. This is a dev
environment, not a load-bearing one — see the main report artifact for
what changes (and why) when this app runs its full topology instead.
