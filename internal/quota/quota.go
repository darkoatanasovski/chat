package quota

import (
	"context"
	"fmt"
	"time"
)

type Decision struct {
	Allowed bool
	Reason  string
}

func allow() Decision             { return Decision{Allowed: true} }
func deny(reason string) Decision { return Decision{Allowed: false, Reason: reason} }

// Quota is the centralized capability/limit authority
// (INSTRUCTIONS.md §22/§23): `limits.Allow(subject, capability, context)`.
type Quota struct {
	tiers       map[string]TierLimits
	rateLimiter *RateLimiter
}

func New(tiers map[string]TierLimits, rateLimiter *RateLimiter) *Quota {
	return &Quota{tiers: tiers, rateLimiter: rateLimiter}
}

func (q *Quota) limitsFor(tier string) (TierLimits, error) {
	limits, ok := q.tiers[tier]
	if !ok {
		return TierLimits{}, fmt.Errorf("quota: unknown tier %q", tier)
	}
	return limits, nil
}

// LimitsFor exposes a tier's raw limits — the dashboard's usage view shows
// "3 of 5 apps used" and needs the "5", not just an allow/deny decision.
func (q *Quota) LimitsFor(tier string) (TierLimits, error) {
	return q.limitsFor(tier)
}

// AllowResource enforces a resource limit (e.g. max_channels,
// max_channel_members) against a count the caller has just read from
// authoritative Postgres state. Resource limits must ultimately be checked
// against real state, not just Redis, to avoid races that create invalid
// persistent state (INSTRUCTIONS.md §25).
func (q *Quota) AllowResource(tier, capability string, currentCount int) (Decision, error) {
	limits, err := q.limitsFor(tier)
	if err != nil {
		return Decision{}, err
	}

	var limit int
	switch capability {
	case CapabilityAppCreate:
		limit = limits.MaxApps
	case CapabilityChannelCreate:
		limit = limits.MaxChannels
	case CapabilityChannelMemberAdd:
		limit = limits.MaxChannelMembers
	default:
		return Decision{}, fmt.Errorf("quota: %q is not a resource capability", capability)
	}

	if currentCount >= limit {
		return deny(fmt.Sprintf("%s limit reached (%d)", capability, limit)), nil
	}
	return allow(), nil
}

// AllowRate enforces a rate limit via the Redis-backed token bucket, keyed
// by subjectKey (e.g. "rate:message:user:123").
func (q *Quota) AllowRate(ctx context.Context, tier, capability, subjectKey string) (Decision, error) {
	limits, err := q.limitsFor(tier)
	if err != nil {
		return Decision{}, err
	}

	var perMinute int
	switch capability {
	case CapabilityMessageSend:
		perMinute = limits.MessagesPerMinute
	case CapabilityReactionWrite:
		perMinute = limits.ReactionsPerMinute
	case CapabilityReadUpdate:
		perMinute = limits.ReadUpdatesPerMinute
	default:
		return Decision{}, fmt.Errorf("quota: %q is not a rate capability", capability)
	}

	ok, err := q.rateLimiter.Allow(ctx, subjectKey, perMinute, time.Now().Unix())
	if err != nil {
		return Decision{}, err
	}
	if !ok {
		return deny(fmt.Sprintf("%s rate limit exceeded (%d/min)", capability, perMinute)), nil
	}
	return allow(), nil
}
