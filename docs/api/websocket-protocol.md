# WebSocket Protocol

`GET/WS /connect?token=<token>` on any `gateway` instance
(`ws://localhost:8091` eu, `:8092` us, `:8093` asia in local dev). Connect to
whichever region's gateway is nearest the client — see
[multi-region.md](../platform/multi-region.md); realtime delivery works
regardless of which region a connection is on relative to a channel's home
region.

The token is the same bearer token issued by `POST /users` — passed as a
query parameter because browsers can't set arbitrary headers during the
WebSocket handshake. The server never trusts anything else from the client;
`user_id`/`region`/`tier` all come from verifying this token.

## Protocol shape

This is a **push-only, server-to-client** protocol for delivery. There is no
client-to-server business message — sending a message is always
`POST /channels/{id}/messages` over REST (INSTRUCTIONS.md §36 keeps these
separate on purpose: the durable write path and the realtime push path are
different concerns). The only client→server WebSocket traffic is the
protocol-level pong response to the server's pings, handled transparently by
any standard WebSocket client library.

## Server → client frames

One JSON frame type in V1:

```json
{
  "type": "message.created",
  "channel_id": "...",
  "message_id": "...",
  "sequence": 42,
  "sender_id": "...",
  "body": "hello",
  "created_at": "2026-01-01T00:00:00.000Z"
}
```

Sent once per message, to every currently-connected socket belonging to a
member of that channel — including the sender's own other connections (a
sender is a channel member too, so if they have multiple tabs/devices open,
all of them receive it, per INSTRUCTIONS.md §21's multi-device model).

A connection only ever receives frames for channels it's a member of — the
gateway filters using the channel's membership before delivering to any
given user's connections (see
[realtime-delivery.md](../platform/realtime-delivery.md)).

## Delivery guarantees (read this before assuming anything)

- **At-most-once per connection, per attempt.** A message send is durably
  persisted regardless of WebSocket state (INSTRUCTIONS.md §18), but the
  *push* to a given open connection is not retried or acknowledged.
- **No delivery while disconnected.** There is no queued/offline delivery —
  if a connection isn't open when a message is fanned out, that connection
  simply doesn't get a frame for it.
- **No ordering guarantee across delivery**, only within — `sequence` is
  assigned in strict per-channel order at persist time and is what frames
  actually carry; use it to detect ordering issues or gaps in what a given
  connection has received, not the order frames arrive on the wire during
  reconnects/bursts.

## Recovering from a gap

Every disconnect (intentional or not) should be followed, on reconnect, by
reconciling with the durable history: call
`GET /channels/{id}/messages` (optionally with `before=<oldest known
sequence>` to page further back) and merge by `message_id`, sorting by
`sequence`. See `demo/app/channels/[id]/page.tsx`'s `reconnect()` for a
reference client implementation, and
[realtime-delivery.md](../platform/realtime-delivery.md#reconnection-and-gap-recovery-20)
for why V1's REST surface makes "re-fetch recent history" the right recovery
strategy rather than a forward cursor.

## Connection limits

Each connection has a bounded 256-frame outbound buffer server-side; a
connection that can't keep up gets forcibly closed by the server rather than
allowed to accumulate unbounded memory (INSTRUCTIONS.md §29). If your client
gets disconnected under load, that's this behavior — reconnect and recover
via history rather than treating it as an error to retry the same socket.

## Heartbeat

The server pings every 30s and expects a pong within 60s
(`internal/realtime/websocket.go`); standard WebSocket clients handle this
automatically. There's no custom application-level heartbeat frame.
