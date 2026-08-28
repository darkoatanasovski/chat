# High-Scale Chat Platform — Engineering Instructions

## 1. Goal

Build a globally distributed, highly scalable chat backend designed to eventually support **millions of concurrent users**.

The initial product should intentionally have very few features.

### V1 features

Implement only:

* Users
* Channels
* Channel membership
* Send messages to channels
* Retrieve channel messages
* List a user's channels
* Realtime message delivery
* Tier-based limits and quotas

Do **not** initially implement:

* Reactions
* Threads
* Blocking
* Typing indicators
* Presence
* Read receipts
* Search
* Editing
* Deleting
* Complex permissions
* E2EE
* Attachments
* Push notifications

The architecture, however, should make adding these later straightforward.

The priority is:

> scalability, correctness, simplicity, observability, and clear architectural boundaries over feature count.

---

# 2. Technology Stack

Primary backend language:

```text
Go
```

Core infrastructure:

```text
Go
PostgreSQL
Kafka
Redis / Valkey
WebSockets
Docker
```

Object storage can be added later for attachments.

Do not introduce Kubernetes initially.

Services should be containerized and designed so they can eventually run under Kubernetes or another orchestrator without architectural changes.

---

# 3. Architectural Principles

The system should be designed around several principles.

### 3.1 Stateless compute

API and gateway servers should be stateless wherever possible.

```text
Client
   │
   ▼
Load Balancer
   │
   ├── Gateway
   ├── Gateway
   ├── Gateway
   └── Gateway
```

Any gateway should be replaceable without losing durable state.

---

### 3.2 Horizontally scalable

Avoid architectures requiring a single machine to continuously become larger.

Prefer:

```text
more users
    ↓
more gateways
more API servers
more Kafka partitions
more DB shards
```

---

### 3.3 Design around access patterns

Do not attempt to create one normalized relational database capable of answering every query.

The two primary access patterns are:

```text
user_id
   ↓
user's channels
```

and:

```text
channel_id
   ↓
channel messages
```

These should have independent storage/indexing strategies where appropriate.

Controlled denormalization is expected.

---

# 4. Multi-Region Architecture

The architecture must support multiple geographic regions.

Initial logical regions:

```text
EU
US
ASIA
```

Example physical regions:

```text
EU     → Frankfurt
US     → Virginia
ASIA   → Singapore
```

Users should connect to the geographically nearest gateway.

```text
                   Global Routing
                         │
           ┌─────────────┼─────────────┐
           ▼             ▼             ▼

          EU             US           ASIA
       Gateways       Gateways       Gateways
```

Do not require users participating in the same channel to be located in the same region.

---

# 5. Channel Home Region

Every channel must have exactly **one authoritative home region**.

Example:

```text
channel_id: ch_123
home_region: EU
```

All authoritative writes for that channel happen in EU regardless of where the sender is located.

Example:

```text
User US
   │
   ▼
US Gateway
   │
   │ internal cross-region request
   ▼
EU Chat Service
   │
   ▼
EU Message Shard
```

This prevents global distributed-write consistency problems.

Do not implement active-active writes for the same channel.

---

# 6. Channel Routing

The system must be able to determine:

```text
channel_id
     ↓
home region
     ↓
virtual shard
     ↓
physical database
```

Conceptually:

```text
Route(channelID)
    -> region
    -> virtualShard
    -> physicalShard
```

Routing information should be heavily cached.

Do not require a central database lookup for every message.

---

# 7. Database Sharding

Messages must be sharded primarily by:

```text
channel_id
```

All messages for a normal channel should reside on the same logical shard.

Do NOT shard using:

```text
every 10,000 messages
```

or arbitrary message ranges from day one.

We want:

```text
channel A → shard 17
channel B → shard 42
channel C → shard 17
```

rather than:

```text
channel A messages 1-10k     → shard 1
channel A messages 10k-20k   → shard 2
```

The primary message query should normally touch exactly one shard.

---

# 8. Virtual Shards

