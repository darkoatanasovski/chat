# Chat Platform

A globally-distributed, horizontally-scalable chat backend built on a
**cell-based architecture**: users, channels, membership, messages, realtime
delivery, and tier-based quotas, where each tenant (App) is pinned to one
self-contained cell and a thin edge router sends every request to the right
one. See [`docs/adr/0006-cell-based-tenant-routing.md`](docs/adr/0006-cell-based-tenant-routing.md)
and [`infra/README.md`](infra/README.md) for the full design.

```
            api.chat.io (global)            us-east-1.api.chat.io (direct)
                  │                                   │
            ┌─────▼──────┐  apikey (?api_key= or token claim)
            │   router   │  → config DB: apikey → {region, shard}
            │ cmd/router │  → topology.yaml: shard → api/ws endpoints
            └─────┬──────┘  → reverse-proxy to that cell
      ┌───────────┼───────────────┐
  region us-east-1            region eu-west-1
  ┌───────────────────┐      ┌───────────────────┐
  │ cell us-east-1-a  │      │ cell eu-west-1-a  │
  │  2×api 2×ws        │      │  ...              │
  │  2×worker          │      │                   │
  │  Postgres+Kafka+   │      │                   │
  │  Valkey (own)      │      │                   │
  └───────────────────┘      └───────────────────┘
       every App (apikey) lives entirely in ONE cell
```

A **cell** (= a shard) is self-contained — its own Postgres, Kafka, and cache,
running 2+ each of `api`, `ws`, and `worker`. A **region** holds one or more
cells. The only global dependency is a small **config** database (the tenant
registry + per-app placement and settings), read cache-first by the router and
every cell service.

Everything is one binary — `chat api` / `chat ws` / `chat worker` /
`chat router` — selected by its first argument.

## Repository layout

```
cmd/chat/       the single binary; dispatches to a role (api/ws/worker/router/og)
cmd/{api,ws,worker,router,ogservice}/  the role implementations
internal/       domain logic + infra wrappers
  appconfig/    apikey → cell resolver over the config DB (cache-first)
  topology/     loads infra/topology.yaml (regions → cells → endpoints)
infra/          topology.yaml + the routing/deployment model (start here)
migrations/     config/ (global) and cell/ (per-cell) additive SQL
deploy/         docker-compose.yml, single Dockerfile, migrate + seed, railway/
docs/           architecture, API reference, operations, ADRs (start at docs/README.md)
```

## Quick start

```bash
cp .env.example .env
make up                # build the chat image + start router, config DB, one cell
./deploy/migrate.sh    # apply config + cell schema (idempotent)
./deploy/seed.sh        # optional: demo org/app/users/channel

cd demo && npm install && npm run dev   # http://localhost:3000
```

The router is on `http://localhost:8080` (the global endpoint); the cell's api
and ws are also exposed directly on `8081`/`8091` for debugging. Full
walkthrough: [`docs/operations/local-development.md`](docs/operations/local-development.md);
Railway: [`docs/operations/railway-dev.md`](docs/operations/railway-dev.md).

## Documentation

Everything else — architecture deep-dives, API reference, operations,
architecture decision records — is indexed at
[`docs/README.md`](docs/README.md). Note: some platform deep-dives under
`docs/platform/` still describe the pre-cell (virtual-shard / home-region)
design and are being updated to match ADR 0006.

## Status

Cell-based routing (single binary, config DB, edge router, per-cell data) is
in and compiles clean (`go build ./...`, `go vet ./...`). The integration test
suite and the `docs/platform/` deep-dives are mid-migration to the new model;
see the ADR for the authoritative design.
