# Multi-Region

> Superseded model note: there is no longer a per-channel `home_region` or
> cross-region write forwarding. Authoritative design:
> [ADR 0006](../adr/0006-cell-based-tenant-routing.md).

## Regions and cells

A **region** (e.g. `us-east-1`, `eu-west-1`) holds one or more **cells**. A
cell is a self-contained shard (its own Postgres, Kafka, cache, running
`api`/`ws`/`worker`). Every App is pinned to one cell — so an App is entirely
within one region, chosen at creation (data residency: create the App in the
region you want).

## No cross-region forwarding

The old design let any region receive a write and forwarded it to the channel's
`home_region`. That's gone: the edge router resolves apikey → the App's cell and
proxies the request straight there, so the request is always handled by the one
authoritative cell. There is no app-layer region forwarding and no
`PeerAPIURL`.

Sequence assignment (`internal/messages`) stays correct because a channel's
messages live in exactly one cell's Postgres — one process assigns the next
sequence, as before, without any cross-region coordination.

## Realtime is per-cell

Each cell has its own Kafka and its own set of `ws` instances sharing one
consumer group. A message published in a cell fans out only within that cell —
which is sufficient because every member of a channel is in the same cell as the
channel (they belong to the same App). There is no global cross-region fanout.

## Global vs per-cell

The only global components are the **config** database (tenant registry +
placement + settings) and the **control** plane (`chat control`, org/dashboard/
billing) and the **edge router**. Everything else is per-cell. See
[infra/README.md](../../infra/README.md).
