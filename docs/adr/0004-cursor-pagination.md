# ADR 0004: Sequence-Cursor Pagination, Never OFFSET

## Status

Accepted.

## Context

`GET /channels/{id}/messages` needs to page through potentially millions of
messages per channel over time. INSTRUCTIONS.md §11 is explicit:
`OFFSET 1000000 LIMIT 50` is called out by name as the pattern to avoid.

## Decision

Paginate by the same monotonic per-channel `sequence` used for ordering
(§10): `WHERE channel_id = $1 AND sequence < $2 ORDER BY sequence DESC
LIMIT $3`, where `$2` is the caller-supplied `before` cursor (the oldest
`sequence` already seen).

## Alternatives considered

**`OFFSET`/`LIMIT`.** Postgres still has to scan and discard every skipped
row server-side — cost grows linearly with how far into a channel's history
you page, meaning the 1000th page of an old, active channel gets
progressively slower forever. Also fundamentally unstable under concurrent
writes: rows can shift position between page requests, causing skipped or
duplicated results.

**Timestamp-based cursors** (`created_at < $2`). Rejected per
INSTRUCTIONS.md §10: timestamps are not guaranteed strictly monotonic or
unique (clock behavior, concurrent inserts in the same millisecond), so they
can't serve as a stable ordering/pagination key on their own. `sequence` is
assigned by a single-row-locked counter specifically so it never has this
problem.

**Opaque cursor tokens** (base64-encoded composite state, common in many
public APIs). More flexible in general, but unnecessary complexity here —
`sequence` is already a stable, meaningful, single-column cursor; wrapping
it in an opaque token would add a decode/encode step without adding
capability.

## Consequences

- The query is always `WHERE channel_id = $1 AND sequence < $2` against the
  table's primary key `(channel_id, sequence)` — no additional index needed,
  and the plan is a simple, cheap index range scan regardless of how deep
  into history the caller pages.
- Reconnection/gap-recovery (§20) uses the same mechanism — a client's "I
  have through sequence N" is directly usable as a pagination cursor, no
  separate recovery protocol needed.
- The trade-off: no "jump to page 50" — cursor pagination is inherently
  sequential. Not a real limitation for a chat history UI, which always
  pages backward from "now" or forward from "last seen."

## Where this lives in code

`internal/messages/messages.go` (`Repo.ListBefore`),
`cmd/api/handlers_messages.go` (`handleListMessages`, `before`/`limit` query
params), `demo/app/channels/[id]/page.tsx` (`loadOlder`, the reference
client usage).
