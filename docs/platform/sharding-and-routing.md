# Sharding and Routing

Implements INSTRUCTIONS.md §6–§8. Answers, for any `channel_id`, the chain:

```
channel_id → home_region → virtual_shard → physical_shard
```

without ever requiring a database round trip on the hot path for the
computed parts.

## Virtual shards

`internal/routing/shards.go`:

```go
func (r *Router) VirtualShard(key string) int {
    h := fnv.New32a()
    h.Write([]byte(key))
    return int(h.Sum32()) % r.cfg.VirtualShardCount
}
```

4096 virtual shards (`deploy/shards.yaml`), a pure function of the key
(`channel_id` for messages, or in principle `user_id` for a future sharded
index — see [data-model.md](data-model.md) for why V1's `user_channels`
stays on the single control-plane instance instead). No lookup, no cache, no
I/O — this must be cheap enough to call on every single request.

## Physical shards

`deploy/shards.yaml` maps *ranges* of virtual shards onto physical Postgres
instances:

```yaml
virtual_shard_count: 4096
physical_shards:
  - id: shard-a
    dsn_env: SHARD_A_DSN
    virtual_shard_range: [0, 2047]
  - id: shard-b
    dsn_env: SHARD_B_DSN
    virtual_shard_range: [2048, 4095]
```

Loaded once at startup (`routing.LoadShardsConfig`), held in memory. Adding
physical capacity means adding an entry here and moving virtual-shard ranges
onto it — the hash function itself never changes, so which virtual shard a
channel belongs to is permanent even as physical placement changes.

This is why virtual shards exist at all: mapping `channel_id` hashes
straight onto 2 physical Postgres instances would mean any future resharding
has to rehash and migrate *every* channel. With virtual shards, rebalancing
is "move virtual shards 512–1023 from shard-a to shard-c," a data-copy
operation with a known, bounded scope.

## Home region: the one non-computed hop

`home_region` isn't a hash — it's a real fact stored on the `channels` row
at creation time (whichever region's `api` instance handled `POST
/channels` becomes the channel's home). `internal/routing.RegionResolver`
caches it in Redis with a Postgres fallback:

```go
func (r *RegionResolver) HomeRegion(ctx context.Context, channelID string) (string, error) {
    // Redis GET route:channel:<id>:home_region, fall through to Postgres on miss,
    // populate the cache on the way back.
}
```

TTL is 10 minutes; home_region is immutable in V1 (no "move channel region"
operation exists yet — `InvalidateHomeRegion` is the unused seam for one).

## Putting it together

```go
func (r *Router) Resolve(key string) (physicalShardID string, virtualShard int, err error) {
    vs := r.VirtualShard(key)
    id, err := r.PhysicalShardID(vs)
    return id, vs, err
}
```

`cmd/api` calls this (`App.shardPoolFor`) to pick a `*pgxpool.Pool` for
message reads/writes, and calls `RegionResolver.HomeRegion` separately to
decide whether to handle a write locally or forward it — see
[multi-region.md](multi-region.md). These are independent questions:
*where a channel's data lives* (physical shard) and *which region is allowed
to write it* (home region) don't have to be the same axis, and in this
deployment aren't — any api instance can reach both shard Postgres
instances; only the home-region rule is enforced at the application layer.
A geo-distributed deployment would add real per-region read replicas (§26)
on top of this, which is why the two concerns are kept separate now instead
of conflated.

## Rule for new code

Domain packages take a `physicalShardID` or a resolved `*pgxpool.Pool` as an
argument — they never call `routing.Router` themselves and never guess a
shard. If you're writing a query and don't already have a pool/shard ID in
hand, that's a sign the caller (an HTTP handler in `cmd/api`, or a
`cmd/worker` entrypoint) needs to resolve it first, per
INSTRUCTIONS.md §49 rule 1.
