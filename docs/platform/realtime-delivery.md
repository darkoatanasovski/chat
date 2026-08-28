# Realtime Delivery

Implements INSTRUCTIONS.md §17–§20 and §29–§31. The core rule: **WebSocket
delivery is never the durable record of a message** — it's a best-effort
push on top of a durable store the client can always fall back to.

```
persist → commit → publish outbox event → fan out over WebSocket (best-effort)
```

If the push fails or the client is offline, the message still exists; the
client recovers by calling `GET /channels/{id}/messages` on reconnect.

## Connection lifecycle (`internal/realtime`, `cmd/gateway`)

`GET/WS /connect?token=...` (browsers can't set custom headers on the
WebSocket handshake, hence the token as a query param, not a header):

1. `auth.Signer.Verify` checks the token — client-asserted identity is never
   trusted (INSTRUCTIONS.md §43).
2. `Hub.Register` creates a `Connection` with a **bounded** outbound channel
   (`outboundBufferSize = 256`).
3. `Registry.Register` writes a TTL'd entry to Redis (`conn:<user>:<connID>`)
   — cross-process visibility for observability / a future presence feature;
   the local `Hub` is what actually drives delivery, so this isn't on the
   fanout hot path.
4. Two goroutines: `writePump` drains the outbound channel to the socket and
   sends periodic pings; `readPump` only watches for close/pong (clients
   never send business messages over the socket — sending is a REST call).

## Fanout (`internal/realtime/fanout.go`)

Every gateway instance consumes the **entire** `message.created` Kafka topic
— not filtered by region, because a channel's members can be anywhere:

```go
for {
    msg := reader.ReadMessage(ctx)
    if dedup.SeenBefore(eventID) { continue }              // §16: at-least-once, so dedup
    members := membershipCache.Members(channelID)           // Redis, fallback to Postgres on miss
    if !hub.HasAnyLocalUser(members) { continue }            // skip work for channels with no local connections
    for _, userID := range members {
        hub.DeliverToUser(userID, frame)
    }
}
```

The membership cache (`membership:channel:<id>:members`, a Redis set) is
**write-through**: `cmd/api` updates it synchronously in the same request
that writes `channel_members` to Postgres (`AddMember`, `CreateChannel`).
Postgres remains the source of truth; a cache miss falls back to
`membership.Repo.ListMembers` and repopulates the cache. This is why it's
safe for correctness even though it's "just a cache" — INSTRUCTIONS.md §21
requires Redis never be authoritative for anything, and this isn't: worst
case on a miss is one extra Postgres query, not a stale wrong answer.

## Backpressure (§29)

```go
func (c *Connection) Enqueue(payload []byte) bool {
    select {
    case c.send <- payload:
        return true
    default:
        return false // buffer full — caller disconnects this connection
    }
}
```

A connection that can't keep up is disconnected, never allowed to grow an
unbounded queue. The client reconnects and recovers via history — the same
mechanism as any other disconnect.

## Reconnection and gap recovery (§20)

The gateway and Hub hold no session state across a disconnect. A
reconnecting client re-authenticates from scratch and is expected to know
its own last-seen sequence per channel (or, as the demo app does, just
re-fetch recent history and merge/dedupe by `message_id`). V1's REST surface
only exposes backward (`before=`) cursor pagination
(INSTRUCTIONS.md §36) — recovering a short gap by re-fetching the most
recent N messages and merging is sufficient for V1's scale and matches what
the demo app (`demo/app/channels/[id]/page.tsx`, `reconnect()`) does. A
production client recovering from a long outage would want a forward
(`after=`) cursor instead of relying on "N is enough" — that's a natural,
additive extension of the same `sequence`-based cursor scheme, not a
redesign.

## Why fan-out-on-write, and its limit

Every gateway sees every message and filters locally — this is
fan-out-on-write at the *delivery* layer only (message *storage* is
unaffected either way). It's appropriate for V1's conservative
`max_channel_members` limits (INSTRUCTIONS.md §31). It does not scale to a
channel with hundreds of thousands of members without either partitioning
the Kafka consumers by channel or introducing dedicated fanout
infrastructure (§30) — deliberately not built yet, since V1 enforces
membership caps that make it unnecessary. When that limit is lifted, this
file (`internal/realtime/fanout.go`) is where fan-out-on-read or
hierarchical fanout would be introduced, without touching message storage or
the outbox at all.
