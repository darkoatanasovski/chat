# Quotas and Tiers

Implements INSTRUCTIONS.md §22–§25. One centralized authority
(`internal/quota`) for every tier/capability check — no `if user.Tier ==
"FREE"` scattered through handlers.

## Tiers

Defined in `deploy/tiers.yaml`, not hardcoded — loaded once at `cmd/api`
startup (`quota.LoadTiers`):

```yaml
tiers:
  FREE:
    max_channels: 1
    max_channel_members: 3
    messages_per_minute: 20
  PRO:
    max_channels: 20
    max_channel_members: 100
    messages_per_minute: 200
  BUSINESS: { max_channels: 100, max_channel_members: 1000, messages_per_minute: 1000 }
  ENTERPRISE: { max_channels: 10000, max_channel_members: 100000, messages_per_minute: 10000 }
```

Every new user gets `FREE` at creation (`users.DefaultTier`). There is no
tier-upgrade endpoint in V1 (out of scope — `tier.changed` is listed as a
future Kafka event in INSTRUCTIONS.md §14).

## Two kinds of limit, checked differently

INSTRUCTIONS.md §25 distinguishes these deliberately:

**Resource limits** (`max_channels`, `max_channel_members`) — checked
against a count read fresh from authoritative Postgres at request time
(`channels.Repo.CountByCreator`, `membership.Repo.CountMembers`), then
`Quota.AllowResource`. Exceeding these would create invalid persistent
state, so Redis is never trusted as the sole source of truth here — a race
between two concurrent requests can each pass the check and both insert, but
the check is always against real state, not a cached count that could be
stale in the direction that matters.

**Rate limits** (`messages_per_minute`) — enforced entirely in Redis via
`Quota.AllowRate` → `RateLimiter.Allow`, a token bucket implemented as a
single Lua script (`internal/quota/ratelimit.go`) so one rate check costs one
Redis round trip (§24: "algorithms that do not require excessive Redis
operations"). Being briefly wrong about a rate limit has no durable-state
consequence, so Redis alone is sufficient here, unlike resource limits.

## The call sites

```go
// cmd/api/handlers_channels.go — resource limit
decision, _ := a.quota.AllowResource(identity.Tier, quota.CapabilityChannelCreate, currentCount)

// cmd/api/handlers_messages.go — rate limit
decision, _ := a.quota.AllowRate(ctx, identity.Tier, quota.CapabilityMessageSend, "rate:message:user:"+identity.UserID.String())
```

Capabilities are string constants (`quota.CapabilityChannelCreate`,
`CapabilityChannelMemberAdd`, `CapabilityMessageSend`) — matches
INSTRUCTIONS.md §23's `Allow(subject, capability, context)` shape, adapted
into two typed methods (`AllowResource`/`AllowRate`) because the two kinds of
limit need genuinely different inputs (a count vs. a Redis key), not because
the concept is different.

## Token bucket details

`internal/quota/ratelimit.go`'s Lua script stores `{tokens, updated_at}` in a
Redis hash per subject key, refills continuously at `limit/60` tokens/sec
(so a client can burst its full per-minute allowance, then must wait for
it to regenerate), and expires the key after 120s of inactivity. One
`EVALSHA` per check.

## Adding a new capability

1. Add a constant in `internal/quota/tiers.go`.
2. Add the corresponding field to `TierLimits` and `deploy/tiers.yaml`.
3. Call `AllowResource` or `AllowRate` at the operation's entry point in
   `cmd/api` — not inside a domain package (keep the centralization).
4. Increment `RateLimitRejectionsTotal`/`QuotaRejectionsTotal` on rejection
   (already wired for the two existing capabilities — see
   [observability.md](observability.md)).
