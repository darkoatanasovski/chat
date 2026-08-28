---
name: new-event
description: Scaffold a new Kafka domain event end-to-end (outbox event type, transactional write, idempotent consumer) following this platform's transactional-outbox pattern. Use when adding a future feature from INSTRUCTIONS.md §44 (reactions, edits, deletes, read receipts, etc.) that needs to publish a durable event.
---

# new-event

Every durable event in this system goes through the transactional outbox
(INSTRUCTIONS.md §16) — **never** publish to Kafka directly from request
handling code (§49 rule 8). `internal/events` currently only defines
`message.created`; this skill scaffolds the same shape for a new event type.

## Steps

1. **Define the event** in `internal/events/events.go`: add a
   `TopicXxx` constant and an `XxxPayload` struct (JSON-tagged, small — it's
   what gets published to Kafka and read back by every consumer).

   ```go
   const TopicReactionAdded = "reaction.added"

   type ReactionAddedPayload struct {
       ReactionID uuid.UUID `json:"reaction_id"`
       ChannelID  uuid.UUID `json:"channel_id"`
       MessageID  uuid.UUID `json:"message_id"`
       UserID     uuid.UUID `json:"user_id"`
       Emoji      string    `json:"emoji"`
       CreatedAt  time.Time `json:"created_at"`
   }
   ```

2. **Write it transactionally** at the point of origin. Find the domain
   repo's write method (e.g. a new `internal/reactions.Repo.Add`) and call
   `events.InsertOutbox` inside the *same* `pgx.Tx` as the row insert — copy
   the pattern in `internal/messages/messages.go`'s `Send` method:

   ```go
   tx, _ := pool.Begin(ctx)
   defer tx.Rollback(ctx)
   // ... INSERT the reaction row ...
   events.InsertOutbox(ctx, tx, events.TopicReactionAdded, channelID, payload)
   tx.Commit(ctx)
   ```

   If the event originates on a shard DB (anything keyed by channel_id, like
   reactions/edits on messages), this "just works" — `cmd/worker` already
   polls every shard's `outbox_events` table generically by type-agnostic
   `event_type`/`payload` columns; no worker changes needed.

   If the event originates on the **control-plane** DB (e.g. a future
   `user.blocked`), the control-plane DB doesn't currently have an
   `outbox_events` table — add one via the `new-migration` skill first,
   mirroring `migrations/shard/0001_init.sql`'s table, and add a
   `worker control` mode/service in `cmd/worker` + `deploy/docker-compose.yml`
   analogous to `worker-outbox-a`/`-b`.

3. **Consume it idempotently.** Kafka delivery is at-least-once
   (INSTRUCTIONS.md §16/§28) — every consumer must dedup. Use
   `internal/realtime.Dedup.SeenBefore(ctx, eventID)` as shown in
   `internal/realtime/fanout.go`, or the same pattern if the new consumer
   lives elsewhere. Build the Kafka reader with
   `internal/storage/kafka.NewConsumer(brokers, topic, groupID)`.

4. **Document it** in `docs/platform/kafka-and-events.md` (add to the event
   table) and, if it changes the API surface, in `docs/api/rest-api.md` or
   `docs/api/websocket-protocol.md`.

## What NOT to do

- Don't publish outside a transaction "just this once" — that reintroduces
  the exact failure mode the outbox exists to prevent (message committed,
  process crashes, event never published).
- Don't skip the dedup check in the new consumer, even if it "shouldn't"
  redeliver — assume it will (§28).
- Don't add a new Kafka topic per micro-event if it's just a different
  `event_type` value on an existing outbox table you already poll — only add
  a new outbox table if the event originates on a database that doesn't have
  one yet.
