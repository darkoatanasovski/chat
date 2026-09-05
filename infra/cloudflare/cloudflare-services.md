# Using Cloudflare out of the box

Where the platform has an edge, an operational chore, or a piece of managed
infrastructure, Cloudflare usually has a first-party service that replaces it
with zero (or near-zero) code. This maps every such concern to the Cloudflare
product and says how to wire it.

Tiers: **Adopt** = wired in this repo (or a one-line binding away). **Recommend**
= clear win, config-only, do it. **Consider** = a real architectural option
with trade-offs.

## The routing / control edge

| Concern | Cloudflare service | Tier | How |
|---|---|---|---|
| apikey → cell routing | **Workers** (`src/worker.js`) | Adopt | The edge router. Deploy with `wrangler deploy`. |
| Placement cache (apikey→cell) | **Workers KV** (`PLACEMENT`) | Adopt | Populated by `sync-kv.sh`; read per request. |
| Refresh placement on a schedule | **Cron Triggers** | Adopt | `[triggers] crons` in `wrangler.toml` → the Worker's `scheduled()` pulls placements and updates KV. |
| Verify auth tokens at the edge | **Workers + Secrets** | Adopt | `wrangler secret put AUTH_SECRET` → HMAC-verify in the Worker (Web Crypto). |
| Global HTTPS / custom domains / DNS | **DNS + SSL/TLS (universal)** | Adopt | Point `api.chat.io` + `*.api.chat.io` at Workers/origins; free managed certs. |
| Geo-steer to nearest region origin | **Load Balancing** (geo-steering, health checks) | Consider | Alternative/complement to the Worker for the *regional* hop; the Worker still does the apikey→cell decision. |
| Accelerate control-plane Postgres from Workers | **Hyperdrive** | Recommend (if control plane moves to Workers) | Connection pooling + query cache in front of the config Postgres. |
| Config DB as serverless SQLite | **D1** | Consider | The config DB is small/read-mostly/global — a fit — but the Go control plane speaks Postgres; adopting D1 means the Worker (or a rewrite) owns those reads. |

## Realtime & events (the ws/worker/Kafka path)

| Concern | Cloudflare service | Tier | How |
|---|---|---|---|
| Per-channel ordered fanout + presence | **Durable Objects** | Consider | One DO per channel is a natural actor for ordered delivery, presence, and sequence assignment — could replace the ws+Redis-pubsub fanout at the edge. Large rearchitecture; evaluate against the Kafka-per-cell model. |
| Outbox → fanout event bus | **Queues** | Consider | Cloudflare Queues can stand in for the per-cell Kafka if realtime moves to Workers/DOs. |
| Edge WebSocket termination | **Workers** (WebSocket API) | Adopt | The Worker already tunnels WS upgrades to the cell's `ws`; DOs would let CF terminate them directly. |

## Storage & content

| Concern | Cloudflare service | Tier | How |
|---|---|---|---|
| Message attachments / uploads | **R2** (S3-compatible, no egress fees) | Recommend | The platform already expects app-hosted object storage (migrations/cell `attachments`); R2 is the drop-in host. Bind `ATTACHMENTS` and issue presigned/PUT URLs. Snippet below. |
| Cache GET responses / static assets | **Cache / Tiered Cache / Argo** | Recommend | Cache read-only API responses and frontend assets at the edge. |
| Host the demo / console frontends | **Pages** | Recommend | `console/` and `demo/` are Next.js — deploy to Pages with Git integration. |
| Link-preview image/asset caching | **Cache** + **Image Resizing** | Consider | Front the OG service; resize/optimize preview images. |

## Security & abuse

| Concern | Cloudflare service | Tier | How |
|---|---|---|---|
| L3/4 + L7 DDoS | **DDoS protection** (always-on) | Adopt | On by default once traffic is proxied (orange-cloud). |
| WAF / OWASP / managed rules | **WAF** | Recommend | Enable Managed Rulesets; add custom rules (e.g. block non-API paths). |
| Edge rate limiting (abuse, not tier quotas) | **Rate Limiting Rules** | Recommend | Coarse per-IP/edge limits; the app keeps its per-tier `quota` limits. |
| Bot filtering | **Bot Management / Super Bot Fight** | Recommend | Challenge automated abuse before it reaches origins. |
| Dashboard/admin/Grafana access | **Access (Zero Trust)** | Recommend | Put the console, Grafana, and any `/internal/*` behind Access policies (SSO, no VPN). |
| Signup bot protection | **Turnstile** | Recommend | Add the widget to dashboard signup; verify the token in the control plane (or the Worker) before `POST /dashboard/signup`. |
| Secrets management | **Workers Secrets** | Adopt | `AUTH_SECRET`, API tokens — never in `wrangler.toml` vars. |

## Observability & ops

| Concern | Cloudflare service | Tier | How |
|---|---|---|---|
| Edge request metrics | **Workers Analytics Engine** | Adopt | Bind `EDGE_ANALYTICS`; the Worker writes one datapoint per request (region/shard/status). Query from Grafana via the SQL API. |
| Ship edge/HTTP logs | **Logpush** | Recommend | Push Worker + HTTP logs to R2 / your log store. |
| Scheduled jobs (KV sync, cleanups) | **Cron Triggers** | Adopt | Already used for the KV refresh; add more `scheduled()` branches as needed. |
| Email (org invites “stand in for email”) | **Email Routing** / Workers email | Consider | Turn the invite links into real delivered email. |

## What this repo wires now

- **Workers + KV + Secrets + Cron Triggers + Analytics Engine** — `src/worker.js`
  + `wrangler.toml` (routing, placement cache, edge token verify, scheduled KV
  refresh, per-request analytics).
- **R2 attachments** — `/uploads` handler in `src/worker.js` + `ATTACHMENTS`
  binding in `wrangler.toml`. Create the bucket: `wrangler r2 bucket create chat-attachments`.
- **Durable Objects realtime** (experimental) — `realtime/` worker: one
  `ChannelRoom` DO per channel for edge WS fanout + presence. Evaluate against
  the ws+Kafka path; not yet wired into the send flow.
- **Turnstile** — server verify done in `cmd/api/turnstile.go` (control-plane
  signup), gated by `TURNSTILE_SECRET`. Frontend widget: `frontends-and-access.md`.
- **D1 / Hyperdrive** — `d1/schema.sql` (edge config/placement projection) and
  commented `wrangler.toml` bindings with enable steps.
- Everything else under **Recommend** that is dashboard-config-only (WAF, DDoS,
  Rate Limiting, Bot, **Access**, Cache, **Pages**, Logpush) needs no code —
  see `frontends-and-access.md` and enable it in the dashboard for the `chat.io`
  zone.

## R2 attachments — ready snippet

Bind R2 in `wrangler.toml`:

```toml
[[r2_buckets]]
binding = "ATTACHMENTS"
bucket_name = "chat-attachments"
```

Handle uploads in a Worker route (returns the object URL the client then sends
as a message `attachment`):

```js
// POST /uploads  (multipart or raw body)
const key = `${crypto.randomUUID()}`;
await env.ATTACHMENTS.put(key, request.body, {
  httpMetadata: { contentType: request.headers.get("content-type") },
});
return Response.json({ url: `https://cdn.chat.io/${key}` }); // R2 custom domain
```

Keep object hosting off the cell data path — R2 serves attachments directly
via its custom domain / cache, exactly the "app integrates its own object
storage" division the platform already assumes.
