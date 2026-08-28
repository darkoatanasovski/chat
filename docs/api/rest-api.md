# REST API

Base path is whichever region's `api` instance you're talking to
(`http://localhost:8081` eu, `:8082` us, `:8083` asia in local dev — see
`docs/operations/docker-compose.md`). All endpoints except `POST /users`
require `Authorization: Bearer <token>`.

## `POST /users`

Creates an account and issues a token. No password (see
[security.md](../platform/security.md)).

```
POST /users
{ "display_name": "Alice", "region": "eu" }

201
{ "user_id": "...", "display_name": "Alice", "region": "eu", "tier": "FREE", "token": "..." }
```

`region` must be one of `eu`, `us`, `asia`. New accounts always start on the
`FREE` tier.

## `POST /channels`

Creates a channel. Its `home_region` is always the region of the `api`
instance that handles this request (INSTRUCTIONS.md §5) — the caller doesn't
choose it independently. The creator is automatically added as the first
member. Enforces `max_channels` (resource quota).

```
POST /channels
Authorization: Bearer <token>
{ "name": "general" }

201
{ "channel_id": "...", "name": "general", "home_region": "eu", "virtual_shard": 2615 }

429  (quota exceeded)
{ "error": "channel.create limit reached (1)" }
```

## `POST /channels/{id}/members`

Adds a member. Caller must already be a member. Enforces
`max_channel_members`. Forwards to the channel's home region if this
instance isn't it (transparent to the caller — same request/response shape
either way, just added latency).

```
POST /channels/{id}/members
Authorization: Bearer <token>
{ "user_id": "..." }

201
{ "channel_id": "...", "user_id": "..." }

403  (not a member)          |  429 (quota exceeded)
```

## `POST /channels/{id}/messages`

Sends a message. Idempotent on `client_message_id`
(INSTRUCTIONS.md §19) — the client generates this UUID; retrying the exact
same `(channel_id, client_message_id)` returns the original message instead
of creating a duplicate, safe to retry after a timeout/dropped response.
Enforces `messages_per_minute`. Forwards to home region if needed.

```
POST /channels/{id}/messages
Authorization: Bearer <token>
{ "client_message_id": "<uuid>", "body": "hello" }

201
{
  "message_id": "...", "channel_id": "...", "sequence": 1,
  "sender_id": "...", "client_message_id": "...",
  "body": "hello", "created_at": "2026-01-01T00:00:00.000Z"
}

403  (not a member)  |  429 (rate limited)
```

Body is capped at 4000 characters. A retried send with the same
`client_message_id` returns `201` with the identical `message_id`/`sequence`
as the original — check those fields, not the status code, to detect a
duplicate-vs-original response.

## `GET /channels/{id}/messages`

Cursor-paginated history, newest first. **Never uses `OFFSET`**
(INSTRUCTIONS.md §11) — pass the oldest `sequence` you've already seen as
`before` to page backward.

```
GET /channels/{id}/messages?before=918273&limit=50
Authorization: Bearer <token>

200
[ { "message_id": "...", "sequence": 918272, ... }, ... ]   // newest of the older batch first
```

`limit` defaults to 50, capped at 100. Omit `before` (or pass `0`) to get
the most recent `limit` messages. Caller must be a channel member.

## `GET /users/me/channels`

One control-plane query, never a scatter/gather across message shards
(INSTRUCTIONS.md §13).

```
GET /users/me/channels
Authorization: Bearer <token>

200
[
  {
    "channel_id": "...", "name": "general", "home_region": "eu",
    "last_message_sequence": 42, "last_message_at": "2026-01-01T00:00:00.000Z"
  }
]
```

Sorted by `last_message_at` descending (channels with no messages yet sort
last, by `joined_at`). `last_message_sequence`/`last_message_at` are
best-effort denormalized fields — see
[data-model.md](../platform/data-model.md) for why a brief lag here never
implies a lost message.

## Errors

All non-2xx responses are `{ "error": "<message>" }`. Common statuses:
`400` malformed input, `401` missing/invalid/expired token, `403` not a
channel member, `404` channel not found, `429` rate-limited or quota
exceeded, `502` home-region unreachable (forwarding failure), `500`
unexpected server error.

## WebSocket

See [websocket-protocol.md](websocket-protocol.md) for `GET/WS /connect`.
