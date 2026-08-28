# ADR 0002: One Authoritative Home Region Per Channel

## Status

Accepted.

## Context

Users in different regions can be members of the same channel
(INSTRUCTIONS.md §4: "do not require users participating in the same
channel to be located in the same region"). Every message needs a
monotonically increasing per-channel `sequence` (§10), which requires
exactly one process able to assign "the next sequence number" for a given
channel at any moment.

## Decision

Every channel has exactly one `home_region`, fixed at creation time to the
region of the `api` instance that created it. All writes to that channel —
sending a message, adding a member — are authoritative only when performed
by that region's `api` instance; any other region's instance forwards the
request there instead of writing locally.

## Alternatives considered

**Active-active writes with conflict resolution** (e.g. CRDTs, vector
clocks, last-write-wins on sequence assignment). Explicitly rejected by
INSTRUCTIONS.md §5: "Do not implement active-active writes for the
same channel." Even setting the spec aside, this trades a simple,
understandable single-writer invariant for genuinely hard distributed-
systems complexity (sequence gaps, conflicting concurrent sequence
assignments, reconciliation logic) to solve a problem V1 doesn't actually
have yet.

**Global sequence coordinator** (e.g. a dedicated sequencing service every
region calls). Adds a new single point of contention and failure in the hot
path of every message send, for every channel, everywhere — worse latency
characteristics than "usually local, occasionally one cross-region hop for
channels whose members happen to span regions."

## Consequences

- A cross-region send costs one extra HTTP hop
  (`cmd/api/forward.go`) — acceptable, and only paid when the sender's
  region differs from the channel's home region, not on every message.
- Sequence assignment (`internal/messages.Repo.Send`) stays a single-Postgres-
  transaction operation with a single-row lock — no distributed consensus
  needed anywhere in the message-send path.
- If a home region becomes unreachable, writes to *that region's* channels
  fail (or queue, in a fuller implementation) — this is a deliberate
  availability/consistency trade-off in favor of correctness, matching
  INSTRUCTIONS.md's priority order (§45: correctness and clear shard
  boundaries rank above availability nuance).
- Reads don't require this at all — see
  `docs/platform/multi-region.md` for why reads are served locally against
  shared Postgres in this deployment.

## Where this lives in code

`channels.home_region` column (`migrations/control/0001_init.sql`),
`internal/routing.RegionResolver` (cached lookup),
`cmd/api/forward.go` (the forwarding mechanism),
`cmd/api/handlers_messages.go` / `handlers_channels.go` (the call sites).
