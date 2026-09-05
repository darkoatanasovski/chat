# infra/ — topology & routing

This directory defines the platform's **physical topology** and the **routing
model** that sits in front of it. It is the source of truth the router, the
local `docker-compose` stack, and the Railway deployment all derive from.

The full rationale is in
[docs/adr/0006-cell-based-tenant-routing.md](../docs/adr/0006-cell-based-tenant-routing.md).
This README is the operational summary.

## The model in one picture

```
                api.chat.io (global)         us-east-1.api.chat.io (direct)
                      │                                │
                ┌─────▼───────┐  apikey (?api_key= or token claim)
                │   router    │  → config DB: apikey → {region, shard}
                │ cmd/router  │  → topology.yaml: shard → api/ws endpoints
                └─────┬───────┘  → reverse-proxy to that cell
        ┌─────────────┼──────────────────┐
   region us-east-1                  region eu-west-1
   ┌───────────────────┐            ┌───────────────────┐
   │ cell us-east-1-a  │            │ cell eu-west-1-a  │
   │  2×api  2×ws       │            │  2×api  2×ws       │
   │  2×worker          │            │  2×worker          │
   │  Postgres          │            │  Postgres          │
   │  Kafka             │            │  Kafka             │
   │  Cache             │            │  Cache             │
   └───────────────────┘            └───────────────────┘
              ▲
              └── every tenant (App) lives entirely in ONE cell
```

## Cells (shards)

A **cell** is a self-contained shard. It owns its Postgres, Kafka, and cache,
and runs `replicas` copies each of `api`, `ws`, and `worker` (all the same
`chat` binary, `chat api` / `chat ws` / `chat worker`). Nothing in one cell
reads or writes another cell's data — there is no cross-cell forwarding and no
global Kafka fanout. A cell is one failure domain.

Cells are declared in [`topology.yaml`](topology.yaml).

## The `config` database (global control plane)

The one datastore shared across every cell. Small, read-mostly, and cached by
every reader (invalidate on change, else 1-day TTL fallback). It holds, per
**App** (the tenant, identified by apikey):

| Data | Used by | Replaces |
|---|---|---|
| placement — `region`, `shard` | router (to route), services (to self-locate) | `home_region`, `virtual_shard`, `shards.yaml` |
| credentials — apikey + encrypted secret | router (auth), api (`/apps/token`) | today's `app_credentials` |
| settings — tier, capabilities, flags | every cell service | today's live `internal/apps` reads |

Because a tenant is pinned to one cell, **all tenant-scoped tables live in the
cell's Postgres** (users, channels, membership, `user_channels`, messages,
reactions, blocks, bookmarks, …). The `config` DB keeps only what is
inherently global: organizations, apps, credentials, placement, settings, and
org-level billing.

## Routing

1. Request arrives at the router (global `api.chat.io`, or a regional
   hostname).
2. Router extracts the apikey — `?api_key=` query param **or** the `app_id`/
   `api_key` claim of a verified bearer token.
3. Router resolves `apikey → {region, shard}` from the `config` DB (cached).
4. Router looks up that shard's `api`/`ws` endpoints in `topology.yaml` and
   reverse-proxies the request (HTTP for `api`, WebSocket upgrade for `ws`).

A request that names a region directly (regional hostname) still resolves the
shard by apikey within that region; a mismatch (apikey pinned to another
region) is a 421-style "misrouted" response telling the client to use the
global endpoint.

## Files

| File | Purpose |
|---|---|
| [`topology.yaml`](topology.yaml) | regions → cells → endpoints + per-cell infra env vars |
| [`cloudflare/`](cloudflare/) | Cloudflare Worker edge (production alternative to `cmd/router`) + KV sync |
| `../deploy/docker-compose.yml` | local stack: router + control + config DB + one cell |
| `../deploy/railway/` | Railway deployment (one service per role) |
| `../cmd/router` | the in-repo edge router (dev/portability) |
| `../internal/appconfig` | config-DB client + cached app-config resolver |

## Scaling knobs

- **Within a cell:** raise `replicas.api/ws/worker`.
- **New cell:** add a `cells` entry, provision its datastores, apply the shard
  migrations, and start pinning new Apps' `shard` to it in the config DB. No
  existing tenant moves and no hash changes — the pain ADR 0001's virtual
  shards existed to avoid is gone because placement is explicit, not hashed.