Do not map channels directly to physical PostgreSQL servers.

Introduce virtual shards from the beginning.

For example:

```text
4096 virtual shards
```

Conceptually:

```text
hash(channel_id)
       %
      4096
       │
       ▼
virtual shard
       │
       ▼
physical PostgreSQL shard
```

Example:

```text
VS 0-511      → PostgreSQL 01
VS 512-1023   → PostgreSQL 02
VS 1024-1535  → PostgreSQL 03
...
```

This allows future rebalancing.

Adding a PostgreSQL server should require moving virtual shards rather than changing the channel hashing algorithm.

The virtual-shard count should therefore be much larger than the initial physical-shard count.

---

# 9. Message Storage

Conceptual schema:

```sql
messages (
    channel_id UUID NOT NULL,
    sequence BIGINT NOT NULL,
    message_id UUID NOT NULL,
    sender_id UUID NOT NULL,
    body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (channel_id, sequence)
)
```

Additional indexes should be introduced only when justified by real query patterns.

Avoid unnecessary indexes on high-write message tables.

---

# 10. Message Ordering

Every channel must have monotonically increasing sequence numbers.

Example:

```text
channel ch_123

1001 message
1002 message
1003 message
1004 message
```

The channel's authoritative region determines ordering.

Sequence numbers are used for:

* message ordering
* pagination
* synchronization
* reconnect recovery
* future read receipts
* future edits/reactions/events

Do not depend exclusively on timestamps for ordering.

---

# 11. Pagination

Never use large OFFSET pagination for messages.

Bad:

```sql
OFFSET 1000000 LIMIT 50
```

Use cursor/sequence pagination.

Example:

```text
GET /channels/ch_123/messages?before=918273&limit=50
```

Conceptually:

```sql
SELECT *
FROM messages
WHERE channel_id = $1
  AND sequence < $2
ORDER BY sequence DESC
LIMIT $3;
```

The relevant index/storage layout must make this efficient.

---

# 12. Large Channel Buckets

Do not prematurely split normal channels.

However, the storage abstraction must allow extremely large channels to eventually use buckets.

Future example:

```text
channel ch_123

bucket 0001
bucket 0002
bucket 0003
...
```

Possible strategies include:

```text
sequence ranges
```

or:

```text
time ranges
```

Example:

```text
ch_123 / 0-10M
ch_123 / 10M-20M
ch_123 / 20M-30M
```

V1 does not need automatic bucket splitting unless necessary.

The code must simply avoid assumptions that one channel can never be bucketed.

---

# 13. User Channel Index

Do not discover a user's channels by querying all channel/message shards.

Maintain a separate user-centric index.

Conceptually:

```text
user_channels (
    user_id,
    channel_id,
    last_message_sequence,
    last_message_at,
    ...
)
```

This storage should be partitionable/shardable by:

```text
user_id
```

Therefore:

```text
GET /users/me/channels
```

should normally hit one user/inbox shard, not all message shards.

---

# 14. Kafka

Kafka is the durable event backbone.

Use Kafka for events such as:

```text
channel.created
channel.member_added
channel.member_removed

message.created

user.created

tier.changed
```

Future events may include:

```text
message.edited
message.deleted

reaction.added
reaction.removed

user.blocked

read.updated
```

Kafka should not replace PostgreSQL as the application database.

Think:

```text
PostgreSQL
    =
durable application state

Kafka
    =
durable ordered event distribution
```

---

# 15. Kafka Partitioning

For channel-related events, Kafka partition keys should normally use:

```text
channel_id
```

This ensures events for a particular channel remain ordered within a Kafka partition.

Conceptually:

```text
hash(channel_id)
       │
       ▼
Kafka partition
```

Do not confuse Kafka partitions with PostgreSQL shards.

They are independent concepts.

---

# 16. Transactional Outbox

Avoid this failure mode:

```text
INSERT message into PostgreSQL
        ↓
process crashes
        ↓
Kafka event never published
```

Use the transactional outbox pattern.

Conceptually:

