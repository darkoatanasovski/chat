# Cloudflare Worker edge router

The production edge for the cell-based platform — a drop-in alternative to the
in-repo Go router (`cmd/router`), running at Cloudflare's edge. Both implement
the **same routing contract** (docs/adr/0006-cell-based-tenant-routing.md), so
which one fronts production is a deployment choice, not a code change:

- **`cmd/router`** — runs anywhere (docker-compose, any host); reads placement
  from the config DB. Used for local dev and portability.
- **this Worker** — global anycast, TLS, DDoS protection at the edge; reads
  placement from Cloudflare KV, kept current from the config DB by `sync-kv.sh`.

```
        api.chat.io (Cloudflare)
              │
        ┌─────▼───────┐  /organizations,/apps,/dashboard,/dodo → CONTROL_ORIGIN
        │   Worker    │  else: apikey (?api_key= or token) → KV → {region,shard}
        │ worker.js   │        → REGIONS[region] origin  (WS upgrades tunnel through)
        └─────┬───────┘
      ┌───────┴────────────┐
 us-east-1.api.chat.io   eu-west-1.api.chat.io   ...
```

## Files

| File | Purpose |
|---|---|
| `src/worker.js` | the Worker: apikey → cell, proxy to region origin / control |
| `wrangler.toml` | Worker config: KV binding, `REGIONS`, `CONTROL_ORIGIN` |
| `sync-kv.sh` | push config DB placements → KV (run on change + on a schedule) |

## Why KV (not Postgres at the edge)

The Worker must not hit Postgres per request. KV is the edge cache; the config
DB stays the source of truth. `sync-kv.sh` writes `apikey:<key>` and
`appid:<id>` → `{region, shard}` — the same eventually-consistent,
invalidate-on-change model `internal/appconfig` already uses, just at the edge.

## Setup

```bash
cd infra/cloudflare
wrangler kv namespace create PLACEMENT      # paste the id into wrangler.toml
wrangler secret put AUTH_SECRET             # = the platform AUTH_SECRET (edge token verify)
# edit wrangler.toml: REGIONS (region→origin) and CONTROL_ORIGIN
wrangler deploy

# populate / refresh the placement map from the config DB:
CONFIG_DATABASE_URL=... CF_ACCOUNT_ID=... CF_KV_NAMESPACE_ID=... CF_API_TOKEN=... ./sync-kv.sh
```

Point `api.chat.io` at the Worker (route or custom domain). Regional
hostnames (`us-east-1.api.chat.io`) point straight at each region's api origin,
so a client can also address a region directly, bypassing apikey routing.

## Keeping KV current

Run `sync-kv.sh` whenever placement or credentials change (new app, new/revoked
credential, a future tenant-move), plus on a cron as a safety net. A revoked
credential's `apikey:*` entry should be deleted; `sync-kv.sh` currently writes
active entries — deleting revoked keys is a small follow-up (bulk DELETE of keys
absent from the active set).

## Status

Scaffold — not yet deployed or load-tested. `worker.js` is plain ES-module
JavaScript (no build step). Verify against a staging Cloudflare account before
fronting production.
