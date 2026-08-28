"use client";

import { useState } from "react";
import { ArrowRight, Globe2, Layers, Radio, UserPlus } from "lucide-react";
import { createUser, ApiError } from "@/lib/api";
import { REGIONS, REGION_ENDPOINTS, type Region } from "@/lib/regions";
import { loadRoster, saveProfile, setActiveProfile } from "@/lib/session";
import { credentialsForTier, TIER_LIMITS, TIERS, type Tier } from "@/lib/tiers";
import type { Profile } from "@/lib/types";
import { Avatar, Button, ErrorBanner, Input, Panel, Select } from "@/components/ui";

export function SignIn({ onSignedIn }: { onSignedIn: (profile: Profile) => void }) {
  const [roster] = useState(loadRoster);
  const [displayName, setDisplayName] = useState("");
  const [region, setRegion] = useState<Region>("eu");
  const [tier, setTier] = useState<Tier>("FREE");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      const credentials = credentialsForTier(tier);
      if (!credentials) {
        setError(`No demo app credentials configured for tier ${tier} (set NEXT_PUBLIC_DEMO_APP_CREDENTIALS_${tier} — see deploy/seed.sh output)`);
        setLoading(false);
        return;
      }
      const apiBase = REGION_ENDPOINTS[region].apiBase;
      const user = await createUser(apiBase, displayName.trim() || "anonymous", region, credentials);
      const profile: Profile = {
        userId: user.user_id,
        displayName: user.display_name,
        region,
        tier: user.tier,
        token: user.token,
      };
      saveProfile(profile);
      onSignedIn(profile);
    } catch (err) {
      setError(err instanceof ApiError ? `${err.status}: ${err.message}` : String(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="mx-auto flex w-full max-w-sm flex-col gap-5">
      <div className="flex flex-col items-center gap-2.5 text-center">
        <span className="grid h-11 w-11 place-items-center rounded-2xl bg-accent-soft text-accent ring-1 ring-inset ring-accent/25">
          <Radio className="h-5 w-5" strokeWidth={2.25} />
        </span>
        <h1 className="text-lg font-semibold text-text">chat-platform-demo</h1>
        <p className="text-sm text-text-muted">Test harness for tier limits, idempotency, and realtime delivery.</p>
      </div>

      {roster.length > 0 && (
        <Panel className="flex flex-col gap-1.5" animate={false}>
          <p className="mb-1 px-1 text-[11px] font-semibold tracking-wide text-text-muted uppercase">
            Continue as
          </p>
          {roster.map((p) => (
            <button
              key={p.userId}
              onClick={() => {
                setActiveProfile(p);
                onSignedIn(p);
              }}
              className="flex items-center gap-2.5 rounded-lg px-2 py-1.5 text-left transition-colors duration-150 hover:bg-white/[0.04]"
            >
              <Avatar name={p.displayName} size="sm" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm text-text">{p.displayName}</div>
                <div className="font-mono text-[11px] text-text-faint">{p.region}</div>
              </div>
              <ArrowRight className="h-3.5 w-3.5 text-text-faint" />
            </button>
          ))}
        </Panel>
      )}

      <Panel animate={false}>
        <div className="mb-1 flex items-center gap-2">
          <UserPlus className="h-4 w-4 text-accent" strokeWidth={2.25} />
          <h2 className="text-sm font-semibold text-text">Create a test user</h2>
        </div>
        <p className="mb-3.5 text-xs text-text-muted">
          No password — a dev-grade token is minted immediately, directly on whichever tier you pick below.
        </p>
        <form onSubmit={handleCreate} className="flex flex-col gap-2.5">
          <Input
            placeholder="display name"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            required
          />
          <div className="flex gap-2.5">
            <div className="relative flex-1">
              <Globe2 className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-text-faint" />
              <Select className="w-full appearance-none pl-8" value={region} onChange={(e) => setRegion(e.target.value as Region)}>
                {REGIONS.map((r) => (
                  <option key={r} value={r}>
                    {REGION_ENDPOINTS[r].label}
                  </option>
                ))}
              </Select>
            </div>
            <div className="relative flex-1">
              <Layers className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-text-faint" />
              <Select className="w-full appearance-none pl-8" value={tier} onChange={(e) => setTier(e.target.value as Tier)}>
                {TIERS.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            </div>
          </div>
          <p className="-mt-0.5 font-mono text-[11px] text-text-faint">
            {TIER_LIMITS[tier].channels} channel{TIER_LIMITS[tier].channels === "1" ? "" : "s"} &middot;{" "}
            {TIER_LIMITS[tier].members} members &middot; {TIER_LIMITS[tier].messagesPerMinute} msg/min
          </p>
          <Button type="submit" variant="primary" loading={loading} icon={<UserPlus className="h-3.5 w-3.5" />}>
            {loading ? "Creating" : "Create user"}
          </Button>
        </form>
        {error && (
          <div className="mt-3">
            <ErrorBanner>{error}</ErrorBanner>
          </div>
        )}
      </Panel>
    </div>
  );
}
