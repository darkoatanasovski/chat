# ADR 0005: KRaft-Mode Kafka and Valkey Instead of Zookeeper+Kafka and Redis

## Status

Accepted. Local-development/deployment-choice ADR, not an architectural one
— nothing in the application code depends on either choice.

## Context

INSTRUCTIONS.md §2 specifies "Kafka" and "Redis / Valkey" as the
infrastructure, without mandating a specific deployment mode or a choice
between Redis and Valkey.

## Decision

**Kafka in KRaft mode** (`apache/kafka:3.8.0`, `KAFKA_PROCESS_ROLES:
broker,controller`), not the classic Zookeeper-coordinated deployment.
**Valkey** (`valkey/valkey:7.2-alpine`), not Redis, for the `go-redis`
client target.

## Rationale

**KRaft over Zookeeper**: Kafka's own project has deprecated new
Zookeeper-based deployments in favor of KRaft as of the versions in scope
here. Running KRaft in combined broker+controller mode means one container
instead of two (Kafka + Zookeeper) for equivalent local functionality, with
one less moving part to configure, monitor, and explain in
`deploy/docker-compose.yml`. Nothing about the application's use of Kafka
(topics, partitioning by `channel_id`, consumer groups) differs between
deployment modes — this is purely an operational simplification, matching
INSTRUCTIONS.md §40's "prefer boring, understandable technology" and §46's
"avoid premature complexity."

**Valkey over Redis**: Valkey is the Linux-Foundation-governed, BSD-licensed
fork of Redis (post-Redis's 2024 license change to SSPL/RSALv2), maintained
by many of Redis's original contributors, and remains wire-protocol
compatible — the `go-redis` client (`internal/storage/redis`) talks to it
with zero code differences from talking to Redis itself. Choosing Valkey
avoids licensing ambiguity for anyone building on this platform commercially
without changing a single line of Go code or Redis command used.

## Consequences

- Swapping either back (Zookeeper-mode Kafka, or actual Redis) requires only
  `deploy/docker-compose.yml` changes — zero application code changes,
  since `internal/storage/kafka` and `internal/storage/redis` only depend on
  wire-protocol compatibility, not deployment topology or vendor.
- KRaft single-node mode has no broker redundancy — acceptable for local
  development and this single-machine simulation; a production multi-region
  deployment would run a proper multi-broker Kafka cluster (KRaft or
  otherwise) per the operational needs of that environment, which is again
  purely a `deploy/`-level concern.

## Where this lives in code

`deploy/docker-compose.yml` (`kafka`, `valkey` services),
`internal/storage/kafka/kafka.go`, `internal/storage/redis/redis.go`
(neither imports anything Kafka-flavor- or Redis-fork-specific).
