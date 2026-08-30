"use client";

import { useState } from "react";
import { motion } from "framer-motion";
import { Check, Loader2, Sparkles } from "lucide-react";
import { createBillingCheckout, ApiError } from "@/lib/api";
import { ConsoleShell, useSession } from "@/components/shell";
import { Button, ErrorBanner, Panel, cx } from "@/components/ui";
import { PLANS, TIER_RANK, UPGRADABLE_TIERS } from "@/lib/plans";

export default function BillingPage() {
  return (
    <ConsoleShell>
      <BillingView />
    </ConsoleShell>
  );
}

function BillingView() {
  const { session } = useSession();
  const [loadingTier, setLoadingTier] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const currentRank = TIER_RANK[session.org.tier] ?? 0;

  async function upgrade(tier: string) {
    setError(null);
    setLoadingTier(tier);
    try {
      const { checkout_url } = await createBillingCheckout(session.token, tier);
      window.location.href = checkout_url;
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
      setLoadingTier(null);
    }
  }

  return (
    <div>
      <div className="mb-8">
        <h1 className="text-2xl font-semibold text-text">Billing</h1>
        <p className="mt-1.5 text-[15px] text-text-muted">
          {session.org.name} is on the <span className="text-text">{session.org.tier}</span> plan. Upgrade for higher limits — payment is
          handled by Dodo Payments' hosted checkout.
        </p>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {PLANS.map((plan, i) => {
          const rank = TIER_RANK[plan.tier];
          const isCurrent = plan.tier === session.org.tier;
          const canUpgrade = UPGRADABLE_TIERS.has(plan.tier) && rank > currentRank;

          return (
            <motion.div
              key={plan.tier}
              initial={{ opacity: 0, y: 10 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.06, duration: 0.3, ease: [0.16, 1, 0.3, 1] }}
            >
              <Panel
                animate={false}
                className={cx("flex h-full flex-col", isCurrent ? "border-accent/50" : "transition-colors duration-150 hover:border-accent/25")}
              >
                <div className="mb-1 flex items-center justify-between">
                  <h2 className="text-lg font-semibold text-text">{plan.name}</h2>
                  {isCurrent && (
                    <span className="inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent-soft px-2.5 py-0.5 text-[11px] font-medium text-accent">
                      <Sparkles className="h-3 w-3" />
                      Current
                    </span>
                  )}
                </div>
                <div className="mb-3 flex items-baseline gap-1">
                  <span className="text-3xl font-semibold text-text">{plan.price}</span>
                  {plan.period && <span className="text-sm text-text-faint">{plan.period}</span>}
                </div>
                <p className="mb-5 text-sm text-text-muted">{plan.tagline}</p>

                <ul className="mb-6 flex flex-1 flex-col gap-2.5">
                  {plan.features.map((f) => (
                    <li key={f} className="flex items-start gap-2 text-[13px] text-text-muted">
                      <Check className="mt-0.5 h-3.5 w-3.5 shrink-0 text-accent" />
                      {f}
                    </li>
                  ))}
                </ul>

                {isCurrent ? (
                  <Button variant="secondary" disabled className="justify-center">
                    Current plan
                  </Button>
                ) : canUpgrade ? (
                  <Button variant="primary" loading={loadingTier === plan.tier} disabled={loadingTier !== null} onClick={() => upgrade(plan.tier)} className="justify-center">
                    {loadingTier === plan.tier ? <Loader2 className="h-4 w-4 animate-spin" /> : `Upgrade to ${plan.name}`}
                  </Button>
                ) : plan.tier === "ENTERPRISE" ? (
                  <Button variant="secondary" disabled className="justify-center">
                    Contact us
                  </Button>
                ) : (
                  <Button variant="secondary" disabled className="justify-center">
                    Included
                  </Button>
                )}
              </Panel>
            </motion.div>
          );
        })}
      </div>
    </div>
  );
}
