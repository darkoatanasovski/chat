# Sharding and Routing

> Superseded model note: this platform previously routed on `channel_id`
> through `home_region → virtual_shard → physical_shard`. That is gone. Routing
> is now tenant-based. Authoritative design:
> [ADR 0006](../adr/0006-cell-based-tenant-routing.md) and
> [infra/README.md](../../infra/README.md).

## How routing works now

Every **App** (tenant, identified by its apikey) is pinned to exactly one
**cell** at creation. A cell is a self-contained shard — its own Postgres,
Kafka, and cache — living in one region. All of an App's users, channels, and
messages live in that cell.

The routing decision is made **once, at the edge**, keyed by apikey:

```
apikey (?api_key= or a verified token claim)
  → config DB: app_credentials/apps → {region, shard}   (internal/appconfig, cache-first)
  → infra/topology.yaml: {region, shard} → that cell's api/ws endpoints
  → reverse-proxy to the cell
```

Two edges implement this identical contract: the Cloudflare Worker
(`infra/cloudflare`, production) and the in-repo Go router (`cmd/router`, dev /
portable). The config DB is the source of truth; the edge caches placement
(KV, or Redis) with a read-through/TTL fallback, so a new App routes
immediately and stale entries self-heal.

## Why no virtual shards

Virtual shards existed to make `channel_id → physical instance` rebalanceable
without rehashing. In the cell model placement is **explicit** (an App row's
`region`/`shard`), not hashed, so rebalancing is "move a tenant to another
cell" (a data copy + a placement flip — the `InvalidateRoute` seam) rather than
a rehash of the whole key space. No hash indirection is needed.

## Within a cell

There is no further sharding inside a cell: one Postgres holds all of the
cell's tenant data. `internal/routing` still answers one small question —
`channel_id → app_id` (cached) — used only for tenant-isolation checks
(`route.AppID == identity.AppID`), never for placement.

## Adding capacity

- **Within a cell:** raise the api/ws/worker replica counts.
- **New cell:** add a `cells` entry in `infra/topology.yaml`, provision its
  Postgres/Kafka/cache, apply `migrations/cell`, and start pinning new Apps'
  `shard` to it. No existing tenant moves; no hash changes.
