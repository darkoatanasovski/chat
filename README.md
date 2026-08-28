# Chat Platform

A globally-distributed, horizontally-scalable chat backend — built to the V1
scope and architectural rules in [`.claude/INSTRUCTIONS.md`](.claude/INSTRUCTIONS.md):
users, channels, membership, sending/retrieving messages, listing a user's
channels, realtime delivery, and tier-based quotas, on an architecture
designed to scale to millions of connections without a rewrite.

```
Client → nearest Gateway (WebSocket) / API (REST)
             │
        Quota (Redis)         Channel Router
                                    │
                          home_region → virtual_shard → physical_shard
                                    │
                              Postgres (shard-local write)
                                    │
                          Outbox event → Kafka (channel_id-keyed)
                                    │
                    every Gateway consumes, delivers to its
                       local connections that are channel members
```

Three logical regions (`eu`/`us`/`asia`), simulated locally as three `api` +
`gateway` pairs sharing one Kafka broker and one Redis-compatible cache; two
physical Postgres message shards behind 4096 virtual shards; a
transactional outbox driving realtime fanout. See
[`docs/platform/architecture-overview.md`](docs/platform/architecture-overview.md)
for the full picture.

## Repository layout

```
cmd/            api, gateway, worker — the three deployable services
internal/       domain logic + infra wrappers (see docs/platform/architecture-overview.md)
migrations/     additive-only SQL, control-plane and shard schemas
deploy/         docker-compose.yml, shard/tier config, migration + seed scripts
tools/loadtest/ connection/throughput/latency load-testing CLI
demo/           minimal Next.js app for exercising the platform by hand
docs/           architecture, API reference, operations, ADRs (start at docs/README.md)
.claude/skills/ platform-up, new-migration, new-event, loadtest
```

## Quick start

```bash
cp .env.example .env
make up                # build + start all 13 containers
./deploy/migrate.sh    # apply schema (idempotent)
./deploy/seed.sh        # optional: demo users + a channel + a message

cd demo && npm install && npm run dev   # http://localhost:3000
```

Full walkthrough: [`docs/operations/local-development.md`](docs/operations/local-development.md).
API reference: [`docs/api/rest-api.md`](docs/api/rest-api.md) and
[`docs/api/websocket-protocol.md`](docs/api/websocket-protocol.md).

## Documentation

Everything else — architecture deep-dives, API reference, operations,
architecture decision records — is indexed at
[`docs/README.md`](docs/README.md).

## Status

V1 feature set only, by design (INSTRUCTIONS.md §1): no reactions, threads,
edit/delete, search, presence, attachments, or E2EE yet. The architecture is
built so each of those is an additive Kafka event + package, not a redesign
— see [`docs/platform/kafka-and-events.md`](docs/platform/kafka-and-events.md)
and the `new-event` skill.
