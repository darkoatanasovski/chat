# Docker Compose Topology

`deploy/docker-compose.yml` stands up the entire architecture from
INSTRUCTIONS.md §41 on one machine — 13 containers, simulating 3 regions and
2 physical message shards.

## Services

| Service | Image / build | Purpose | Host port(s) |
|---|---|---|---|
| `postgres-control` | `postgres:16-alpine` | control-plane DB | `5433` |
| `postgres-shard-a` | `postgres:16-alpine` | message shard A | `5434` |
| `postgres-shard-b` | `postgres:16-alpine` | message shard B | `5435` |
| `kafka` | `apache/kafka:3.8.0` | single-node KRaft broker | `9092` |
| `valkey` | `valkey/valkey:7.2-alpine` | Redis-compatible cache/rate-limiter/registry | `6379` |
| `api-eu` / `api-us` / `api-asia` | built from `cmd/api` | REST API, one per region | `8081` / `8082` / `8083` |
| `gateway-eu` / `gateway-us` / `gateway-asia` | built from `cmd/gateway` | WebSocket edge, one per region | `8091` / `8092` / `8093` |
| `worker-outbox-a` / `worker-outbox-b` | built from `cmd/worker` | outbox publisher, one per shard | metrics `9101` / `9102` |

All `api`/`gateway`/`worker` images come from one `deploy/docker/Dockerfile`
(multi-stage Go build); a `SERVICE` build arg picks which `cmd/*` package to
build, so there's no duplicated Dockerfile logic across 8 services.

## Networking

Every service is on the default compose bridge network and reachable by
service name (`postgres-control`, `api-eu`, `kafka`, ...) — that's what
`PEER_API_EU_URL=http://api-eu:8080` etc. resolve against for cross-region
forwarding, and what lets every `api`/`worker` instance reach both shard
Postgres instances regardless of which "region" it represents (see
[../platform/multi-region.md](../platform/multi-region.md) for why that's
fine in this deployment).

## Config delivery

`deploy/shards.yaml` and `deploy/tiers.yaml` are mounted read-only into
every container that needs them (`/etc/chat/shards.yaml`,
`/etc/chat/tiers.yaml`) rather than baked into the image — editing either
file and restarting the affected services (no rebuild needed) is enough to
change shard topology or tier limits.

## Startup ordering and resilience

`depends_on` only guarantees container *start* order, not readiness — a
freshly-started Postgres container isn't immediately accepting connections.
Rather than fight this with complex healthcheck-gated `depends_on`
conditions, `internal/storage/postgres.Connect` and
`internal/storage/redis.Connect` retry for up to 30 seconds before giving
up, and every compute service (`api`/`gateway`/`worker`) has `restart:
unless-stopped` as a safety net. This is also just correct behavior for a
real deployment where a dependency can restart at any time
(INSTRUCTIONS.md §28) — not a docker-compose-specific workaround.

Postgres containers do have healthchecks (`pg_isready`) used for
`docker compose ps` visibility and local tooling, even though nothing
`depends_on`-blocks on them.

## Data persistence

Named volumes (`control-data`, `shard-a-data`, `shard-b-data`) survive
`make down` / `docker compose down`. To wipe all data:
`docker compose -f deploy/docker-compose.yml down -v`.

## Scaling this locally

To add a third physical shard: add a `postgres-shard-c` service, a
`worker-outbox-c` service, a `SHARD_C_DSN` env var threaded through
`common-env` and `internal/platform/config`, an entry in `deploy/shards.yaml`
under `physical_shards` (and shrink `shard-a`/`shard-b`'s virtual-shard
ranges to make room), and a `shardPools["shard-c"]` entry in `cmd/api/main.go`.
No application code needs to know shard count ahead of time beyond reading
`shards.yaml` — see
[../platform/sharding-and-routing.md](../platform/sharding-and-routing.md).
