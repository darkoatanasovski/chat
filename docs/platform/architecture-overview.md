# Architecture Overview

A cell-based, multi-region chat platform. The authoritative design is
[docs/adr/0006-cell-based-tenant-routing.md](../adr/0006-cell-based-tenant-routing.md)
and [infra/README.md](../../infra/README.md); this is the summary.

## The model

Route on the **tenant** (an App, identified by its apikey), decided once at the
edge — not per-channel, deep in the app.

```
        edge router (Cloudflare Worker or cmd/router)
   apikey (?api_key= / token) → config DB: {region, shard} → proxy to that cell
                    │
   region us-east-1                 region eu-west-1
   ┌───────────────────┐            ┌───────────────────┐
   │ cell us-east-1-a  │            │ cell eu-west-1-a  │
   │  2×api 2×ws        │            │  ...              │
   │  2×worker          │            │                   │
   │  Postgres+Kafka+   │            │                   │
   │  Valkey (own)      │            │                   │
   └───────────────────┘            └───────────────────┘
       every App (and all its data) lives in ONE cell
```

- A **cell** (= a shard) is self-contained: its own Postgres, Kafka, and cache,
  running `chat api` / `chat ws` / `chat worker`. A **region** holds one or
  more cells. Nothing in one cell reads another cell's data.
- The only global dependency is a small **config** database (the tenant
  registry + per-app placement and settings), read cache-first by the router
  and every cell service (`internal/appconfig`).

## Roles (one binary)

Everything is the single `chat` binary; the first arg selects the role:

| Role | Was | Responsibility |
|---|---|---|
| `chat api` | cmd/api | REST data plane for one cell |
| `chat ws` | cmd/gateway | WebSocket edge for one cell (Kafka fanout) |
| `chat worker` | cmd/worker | transactional-outbox publisher + retention/reminders for one cell |
| `chat control` | — | global org/dashboard/billing plane (config DB + cross-cell admin) |
| `chat router` | — | apikey → cell edge router (dev/portable; Cloudflare is the prod edge) |
| `chat og` | cmd/ogservice | OpenGraph link-preview scraper |

## Request flow (send a message)

```
client → edge router (apikey → cell) → api-<cell>
  verify token, membership + rate limit
  BEGIN
    INSERT messages (channel_id, sequence, ...)   (internal/messages)
    INSERT outbox_events                           (internal/events)
  COMMIT
  → chat worker polls outbox_events, publishes to the cell's Kafka
  → every ws in the cell consumes, delivers to its local members' sockets
```

There is no cross-region write forwarding and no virtual-shard indirection —
the edge already sent the request to the one cell that owns the App.

## Data model

- **config DB** (`migrations/config`): organizations, apps (+ region/shard
  placement + settings), app_credentials, org_users, org_invites,
  translation_usage.
- **cell DB** (`migrations/cell`, applied identically to every cell): users,
  channels, membership, `user_channels`, blocks, mutes, bookmarks, and the
  full message log (messages, reactions, polls, threads, pins, translations).
  `app_id` on these rows carries no FK — `apps` is in a different database.

## Package map

```
cmd/chat/         the binary; dispatches to a role
internal/
  appconfig/      apikey/app_id -> AppConfig (placement+settings), cache-first
  topology/       loads infra/topology.yaml (regions -> cells -> endpoints)
  routing/        channel_id -> app_id cache (tenant-isolation checks)
  users/ channels/ membership/ messages/ events/ reactions/ polls/ ...  domain
  quota/ realtime/ storage/ platform/  quotas, WS hub/fanout, infra wrappers
```

See the sibling docs for deep dives (some still being updated to the cell
model): [data-model.md](data-model.md), [kafka-and-events.md](kafka-and-events.md),
[realtime-delivery.md](realtime-delivery.md), [quotas-and-tiers.md](quotas-and-tiers.md),
[observability.md](observability.md), [security.md](security.md).
