"use client";

import { useEffect, useRef, useState } from "react";
import { motion } from "framer-motion";
import { Check, Loader2 } from "lucide-react";
import { me, ApiError } from "@/lib/api";
import { ConsoleShell, useSession } from "@/components/shell";
import { Button, Panel } from "@/components/ui";

const POLL_INTERVAL_MS = 1500;
const MAX_POLLS = 20; // ~30s — the subscription.active webhook is usually near-instant.

// Dodo redirects the customer here once checkout completes, but the org's
// tier only actually changes once our own subscription.active webhook has
// been processed (see cmd/api/handlers_billing.go) — that can lag the
// redirect by a second or two. So rather than trust any query param Dodo
// puts on the return URL, this page just polls GET /dashboard/me until the
// tier it already has on file changes, and gives up gracefully if it
// doesn't within a reasonable window.
export default function BillingReturnPage() {
  return (
    <ConsoleShell>
      <BillingReturnView />
    </ConsoleShell>
  );
}

function BillingReturnView() {
  const { session, setSession } = useSession();
  const startingTier = useRef(session.org.tier);
  const [status, setStatus] = useState<"waiting" | "done" | "timeout">("waiting");
  const [newTier, setNewTier] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    let attempts = 0;

    async function poll() {
      attempts += 1;
      try {
        const result = await me(session.token);
        if (cancelled) return;
        if (result.org.tier !== startingTier.current) {
          setSession({ ...session, org: result.org });
          setNewTier(result.org.tier);
          setStatus("done");
          return;
        }
      } catch (err) {
        // Transient errors just retry on the next tick — surfacing them
        // here would be noisier than useful for a background poll.
        if (!(err instanceof ApiError)) throw err;
      }
      if (attempts >= MAX_POLLS) {
        if (!cancelled) setStatus("timeout");
        return;
      }
      if (!cancelled) setTimeout(poll, POLL_INTERVAL_MS);
    }

    poll();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.token]);

  return (
    <div className="flex min-h-[70vh] items-center justify-center">
      <div className="w-full max-w-md">
        <Panel>
          <div className="flex flex-col items-center gap-4 text-center">
            {status === "waiting" && (
              <>
                <span className="grid h-14 w-14 place-items-center rounded-full bg-accent-soft text-accent">
                  <Loader2 className="h-6 w-6 animate-spin" />
                </span>
                <h1 className="text-xl font-semibold text-text">Confirming your payment</h1>
                <p className="text-[15px] text-text-muted">This usually takes a couple of seconds — hang tight.</p>
              </>
            )}
            {status === "done" && (
              <motion.div
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                className="flex flex-col items-center gap-4"
              >
                <span className="grid h-14 w-14 place-items-center rounded-full bg-success-soft text-success">
                  <Check className="h-6 w-6" />
                </span>
                <h1 className="text-xl font-semibold text-text">You&apos;re on the {newTier} plan</h1>
                <p className="text-[15px] text-text-muted">Your new limits are already in effect.</p>
                <Button variant="primary" className="w-full justify-center" onClick={() => (window.location.href = "/console/overview")}>
                  Go to your dashboard
                </Button>
              </motion.div>
            )}
            {status === "timeout" && (
              <>
                <h1 className="text-xl font-semibold text-text">Still processing</h1>
                <p className="text-[15px] text-text-muted">
                  Your payment went through, but it&apos;s taking longer than usual to reflect here. It&apos;s safe to leave this page — your
                  plan updates automatically once it&apos;s done.
                </p>
                <Button variant="secondary" className="w-full justify-center" onClick={() => (window.location.href = "/console/billing")}>
                  Back to billing
                </Button>
              </>
            )}
          </div>
        </Panel>
      </div>
    </div>
  );
}
