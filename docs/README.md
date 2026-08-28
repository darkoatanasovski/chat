# Documentation

Documentation for the high-scale chat platform defined in
[`.claude/INSTRUCTIONS.md`](../.claude/INSTRUCTIONS.md) — its architecture,
API surface, operations, demo app, and the key design decisions behind it.

## Start here

New to this repo? Read in this order:
1. [platform/architecture-overview.md](platform/architecture-overview.md) — the whole system in one page
2. [operations/local-development.md](operations/local-development.md) — get it running
3. [api/rest-api.md](api/rest-api.md) — the endpoints
4. [demo/getting-started.md](demo/getting-started.md) — click through it

## Platform architecture

Deep dives into how and why the system is built the way it is.

- [architecture-overview.md](platform/architecture-overview.md) — services, package map, request flow
- [data-model.md](platform/data-model.md) — control-plane vs. shard databases, schema
- [sharding-and-routing.md](platform/sharding-and-routing.md) — virtual shards, physical shards, home-region cache
- [multi-region.md](platform/multi-region.md) — region simulation, cross-region write forwarding
- [realtime-delivery.md](platform/realtime-delivery.md) — WebSocket lifecycle, Kafka fanout, backpressure, reconnection
- [kafka-and-events.md](platform/kafka-and-events.md) — the outbox pattern, current and future events
- [quotas-and-tiers.md](platform/quotas-and-tiers.md) — the centralized tier/capability system
- [security.md](platform/security.md) — authentication model, authorization, input limits
- [observability.md](platform/observability.md) — metrics, logs, tracing seam, health checks

## API reference

- [rest-api.md](api/rest-api.md) — every REST endpoint
- [websocket-protocol.md](api/websocket-protocol.md) — the `/connect` protocol and delivery guarantees

## Operations

- [local-development.md](operations/local-development.md) — day-to-day dev workflow
- [docker-compose.md](operations/docker-compose.md) — the 13-container local topology
- [migrations.md](operations/migrations.md) — schema-change philosophy and the migration runner
- [load-testing.md](operations/load-testing.md) — what `tools/loadtest` measures and how to read it

## Demo app

- [getting-started.md](demo/getting-started.md) — running and using `demo/`

## Architecture decision records

- [0001-virtual-shards.md](adr/0001-virtual-shards.md)
- [0002-channel-home-region.md](adr/0002-channel-home-region.md)
- [0003-transactional-outbox.md](adr/0003-transactional-outbox.md)
- [0004-cursor-pagination.md](adr/0004-cursor-pagination.md)
- [0005-kraft-kafka-and-valkey.md](adr/0005-kraft-kafka-and-valkey.md)

## Claude Code skills

Project-specific skills live in [`.claude/skills/`](../.claude/skills/):
`platform-up` (start/stop the stack), `new-migration` (scaffold a schema
change), `new-event` (scaffold a new Kafka event end-to-end), `loadtest`
(run and interpret `tools/loadtest`).
