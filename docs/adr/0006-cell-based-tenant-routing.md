# ADR 0006: Cell-Based Tenant Routing

## Status

Accepted. **Supersedes [0001](0001-virtual-shards.md) (virtual shards) and
[0002](0002-channel-home-region.md) (channel home-region).**

## Context

The V1 architecture routed on `channel_id` across two independent axes:

- `home_region` — an immutable per-channel fact deciding which region's `api`
  may write it, enforced by **app-layer cross-region forwarding**
  (`cmd/api/forward.go`).
- `virtual_shard = hash(channel_id) % 4096` — mapped by `deploy/shards.yaml`
  ranges onto physical Postgres instances, with **every `api` instance
  connected to every shard**.

Realtime made this heavier still: every `gateway`/`ws` instance consumed the
*entire* Kafka topic and filtered against local connections.

This is a lot of surface area for a starting point: 4096 virtual shards, a
region-vs-shard orthogonality, per-request home-region forwarding, global
Kafka fanout, and every service reaching every datastore. Correct and
scalable, but the mental model is large and every service has a wide blast
radius.

## Decision

Route on the **tenant** (an App, identified by its apikey), decided **once at
the edge**, and make each shard a **self-contained cell**.

- A **cell** (= a shard) is `{2+ api, 2 ws, 2 worker, its own Postgres, its
  own Kafka, its own cache}`. A **region** contains one or more cells.
- Every App is **pinned to exactly one cell** at creation. All of that App's
  tenant data — users, channels, membership, messages, and every other
  tenant-scoped table — lives in that cell's Postgres. There are **no
  cross-cell reads or writes**.
- A thin **router** (`cmd/router`) reads the apikey (from `?api_key=` or a
  verified token claim), looks up `apikey → {region, shard}` in a small
  global **`config` database**, and reverse-proxies to that cell's `api`/`ws`.
  The global hostname (`api.chat.io`) routes by apikey; per-region hostnames
  (`us-east-1.api.chat.io`) address a region directly.
- The `config` DB is the *only* global dependency. It stores, per App:
  placement (`region`, `shard`), credentials (apikey + encrypted secret), and
  settings (tier, capabilities, flags). Every reader — router and cell
  services alike — caches it, invalidated on change or after a 1-day TTL.

Because a tenant lives entirely in one cell, `home_region`, `virtual_shard`,
the 4096-virtual-shard table, cross-region forwarding, and global Kafka
fanout are all **removed**, not reconfigured.

## Alternatives considered

**Keep channel-level routing, just simplify the config.** Rejected: the cost
isn't the config, it's the two orthogonal axes and the app-layer forwarding
they force. Tenant-level pinning collapses both axes into one lookup and
deletes the forwarding path entirely.

**Global Kafka + cache, only Postgres per cell.** Considered (it's cheaper),
but a shared regional Kafka keeps a region-wide blast radius and forces `ws`
consumers to filter a firehose to their own shard. Full per-cell isolation
was chosen so "add a shard" is a self-contained unit with no shared realtime
plane.

**Delegate routing to Envoy/Cloudflare instead of an in-repo app.** The
apikey→cell decision is domain logic (it reads the same `config` DB and token
format the services use); keeping it as a small, testable Go service keeps it
honest. A production edge could later read the same `config` DB from Envoy
without changing the model.

## Consequences

- One routing decision, at the edge, keyed by apikey — no per-request,
  per-channel forwarding anywhere in the data path.
- Each cell is an isolated failure domain: one cell down affects only the
  tenants pinned to it.
- "Scale" becomes two clear knobs: add instances *within* a cell, or add a
  *new* cell and pin new tenants to it.
- Tenant-scoped tables move from the global control DB into the shard DB (see
  the data-model note in `infra/README.md`); the global DB shrinks to
  orgs/apps/credentials/placement/settings (+ org-global billing).
- Moving a tenant between cells is now a real (future) operation — a
  data copy plus a `config` placement flip — rather than the never-built
  "move channel region." The `config` cache invalidation hook is the seam for
  it.

## Where this lives in code

`cmd/router` (edge routing), `internal/config` (the `config` DB client +
cached app-config resolver, replacing `internal/routing`'s
`RegionResolver`/virtual-shard `Router`), `infra/topology.yaml` (regions →
cells → endpoints + per-cell infra), and per-cell wiring in `cmd/api`,
`cmd/ws`, `cmd/worker` (all built from the single `chat <role>` binary).