```text
BEGIN

INSERT message

INSERT outbox_event

COMMIT
```

Then:

```text
Outbox Publisher
       │
       ▼
Kafka
```

Only delete/mark the outbox record after successful publication.

Consumers must still be idempotent because duplicate delivery is possible.

---

# 17. Realtime Delivery

Clients maintain persistent WebSocket connections to their nearest gateway.

```text
Client
   │
   │ WebSocket
   ▼
Gateway
```

Gateway responsibilities should remain limited:

* connection lifecycle
* authentication
* heartbeat
* basic protocol validation
* routing
* realtime event delivery
* backpressure
* connection limits

Do not put significant business logic inside gateways.

---

# 18. WebSockets Are Not Durable Storage

Never assume WebSocket delivery is reliable.

The durable message must exist independently of realtime delivery.

Conceptually:

```text
Send Message
     │
     ▼
persist
     │
     ▼
commit
     │
     ▼
publish event
     │
     ▼
realtime delivery
```

If realtime delivery fails, the client recovers through synchronization/history APIs.

---

# 19. Client Message IDs and Idempotency

Clients must generate a unique client message ID.

Example:

```text
client_message_id = UUID
```

Retrying:

```text
SendMessage(
    channel=123,
    client_message_id=ABC
)
```

must not create duplicate messages.

The server should safely recognize duplicate requests and return the previously created message.

This is particularly important when clients lose connectivity after sending but before receiving an acknowledgement.

---

# 20. Reconnection

Clients should maintain synchronization state.

Example:

```text
channel ch_123
last_sequence = 918273
```

After reconnecting:

```text
client:
I have through 918273

server:
918274
918275
918276
```

Realtime delivery can therefore be lossy because durable synchronization repairs gaps.

---

# 21. Redis / Valkey

Redis should be used for fast ephemeral/distributed state such as:

* connection routing
* channel routing cache
* membership cache
* tier/quota cache
* rate limiting
* hot channel metadata
* distributed counters where appropriate

Example connection routing:

```text
user:123
   ↓
EU/gateway-17/connection-abc
```

Users may have multiple connections:

```text
user:123

iPhone → gateway 17
Web    → gateway 42
Mac    → gateway 08
```

Do not treat Redis as the authoritative store for messages.

---

# 22. Tier System

The architecture must support different account/application tiers from day one.

Do not scatter checks such as:

```go
if user.Plan == "free" {
    ...
}
```

throughout the codebase.

Create a centralized quota/capability system.

Example tiers:

```text
FREE
PRO
BUSINESS
ENTERPRISE
```

Example quota definition:

```text
FREE

max_channels        = 1
max_channel_members = 3
messages_per_minute = 20
```

Example:

```text
PRO

max_channels        = 100
max_channel_members = 100
messages_per_minute = 1000
```

Values above are configurable examples, not hardcoded product decisions.

---

# 23. Quota Service

Business operations should use a common interface conceptually similar to:

```go
limits.Allow(
    subject,
    capability,
    context,
)
```

Capabilities might include:

```text
channel.create
channel.member.add
message.send
```

Examples:

```text
Allow(user, "channel.create")
Allow(channel, "channel.member.add")
Allow(user, "message.send")
```

The quota system should determine limits based on tier/configuration.

---

# 24. Rate Limiting

Rate limiting must work across multiple API/gateway instances.

Do not use process-local counters as the authoritative limiter.

Use Redis/Valkey or another distributed mechanism.

Support limits by dimensions such as:

```text
user_id
channel_id
IP
application_id
tier
```

Example:

```text
rate:message:user:123
```

Rate limiting algorithms may include:

```text
token bucket
sliding window
```

Prefer algorithms that do not require excessive Redis operations.

---

# 25. Quota Counters

Differentiate between:

### Rate limits

Example:

```text
20 messages/minute
```

and:

### Resource limits

Example:

```text
1 channel
3 members/channel
```

They require different handling.

Resource limits should ultimately be enforceable against authoritative state.

