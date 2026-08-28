export const TIERS = ["FREE", "PRO", "BUSINESS", "ENTERPRISE"] as const;
export type Tier = (typeof TIERS)[number];

export const TIER_LIMITS: Record<Tier, { channels: string; members: string; messagesPerMinute: string }> = {
  FREE: { channels: "1", members: "3", messagesPerMinute: "20" },
  PRO: { channels: "20", members: "100", messagesPerMinute: "200" },
  BUSINESS: { channels: "100", members: "1000", messagesPerMinute: "1000" },
  ENTERPRISE: { channels: "10000", members: "100000", messagesPerMinute: "10000" },
};

// Tier is no longer a field an end-user request chooses — it's resolved
// live from the App's owning Organization. The demo's tier picker instead
// selects *which seeded App's credentials* to authenticate POST /users
// with (see deploy/seed.sh, which prints these as NEXT_PUBLIC_ env vars).
// Next.js only inlines NEXT_PUBLIC_* vars referenced as literal
// `process.env.NEXT_PUBLIC_X` expressions, so each tier is spelled out
// here rather than built from a template string.
const DEMO_APP_CREDENTIALS: Record<Tier, string | undefined> = {
  FREE: process.env.NEXT_PUBLIC_DEMO_APP_CREDENTIALS_FREE,
  PRO: process.env.NEXT_PUBLIC_DEMO_APP_CREDENTIALS_PRO,
  BUSINESS: process.env.NEXT_PUBLIC_DEMO_APP_CREDENTIALS_BUSINESS,
  ENTERPRISE: process.env.NEXT_PUBLIC_DEMO_APP_CREDENTIALS_ENTERPRISE,
};

export function credentialsForTier(tier: Tier): { key: string; secret: string } | null {
  const raw = DEMO_APP_CREDENTIALS[tier];
  if (!raw) return null;
  const [key, secret] = raw.split(":");
  if (!key || !secret) return null;
  return { key, secret };
}
