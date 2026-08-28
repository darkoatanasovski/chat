# ADR 0001: Virtual Shards Between Channel IDs and Physical Postgres Instances

## Status

Accepted.

## Context

Messages must be sharded by `channel_id` (INSTRUCTIONS.md §7): all of a
channel's messages live on one logical shard, so the primary message query
touches exactly one shard. The question is how `channel_id` maps to a
physical Postgres instance.

## Decision

Introduce a fixed, large number of virtual shards (4096) between the hash
space and physical instances. `virtual_shard = hash(channel_id) %
4096`, computed in-process with no I/O. A separate, small config file
(`deploy/shards.yaml`) maps *ranges* of virtual shards onto physical
Postgres instances (currently `shard-a` = 0–2047, `shard-b` = 2048–4095).

## Alternatives considered

**Hash directly onto physical instances**
(`physical = hash(channel_id) % num_physical_shards`). Simpler, but adding a
physical shard changes `num_physical_shards`, which changes the hash-mod
result for nearly every existing channel — every channel would need to be
identified as "moved" and its data physically relocated in one atomic
operation, or the hashing scheme would need versioning gymnastics.

**Range-based sharding by channel creation time or ID prefix.** Rejected
outright per INSTRUCTIONS.md §7 — this is explicitly called out as the
wrong shape ("channel A messages 1–10k → shard 1" is a bad pattern the spec
warns against; that describes bucketing *within* a channel, not sharding
*across* channels, and conflating the two was an early mistake to avoid).

## Consequences

- Adding physical capacity is "move virtual shard ranges," a bounded,
  plannable data-migration operation — never a rehash of the entire
  channel space.
- The virtual-shard count (4096) is chosen once, generously, up front —
  it should comfortably outlive any physical shard count the system will
  reach, since *that* number is harder to change without a full rehash.
  Going from 2 to 20 physical shards never requires touching it; going from
  4096 to 8192 virtual shards would.
- One extra indirection (virtual → physical) to reason about, for a system
  that at V1 only has 2 physical shards — deliberately "ready for scale"
  rather than "at scale" (INSTRUCTIONS.md §46).

## Where this lives in code

`internal/routing/shards.go` (`Router.VirtualShard`,
`Router.PhysicalShardID`, `Router.Resolve`), config in `deploy/shards.yaml`.