Redis may accelerate checks but should not be the only source of truth where exceeding the quota would create invalid persistent state.

Race conditions must be considered.

---

# 26. Regional Replication

Each channel has one authoritative writer region.

Other regions may maintain asynchronous replicas/materialized state.

Example:

```text
              EU
        authoritative
             │
             │ Kafka replication
       ┌─────┴─────┐
       ▼           ▼
      US          ASIA
```

Do not introduce synchronous cross-region database consensus for normal message writes.

The goal is low latency and high availability without making every message wait for multiple continents.

---

# 27. Cross-Region Message Flow

Example:

```text
Channel home = EU
Sender = US
Recipient = ASIA
```

Flow:

```text
US Client
   │
   ▼
US Gateway
   │
   ▼
EU Chat Service
   │
   ▼
EU PostgreSQL
   │
   ▼
Kafka
   │
   ├────────→ US
   │
   └────────→ ASIA
                 │
                 ▼
            ASIA Gateway
                 │
                 ▼
             Recipient
```

The authoritative region establishes ordering.

---

# 28. Failure Philosophy

Assume everything can fail.

Design for:

```text
WebSocket disconnects
gateway crashes
API crashes
Kafka duplicate events
Kafka consumer restarts
Redis restarts
database failover
cross-region network failures
client retries
out-of-order realtime delivery
```

Prefer:

```text
at-least-once delivery
+
idempotency
```

over attempting exactly-once behavior across the entire distributed system.

---

# 29. Backpressure

Do not allow slow WebSocket clients to consume unlimited server memory.

Each connection must have bounded outbound buffers.

Conceptually:

```text
Kafka/events
     │
     ▼
Gateway
     │
     ▼
bounded queue
     │
     ▼
WebSocket
```

If the client cannot keep up:

```text
disconnect client
```

The client can reconnect and synchronize durable events.

Never allow an unbounded per-connection queue.

---

# 30. Hot Channels

Do not assume hashing creates perfectly balanced traffic.

A single channel may become extremely hot.

The architecture should distinguish:

```text
storage partitioning
```

from:

```text
delivery fanout
```

A hot channel may need specialized fanout infrastructure without moving its authoritative message history.

Do not prematurely implement this, but keep message persistence and realtime fanout decoupled.

---

# 31. Large Channels

Small channels and enormous broadcast-style channels have different characteristics.

For normal channels:

```text
fan-out-on-write
```

is acceptable.

For channels with hundreds of thousands/millions of members, eventually support:

```text
fan-out-on-read
```

or hierarchical fanout.

Do not perform millions of synchronous writes when one message is created.

V1 may enforce conservative channel-member limits until large-channel fanout exists.

---

# 32. PostgreSQL Rules

Prefer simple queries with known shard keys.

Good:

```text
channel_id + sequence
user_id + channel_id
```

Avoid queries requiring:

```text
scan every shard
JOIN across shards
global ORDER BY
large OFFSET
```

Cross-shard joins should generally be solved through application-level data modeling or derived indexes.

---

# 33. IDs

Use globally unique IDs that can be generated without a central database.

UUIDv7 or another time-sortable globally unique ID strategy is preferred.

Important IDs include:

```text
user_id
channel_id
message_id
client_message_id
event_id
```

Do not depend on PostgreSQL auto-increment IDs across shards.

---

# 34. Service Boundaries

Start with a small number of deployable services.

Do NOT create dozens of microservices.

A reasonable initial architecture:

```text
Gateway
Chat/API
Kafka consumers/workers
```

Internally keep clear modules for:

```text
users
channels
messages
routing
quotas
storage
events
realtime
```

A modular monolith for business logic is preferable to premature microservices.

Services can be extracted later when scaling characteristics justify it.

---

# 35. Suggested Repository Structure

Example:

```text
cmd/
    api/
    gateway/
    worker/

internal/
    users/
    channels/
    messages/
    membership/
    routing/
    quota/
    realtime/
    events/

    storage/
        postgres/
        redis/
        kafka/

    platform/
        config/
        logging/
        metrics/
        tracing/
```

