package quota

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func testTiers() map[string]TierLimits {
	return map[string]TierLimits{
		TierFree: {MaxChannels: 1, MaxChannelMembers: 3, MessagesPerMinute: 20},
		TierPro:  {MaxChannels: 50, MaxChannelMembers: 500, MessagesPerMinute: 600},
	}
}

func TestQuota_AllowResource(t *testing.T) {
	q := New(testTiers(), nil)

	t.Run("under limit allows", func(t *testing.T) {
		d, err := q.AllowResource(TierFree, CapabilityChannelCreate, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !d.Allowed {
			t.Fatalf("expected allow with 0 of 1 channels used, got deny: %s", d.Reason)
		}
	})

	t.Run("at limit denies", func(t *testing.T) {
		d, err := q.AllowResource(TierFree, CapabilityChannelCreate, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Allowed {
			t.Fatalf("expected deny at FREE tier's max_channels limit (1), got allow")
		}
		if d.Reason == "" {
			t.Fatalf("expected a non-empty deny reason")
		}
	})

	t.Run("over limit denies", func(t *testing.T) {
		d, err := q.AllowResource(TierFree, CapabilityChannelMemberAdd, 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Allowed {
			t.Fatalf("expected deny with 5 members already, over the FREE limit of 3")
		}
	})

	t.Run("unknown tier errors", func(t *testing.T) {
		if _, err := q.AllowResource("NOT_A_TIER", CapabilityChannelCreate, 0); err == nil {
			t.Fatalf("expected an error for an unknown tier")
		}
	})

	t.Run("unknown capability errors", func(t *testing.T) {
		if _, err := q.AllowResource(TierFree, "not.a.capability", 0); err == nil {
			t.Fatalf("expected an error for a non-resource capability")
		}
	})

	t.Run("rate capability rejected as a resource capability", func(t *testing.T) {
		if _, err := q.AllowResource(TierFree, CapabilityMessageSend, 0); err == nil {
			t.Fatalf("expected an error: message.send is a rate capability, not a resource capability")
		}
	})
}

func TestQuota_AllowRate(t *testing.T) {
	q := New(testTiers(), NewRateLimiter(testRedis(t)))
	ctx := context.Background()
	subject := "rate:message:user:" + uuid.NewString()

	d, err := q.AllowRate(ctx, TierFree, CapabilityMessageSend, subject)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("expected first send to be allowed under a fresh FREE-tier bucket")
	}

	t.Run("unknown tier errors", func(t *testing.T) {
		if _, err := q.AllowRate(ctx, "NOT_A_TIER", CapabilityMessageSend, subject); err == nil {
			t.Fatalf("expected an error for an unknown tier")
		}
	})

	t.Run("resource capability rejected as a rate capability", func(t *testing.T) {
		if _, err := q.AllowRate(ctx, TierFree, CapabilityChannelCreate, subject); err == nil {
			t.Fatalf("expected an error: channel.create is a resource capability, not a rate capability")
		}
	})
}

func TestQuota_AllowRate_ExhaustsBucket(t *testing.T) {
	tiers := map[string]TierLimits{
		TierFree: {MessagesPerMinute: 2},
	}
	q := New(tiers, NewRateLimiter(testRedis(t)))
	ctx := context.Background()
	subject := "rate:message:user:" + uuid.NewString()

	for i := 1; i <= 2; i++ {
		d, err := q.AllowRate(ctx, TierFree, CapabilityMessageSend, subject)
		if err != nil {
			t.Fatalf("send %d: unexpected error: %v", i, err)
		}
		if !d.Allowed {
			t.Fatalf("send %d: expected allow within the 2/min FREE limit", i)
		}
	}

	d, err := q.AllowRate(ctx, TierFree, CapabilityMessageSend, subject)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Allowed {
		t.Fatalf("expected the 3rd send within the same minute to be rate-limited")
	}
	if d.Reason == "" {
		t.Fatalf("expected a non-empty deny reason")
	}
}
