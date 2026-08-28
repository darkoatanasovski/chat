# ADR 0003: Transactional Outbox for Message Events

## Status

Accepted.

## Context

A message must be durably persisted before any realtime delivery is
attempted (INSTRUCTIONS.md §18) — but delivery is driven by a Kafka event
(`message.created`), and "insert the message, then publish to Kafka" as two
separate operations has an unavoidable failure window:

```
INSERT message into Postgres
        ↓
process crashes
        ↓
Kafka event never published → message exists but is never delivered live
```

## Decision

Insert the message row and an `outbox_events` row in the **same Postgres
transaction**. A separate process (`cmd/worker`, one instance per physical
shard) polls `outbox_events`, publishes each row to Kafka, and deletes it
only after a successful publish.

## Alternatives considered

**Dual-write from the request handler** (insert message, then publish to
Kafka, both from `cmd/api`). Simplest to write, but reintroduces exactly the
failure window above, and also couples message-send request latency to
Kafka availability/latency — a slow or unavailable Kafka broker would make
every message send slow or fail, even though the message itself could have
been safely durable already.

**Change Data Capture (CDC) off the Postgres WAL** (e.g. Debezium). Removes
the need for an application-level outbox table and polling worker entirely
— genuinely a stronger pattern at larger scale. Rejected for V1 as
infrastructure not justified yet (INSTRUCTIONS.md §46): it requires
logical replication setup, a CDC connector, and its own operational
surface, for a benefit (no polling latency, no outbox table) that doesn't
matter at V1's scale. The outbox table's schema (`event_id`, `event_type`,
`channel_id`, `payload`, `created_at`) is intentionally CDC-connector-shaped
if this migration is ever needed later.

## Consequences

- At-least-once delivery to Kafka is guaranteed (a crash between publish and
  delete redelivers on next poll) — every consumer must be idempotent
  (INSTRUCTIONS.md §16/§28). See `internal/realtime.Dedup`.
- Message-send latency includes one extra Postgres write (the outbox row)
  but zero dependency on Kafka being reachable at request time — a slow or
  down Kafka broker delays realtime delivery, not message persistence.
- `cmd/worker` is a new deployable unit, one per physical shard, with its
  own poll-interval/batch-size tuning (`internal/events/publisher.go`,
  currently 250ms / 100 rows) — a small operational cost for removing the
  crash-window entirely.
- The outbox table lives on each shard database (not the control-plane
  database) — see `docs/platform/kafka-and-events.md` for why control-plane
  events (membership changes) don't currently use this mechanism at all.

## Where this lives in code

`migrations/shard/0001_init.sql` (`outbox_events`),
`internal/events/events.go` (`InsertOutbox`),
`internal/events/publisher.go` (`Publisher.PollOnce`/`Run`),
`internal/messages/messages.go` (`Repo.Send`, the call site — outbox insert
happens inside the same transaction as the message insert),
`cmd/worker/main.go`.
