# Data Model

Two physically separate database groups, matching the two access patterns
INSTRUCTIONS.md §3.3 calls out — `user_id → channels` and
`channel_id → messages` — deliberately *not* one normalized database trying
to answer both efficiently.

## Control-plane database (one instance)

`migrations/control/0001_init.sql`. Low-write, highly-cacheable metadata.

| Table | Purpose | Key |
|---|---|---|
| `users` | account identity, tier | `user_id` |
| `channels` | channel identity, `home_region`, `virtual_shard` | `channel_id` |
| `channel_members` | who's in a channel | `(channel_id, user_id)` |
| `user_channels` | **the** index behind `GET /users/me/channels` | `(user_id, channel_id)` |

`channels.home_region` is the one piece of routing metadata that isn't
purely computed — see [sharding-and-routing.md](sharding-and-routing.md).
`virtual_shard` is computed but stored alongside it at creation time so reads
never need to recompute a hash either.

`user_channels` carries a denormalized `last_message_sequence` /
`last_message_at`, updated best-effort by whichever `api` instance completes
a send (`internal/channels.Repo.UpdateLastMessage`). This is intentionally
*not* part of the message-send transaction — the message is durable the
moment its own shard-local transaction commits; a delayed or failed index
update only means a channel's position in `GET /users/me/channels` briefly
lags, never that a message is lost.

## Shard database (N instances, `shard-a`/`shard-b` in this deployment)

`migrations/shard/0001_init.sql`, applied identically to every shard. High-
write, append-only.

| Table | Purpose | Key |
|---|---|---|
| `messages` | the durable message log | `(channel_id, sequence)` |
| `channel_sequences` | per-channel monotonic sequence counter | `channel_id` |
| `outbox_events` | transactional outbox (INSTRUCTIONS.md §16) | `event_id` |

```sql
CREATE TABLE messages (
    channel_id UUID NOT NULL,
    sequence BIGINT NOT NULL,
    message_id UUID NOT NULL,
    sender_id UUID NOT NULL,
    client_message_id UUID NOT NULL,
    body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, sequence)
);
CREATE UNIQUE INDEX idx_messages_client_message_id ON messages (channel_id, client_message_id);
```

Only two indexes on `messages`: the primary key (serves both single-message
lookup and cursor pagination — `WHERE channel_id = $1 AND sequence < $2
ORDER BY sequence DESC`) and the idempotency unique index. No indexes are
added speculatively (INSTRUCTIONS.md §9).

`channel_sequences` exists so sequence assignment locks exactly one row (`SELECT
... FOR UPDATE` scoped to one `channel_id`) instead of anything table-wide —
see `internal/messages/messages.go`'s `Send`.

## IDs

Every ID (`user_id`, `channel_id`, `message_id`, `client_message_id`,
`event_id`) is a UUIDv7 (`uuid.NewV7()`), globally unique without a central
sequence and time-sortable. No table uses a Postgres auto-increment primary
key for anything that has to be globally unique (INSTRUCTIONS.md §33) — the
one exception is `channel_sequences.last_sequence` / `messages.sequence`,
which is *deliberately* a plain per-channel counter, not a global ID: it's
the ordering mechanism (§10), not an identifier.

## Why two databases instead of one

A single normalized Postgres instance would force either cross-shard queries
(to join a user's channels against per-channel message tables) or an
increasingly wide "everything" schema that can't be sharded cleanly by
either key. Splitting along the two access patterns means `GET
/users/me/channels` and `GET /channels/{id}/messages` each touch exactly one
logical store, and the message store can scale horizontally (more physical
shards) independently of the control-plane store's growth.
