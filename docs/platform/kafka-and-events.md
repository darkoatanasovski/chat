# Kafka and Events

Implements INSTRUCTIONS.md §14–§16. Kafka is the durable *event
distribution* layer; Postgres remains the durable *application state*. V1
only has one event type in flight — `message.created` — because it's the
only one anything in scope actually needs. Everything below is written so
adding the next one (reactions, edits, ...) follows the same shape; see the
`new-event` skill for the mechanical steps.

## Current events

| Topic | Producer | Consumer(s) | Payload |
|---|---|---|---|
| `message.created` | `cmd/worker` (outbox publisher, one per shard) | every `cmd/gateway` instance (fanout) | `internal/events.MessageCreatedPayload` |

## Why control-plane changes (channel created, member added) don't publish to Kafka in V1

INSTRUCTIONS.md §49 rule 8 is explicit: **never publish a Kafka event
outside the reliable outbox mechanism.** The control-plane database
(`users`, `channels`, `channel_members`) has no `outbox_events` table in
V1 — only the shard databases do, alongside the `messages` table they exist
to protect. Membership changes are propagated to the one place that
currently needs to know about them (the gateway's Redis membership cache)
synchronously, in the same request, as a write-through cache update — not
via Kafka. This is correctness-neutral (Postgres is still the source of
truth; see [realtime-delivery.md](realtime-delivery.md)) and avoids standing
up an outbox+worker for control-plane tables before anything actually
depends on it.

If a future feature needs `channel.member_added` as a durable event (e.g. an
audit log, or a notification-fanout service), add a control-plane
`outbox_events` table via the `new-migration` skill and a corresponding
`cmd/worker` mode — the mechanism already exists for shard DBs and is
designed to be copied, not reinvented.

## Partitioning

Every channel-related Kafka message is keyed by `channel_id`
(`internal/storage/kafka.NewProducer` uses `&kafka.Hash{}` as the balancer,
and the outbox publisher passes `r.ChannelID.String()` as the key). This
guarantees per-channel ordering within a partition — Kafka partitions and
Postgres virtual shards are independent concepts that happen to use the same
key; INSTRUCTIONS.md §15 is explicit that they must not be confused.

## The outbox, concretely

```sql
-- migrations/shard/0001_init.sql
CREATE TABLE outbox_events (
    event_id UUID PRIMARY KEY,
    event_type TEXT NOT NULL,
    channel_id UUID NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`internal/messages.Repo.Send` inserts a `messages` row and an
`outbox_events` row in one transaction (`internal/events.InsertOutbox`).
`cmd/worker` (`internal/events.Publisher.PollOnce`) polls
`outbox_events ORDER BY created_at`, publishes each to Kafka using
`event_type` as the topic, and deletes the row **only after** a successful
publish:

```go
for _, r := range pending {
    kafka.Publish(ctx, writer, r.EventType, r.ChannelID.String(), r.Payload)
    pool.Exec(ctx, `DELETE FROM outbox_events WHERE event_id = $1`, r.EventID)
}
```

A crash between publish and delete redelivers the event on the next poll —
this is why every consumer must be idempotent (§16, §28), not an oversight.

## Idempotent consumption

`internal/realtime.Dedup` wraps a Redis `SETNX` with a 10-minute TTL, keyed
by `<channel_id>:<sequence>` (deterministic, cheaper than trusting a
possibly-redelivered `event_id`). Every consumer of every topic should use
the same pattern — see `internal/realtime/fanout.go`'s `handle` method for
the reference implementation.

## Local infrastructure note

This deployment runs a single-node Kafka broker in KRaft mode (no
Zookeeper) — see `docs/adr/0005-kraft-kafka-and-valkey.md`.
