# Observability

Implements INSTRUCTIONS.md §37. Every service (`api`, `ws`, `worker`,
`router`, `control`) exposes metrics, structured logs, and a health check; a
lightweight tracing seam is wired through every request even though no
exporter is plugged in yet.

## Collection: cells push, one Grafana for the fleet

In the cell model (docs/adr/0006-cell-based-tenant-routing.md) each cell is a
self-contained failure domain, possibly in another region — so metrics are
**pushed**, not centrally scraped. Every cell runs a **Grafana Alloy**
collector (`deploy/observability/alloy/cell.alloy`) that scrapes its own
api/ws/worker `/metrics` every 5s, stamps each series with the cell's `region`
and `shard` labels, and **remote-writes** to a single central Prometheus
(`--web.enable-remote-write-receiver`). The central Prometheus directly
scrapes only the global services co-located with it (`router`, `control`,
`og-service`). **One Grafana** queries the central store and serves every
region and shard, sliceable by the `region`/`shard` labels the collectors
stamp. Adding a cell means running one more collector — no central config
change. (Alloy is interchangeable with a Vector or OpenTelemetry Collector
using a prometheus-scrape source + remote-write sink.) A production fleet
points remote-write at Grafana Mimir/Thanos/Cloud instead of one Prometheus;
only the URL changes.

## Metrics

Prometheus, served on a separate port from application traffic
(`METRICS_ADDR`, default `:9100`) so `/metrics` scraping is never affected by
application load or vice versa. `internal/platform/metrics.New(namespace)`
registers a namespaced set per service (`chat_api`, `chat_ws`,
`chat_worker`, `chat_router`, `chat_control`).

| Metric | Type | Where recorded |
|---|---|---|
| `http_requests_total{route,method,status}` | counter | `cmd/api` instrument middleware |
| `http_request_duration_seconds{route,method}` | histogram | same |
| `websocket_connections_active` | gauge | `cmd/gateway` connect/disconnect |
| `websocket_disconnects_total{reason}` | counter | `cmd/gateway` (`backpressure`, `write_error`, `client_disconnected`, `ping_failed`) |
| `messages_sent_total{region}` | counter | `cmd/api` on successful send |
| `message_persist_latency_seconds` | histogram | `cmd/api` around `messages.Repo.Send` |
| `message_delivery_latency_seconds` | histogram | `cmd/gateway` fanout, `time.Since(payload.CreatedAt)` at push time |
| `kafka_producer_latency_seconds` | histogram | `cmd/worker` outbox publish |
| `postgres_query_latency_seconds{query}` | histogram | `Metrics.TimePostgres` wrapper |
| `redis_latency_seconds{op}` / `redis_errors_total{op}` | histogram/counter | `Metrics.TimeRedis` wrapper |
| `rate_limit_rejections_total{capability}` | counter | `cmd/api` quota middleware |
| `quota_rejections_total{capability}` | counter | same |
| `cross_region_latency_seconds{from_region,to_region}` | histogram | `cmd/api` forwarding |

Query/op labels are always short, low-cardinality names (`"send_message"`,
`"list_messages"`) — never interpolated SQL, IDs, or channel names, both for
Prometheus cardinality reasons and because message content must never appear
in telemetry.

## Structured logs

`log/slog`, JSON handler, one logger per service pre-tagged with
`service`/`region` (`internal/platform/logging.New`). Every HTTP request
logs `request_id`, `route`, `method`, `status`, `duration_ms`
(`cmd/api/middleware.go`'s `instrument`). **Message bodies are never
logged** — INSTRUCTIONS.md §37 is explicit about this, and no log call
anywhere in the codebase includes a message `body` field.

## Tracing seam

`internal/platform/tracing` propagates `request_id`/`channel_id`/`region`/
`virtual_shard`/`physical_shard` through `context.Context` and assigns a
`request_id` per request, but doesn't create real spans — `Start(ctx, op)`
is a documented no-op seam where a real OTel exporter would be wired in.
This was a deliberate choice: pulling in a full tracing SDK isn't justified
until there's a real backend to export to (INSTRUCTIONS.md §46). Adding one
means replacing `tracing.Start`'s body, not changing any call site.

## Health checks

`GET /healthz` on every service, `internal/platform/health.Handler`, runs a
map of named checks with a 2-second timeout and returns 503 if any fail:

- `api`: control DB + every physical shard (`pool.Ping`, sourced from
  `router.PhysicalShards()` so it stays in sync with `shards.yaml`).
- `gateway`: Redis + control DB.
- `worker`: its one shard DB.

## What to add before a real production deployment

- Wire `internal/platform/tracing.Start` to a real OTel SDK + exporter.
- Add `kafka_consumer_lag` (the gauge exists —
  `Metrics.KafkaConsumerLagGap` — but nothing populates it yet; needs a
  periodic `kafka-go` admin-client lag query per consumer group).
- Alerting rules on the rejection counters and `websocket_disconnects_total{reason="backpressure"}` specifically — a rising backpressure-disconnect rate is the earliest signal a gateway or its downstream client is falling behind.
