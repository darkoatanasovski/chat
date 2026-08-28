# Architecture Overview

This platform implements the V1 slice of `.claude/INSTRUCTIONS.md`: users,
channels, channel membership, sending/retrieving messages, listing a user's
channels, realtime delivery, and tier-based quotas — built on an architecture
designed to scale to millions of connections without a rewrite.

## Services

Three deployable Go binaries, one modular monolith's worth of internal
packages:

| Service | Binary | Responsibility |
|---|---|---|
| API | `cmd/api` | Stateless REST service: auth, quota enforcement, reads/writes, cross-region write forwarding |
| Gateway | `cmd/gateway` | WebSocket edge: connection lifecycle, Kafka-driven realtime fanout, backpressure |
| Worker | `cmd/worker` | Transactional-outbox publisher, one instance per physical Postgres shard |

Each is deployed three times (once per simulated region — `eu`/`us`/`asia`)
except the worker, which is deployed once per physical shard
(`shard-a`/`shard-b`). See `deploy/docker-compose.yml` for the full topology
and [multi-region.md](multi-region.md) for why regions and shards are
different axes.

## Request flow (send a message)

```
Client
  │ HTTP POST /channels/{id}/messages
  ▼
api-<region>            verifies token, checks membership + rate limit
  │
  ├─ channel home_region == this region? ──No──▶ forward to api-<home_region>
  │                                                (same handler runs there)
  ▼ Yes
resolve virtual shard = hash(channel_id) % 4096   (internal/routing, no I/O)
resolve physical shard from deploy/shards.yaml
  ▼
BEGIN
  INSERT messages (channel_id, sequence, ...)      (internal/messages)
  INSERT outbox_events                              (internal/events)
COMMIT
  ▼
cmd/worker polls outbox_events, publishes to Kafka topic "message.created"
  ▼
every gateway consumes the topic, checks its local connections against
the channel's (Redis-cached) membership, pushes to matching WebSockets
```

Every step above maps to a numbered section of `.claude/INSTRUCTIONS.md`;
the deep dives in this docs tree reference those sections directly.

## Package map

```
cmd/
  api/       HTTP handlers + middleware + cross-region forwarding
  gateway/   WebSocket upgrade + Kafka fanout consumer wiring
  worker/    Outbox publisher entrypoint

internal/
  users/         account identity
  channels/      channel identity + home_region (the one non-computed routing fact)
  membership/    channel_members + user_channels index
  messages/      per-shard message log: send (idempotent) + cursor-paginated read
  events/        outbox event types, transactional write, publisher
  routing/       virtual-shard hashing, physical-shard config, home-region cache
  quota/         tiers, capabilities, Allow(), Redis token-bucket rate limiter
  realtime/      connection hub, Redis registry, membership cache, Kafka fanout, WS protocol
  storage/       postgres/redis/kafka connection wrappers (no business logic)
  platform/      config, logging, metrics, tracing, health, auth (cross-cutting)
```

Domain packages never talk to Postgres/Redis/Kafka directly through anything
but `internal/storage/*` wrappers, and never decide *which* shard or region
to use — that's always `internal/routing`'s job. See
[sharding-and-routing.md](sharding-and-routing.md).

## What's deliberately not built

Reactions, threads, blocking, typing indicators, presence, read receipts,
search, edit, delete, complex permissions, E2EE, attachments, push
notifications — per INSTRUCTIONS.md §1. The architecture is shaped so each of
these is an additive Kafka event + a new package, not a redesign; see
[kafka-and-events.md](kafka-and-events.md) and the `new-event` skill.
