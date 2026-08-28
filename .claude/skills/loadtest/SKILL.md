---
name: loadtest
description: Run tools/loadtest against the running local stack to measure WebSocket connection capacity, message throughput, and delivery latency, optionally including a reconnection storm. Use when the user wants to load-test, benchmark, or stress-test the chat platform.
---

# loadtest

`tools/loadtest` is a Go CLI that connects real WebSocket clients to a
running gateway and sends real messages through a running api instance,
matching INSTRUCTIONS.md §38: connection count and message rate are modeled
*independently*, and channel membership stays within tier quotas while
connection count is scaled the way a real client would — multiple
sockets/devices per member (§21) — not one connection per member.

## Prerequisites

The stack must already be up (`platform-up` skill) and migrated. Run from
the repo root.

## Basic run

```bash
go run ./tools/loadtest \
  --api-url http://localhost:8081 \
  --ws-url  ws://localhost:8091 \
  --region eu \
  --members 3 \
  --connections-per-member 20 \
  --rate 2 \
  --duration 20s
```

This creates 1 sender + 2 additional members (3 total — at the FREE-tier
`max_channel_members` limit), opens 20 WebSocket connections per member (60
total), and sends messages at 2/sec from the sender for 20 seconds.

## Reading the report

```
messages sent:          40
messages accepted:      20
messages rate-limited:  20
delivery frames seen:   1180
delivery latency p50:   3ms
delivery latency p90:   9ms
delivery latency p99:   22ms
delivery latency max:   40ms
```

- **accepted vs rate-limited**: FREE tier allows 20 messages/minute. At
  `--rate 2` for 20s (40 attempts), expect roughly half rejected with 429 —
  this is the quota system working, not a bug. To test raw throughput instead
  of quota behavior, lower `--rate` to `0.3` (just under 20/min) so nothing
  gets rejected, and raise `--duration` accordingly.
- **delivery frames seen**: should be roughly `accepted × connections`
  (every accepted message fans out to every connected socket of every
  member, including the sender's own connections).
- **delivery latency**: measured as `time-of-arrival − server-persisted
  created_at`, i.e. purely the persist→publish→consume→push pipeline, not
  HTTP request time.

## Reconnection storm scenario

```bash
go run ./tools/loadtest --members 3 --connections-per-member 50 \
  --duration 30s --reconnect-storm
```

At the midpoint, every open connection is dropped and immediately
re-established concurrently — this exercises gateway connection churn and
Redis registry cleanup/re-registration under load (INSTRUCTIONS.md §38
"reconnection storms").

## Testing a different region / cross-region load

Point `--api-url`/`--ws-url` at `us` (`:8082`/`:8092`) or `asia`
(`:8083`/`:8093`) instead of `eu` to load-test from a different simulated
region — since the channel's home region is wherever it's created, this also
exercises cross-region write forwarding under load if you create the channel
in one region and point connections at another (requires a small script
change; the CLI itself always keeps sender and members in one `--region` for
simplicity).

## Known limitation

There's no tier-upgrade path in V1, so `--members` is capped by whatever
`deploy/tiers.yaml` sets for `FREE.max_channel_members` (default 3). This is
intentional — it's the same quota enforcement real traffic goes through, not
a shortcut. Raise it in `deploy/tiers.yaml` locally if you need more real
members for a specific test.
