# Load Testing

Implements INSTRUCTIONS.md §38–§39: connection capacity and message
throughput are modeled and measured *independently* — "1M connected users"
is not the same claim as "1M messages/sec." `tools/loadtest` is the tool;
the `loadtest` skill covers day-to-day usage and how to read its output in
depth. This page covers what it measures and why it's built the way it is.

## What it measures independently

- **Connection capacity**: how many concurrent WebSocket connections a
  gateway instance can hold and deliver to.
- **Message throughput**: messages/sec the `api`→Postgres→outbox→Kafka
  pipeline sustains, independent of how many connections are listening.
- **Delivery latency**: persist-to-push time, end to end through the real
  outbox/Kafka/fanout pipeline (not simulated).
- **Reconnection storms**: what happens when many connections drop and
  re-establish simultaneously (`--reconnect-storm`).

## Why connections are decoupled from channel membership

`max_channel_members` is a real, enforced quota (see
[../platform/quotas-and-tiers.md](../platform/quotas-and-tiers.md)) — you
cannot add thousands of *members* to a FREE-tier channel, on purpose. But a
single member can hold many concurrent connections (multiple devices/tabs,
INSTRUCTIONS.md §21), and that's exactly how `tools/loadtest` scales
connection count: `--members` stays within the tier's real quota, while
`--connections-per-member` opens that many real sockets per member. This
means the tool is exercising the *actual* quota system under load, not
bypassing it to get a bigger number — a load test that had to disable
quotas to run wouldn't be testing the system that ships.

## Interpreting rate-limit rejections

At default settings, the sender will hit `messages_per_minute` (429s) partway
through a run — this is expected and is the tool proving the rate limiter
holds under concurrent load, not a failure. Lower `--rate` below the tier's
per-minute allowance if you specifically want to measure sustained accepted
throughput instead.

## What this tool does not attempt

It does not simulate millions of connections from a single machine (OS file
descriptor / ephemeral port limits make that impractical from one process
regardless of server capacity) — for that scale of test, run multiple
instances of `tools/loadtest` from multiple machines against the same
stack and aggregate the reports, or adapt it into a distributed load-testing
harness (k6, Locust, Gatling) using the same request/message shapes
documented in [../api/rest-api.md](../api/rest-api.md) and
[../api/websocket-protocol.md](../api/websocket-protocol.md).

## Capacity planning

Track the metrics in
[../platform/observability.md](../platform/observability.md) — connections/
gateway, messages/sec, Kafka producer latency, Postgres query latency, Redis
op latency — against load-test runs at increasing scale to find where each
component's latency starts degrading, per INSTRUCTIONS.md §39. Scaling
decisions should follow these measured knees, not registered-user counts.