Keep domain logic independent from infrastructure implementations where practical.

---

# 36. API Design

Initial API surface should remain very small.

Conceptually:

```text
POST /users

POST /channels
GET  /channels

POST /channels/{id}/members

POST /channels/{id}/messages
GET  /channels/{id}/messages
```

And:

```text
WebSocket /connect
```

Exact API design may evolve.

Prioritize cursor-based pagination, idempotency and stable identifiers from the beginning.

---

# 37. Observability

Observability is mandatory from the beginning.

Every service should expose:

```text
metrics
structured logs
distributed traces
health checks
```

Track at minimum:

```text
active WebSocket connections

messages/sec
message persistence latency
message delivery latency

p50
p90
p95
p99

Kafka producer latency
Kafka consumer lag

PostgreSQL query latency
PostgreSQL connections

Redis latency
Redis errors

cross-region latency

rate-limit rejections
quota rejections

WebSocket disconnect rate
```

Include identifiers such as:

```text
request_id
event_id
channel_id
region
virtual_shard
physical_shard
```

where appropriate.

Do not log message bodies by default.

---

# 38. Load Testing

Scalability claims must be benchmarked.

Create load-testing tooling early.

Test independently:

```text
WebSocket connection capacity
messages/sec
Kafka throughput
PostgreSQL inserts/sec
message history reads
Redis rate limiting
cross-region routing
fanout
reconnection storms
```

Important scenario:

```text
1M connected users
```

does not mean:

```text
1M simultaneously active users.
```

Model connection count and message rate separately.

---

# 39. Capacity Planning

Design infrastructure around measurable units.

Track:

```text
connections / gateway
messages / second
events / second
Kafka MB / second
DB writes / second
DB reads / second
Redis operations / second
network MB / second
storage growth / day
```

Scaling decisions should be driven by these metrics rather than registered-user count.

---

# 40. Infrastructure Philosophy

Initial deployment should favor simple, cost-efficient infrastructure.

Prefer:

```text
dedicated servers / VMs
Docker
systemd or simple container orchestration
managed DNS
load balancers
```

Do not introduce Kubernetes merely because the system is intended to become large.

Kubernetes can be introduced when operational complexity makes it beneficial.

The application architecture must not depend on Kubernetes.

---

# 41. Development Environment

Local development should reproduce the important architectural components using Docker Compose.

Example:

```text
Go API
Go Gateway
PostgreSQL
Kafka
Redis/Valkey
```

Developers and agents should be able to start the complete environment with a small number of commands.

---

# 42. Migration Philosophy

Schema changes must assume databases eventually contain billions of records.

Avoid migrations requiring:

```text
rewrite entire messages table
lock massive tables
update every existing message
```

Prefer:

```text
additive migrations
background backfills
dual-read/write transitions when necessary
```

Think about future migration cost before introducing fields to high-volume tables.

---

# 43. Security Basics

Even though features are minimal, enforce:

```text
authentication
channel membership authorization
input size limits
rate limiting
TLS
secure internal communication
```

A user must never be able to send/read messages from a channel without authorization.

Never trust:

```text
user_id
tier
region
permissions
```

provided by the client.

---

# 44. Future Features

Future features should fit naturally into the event architecture.

For example:

```text
reaction.added
reaction.removed

message.edited
message.deleted

user.blocked
user.unblocked

typing.started
typing.stopped

read.updated
```

Durable events can flow through Kafka.

Ephemeral events such as typing/presence generally should not require PostgreSQL or Kafka durability.

Do not implement these features yet.

---

# 45. Priority Order

When making architectural decisions, optimize in this order:

1. Correctness
2. Clear shard boundaries
3. Horizontal scalability
4. Failure recovery
5. Predictable latency
6. Observability
7. Operational simplicity
8. Infrastructure cost
9. Feature velocity

Avoid sacrificing the first five for premature feature development.

---

# 46. Avoid Premature Complexity

Being "ready for scale" does NOT mean implementing Facebook's entire infrastructure immediately.

