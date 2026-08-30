// Illustrative pricing/limits — mirrors deploy/tiers.yaml, the config that
// actually enforces these numbers. Edit both together if either changes.
// Shared between the public landing page (/) and the billing page, so the
// numbers a visitor sees before signing up never drift from what an
// existing org sees when upgrading.
export const PLANS = [
  {
    tier: "FREE",
    name: "Free",
    price: "$0",
    period: "forever",
    tagline: "Try the platform with a single app.",
    features: ["1 app", "1 channel", "Up to 3 members per channel", "20 messages/min per app", "7-day message history"],
  },
  {
    tier: "PRO",
    name: "Pro",
    price: "$29",
    period: "/month",
    tagline: "For a growing product with real users.",
    features: ["5 apps", "20 channels per app", "Up to 100 members per channel", "200 messages/min per app", "30-day message history"],
  },
  {
    tier: "BUSINESS",
    name: "Business",
    price: "$99",
    period: "/month",
    tagline: "Higher ceilings for production traffic.",
    features: ["20 apps", "100 channels per app", "Up to 1,000 members per channel", "1,000 messages/min per app", "90-day message history"],
  },
  {
    tier: "ENTERPRISE",
    name: "Enterprise",
    price: "Custom",
    period: "",
    tagline: "Custom limits, contracts, and support.",
    features: [
      "1,000 apps",
      "10,000 channels per app",
      "Up to 100,000 members per channel",
      "10,000 messages/min per app",
      "Unlimited message history",
    ],
  },
] as const;

export const TIER_RANK: Record<string, number> = { FREE: 0, PRO: 1, BUSINESS: 2, ENTERPRISE: 3 };

export const UPGRADABLE_TIERS = new Set(["PRO", "BUSINESS"]);