Build the abstractions that allow scaling.

For example:

```text
GOOD

4096 virtual shards
      ↓
currently mapped to
      ↓
1 PostgreSQL instance
```

rather than:

```text
BAD

deploy 64 PostgreSQL servers
before having users
```

Likewise:

```text
GOOD

region-aware routing interface

currently:
EU + US + ASIA
```

The architecture should support scale without requiring all future infrastructure to exist on day one.

---

# 47. Core Architectural Model

Keep this mental model:

```text
                         CLIENT
                           │
                           │ WebSocket / HTTP
                           ▼
                    Nearest Gateway
                           │
                           ▼
                      Chat/API
                           │
              ┌────────────┴────────────┐
              │                         │
              ▼                         ▼
         Quota/Redis               Channel Router
                                        │
                              channel.home_region
                                        │
                              virtual_shard
                                        │
                              physical_shard
                                        │
                                        ▼
                                   PostgreSQL
                                        │
                                   Outbox Event
                                        │
                                        ▼
                                      Kafka
                                        │
                    ┌───────────────────┼──────────────────┐
                    ▼                   ▼                  ▼
                   EU                  US                ASIA
                 Delivery            Delivery           Delivery
                    │                   │                  │
                    ▼                   ▼                  ▼
                Gateways            Gateways           Gateways
                    │                   │                  │
                    ▼                   ▼                  ▼
                 Clients             Clients            Clients
```

The most important invariants are:

```text
ONE channel
    ↓
ONE authoritative home region
    ↓
ONE virtual shard
    ↓
ONE authoritative write path
```

and:

```text
user_id
    ↓
user-centric channel index
```

Therefore the two most common operations:

```text
GetChannels(user_id)
```

and:

```text
GetMessages(channel_id)
```

should **not require scatter/gather across the cluster**.

---

# 48. V1 Success Criteria

V1 is successful when we can reliably demonstrate:

```text
Create user
     ↓
Create channel
     ↓
Add users
     ↓
Connect users through WebSocket
     ↓
Send message
     ↓
Persist exactly one logical message
     ↓
Publish event
     ↓
Deliver realtime
     ↓
Disconnect recipient
     ↓
Send more messages
     ↓
Reconnect recipient
     ↓
Recover missing messages
```

while simultaneously demonstrating:

```text
tier limits
rate limits
idempotent retries
cursor pagination
virtual shard routing
region-aware routing
Kafka event delivery
Redis-backed distributed state
failure recovery
observability
```

The objective is **not feature completeness**.

The objective is proving that the architectural foundation can evolve from:

```text
1 server
```

to:

```text
many regions
+
many gateways
+
many Kafka partitions
+
many PostgreSQL shards
+
millions of concurrent connections
```

without rewriting the fundamental data model.

---

# 49. Instructions for Coding Agents

When implementing new functionality:

1. Do not bypass the routing layer and access a database directly from domain code.
2. Do not introduce cross-shard queries without explicit architectural justification.
3. Do not introduce global database scans.
4. Do not use OFFSET pagination for large datasets.
5. Do not use timestamps as the sole message ordering mechanism.
6. Do not make Redis the authoritative message store.
7. Do not treat WebSocket delivery as durable.
8. Do not publish Kafka events outside the reliable outbox/event mechanism.
9. Make Kafka consumers idempotent.
10. Do not store important state only in process memory.
11. Apply tier/quota enforcement through the centralized quota system.
12. Assume every operation may be retried.
13. Assume every service may crash between any two operations.
14. Assume regions can temporarily lose connectivity.
15. Keep hot-path database queries simple and shard-local.
16. Add metrics for new hot-path operations.
17. Avoid adding infrastructure unless justified by an actual scaling requirement.
18. Prefer boring, understandable technology over unnecessary distributed-system complexity.

When uncertain, optimize the design around:

> **Can this operation remain shard-local, region-aware, retry-safe, observable, and horizontally scalable?**

If not, reconsider the design before implementing it.

