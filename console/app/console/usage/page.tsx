"use client";

import { motion } from "framer-motion";
import { BarChart3, ExternalLink, MessageCircle, Users as UsersIcon } from "lucide-react";
import { ApiError } from "@/lib/api";
import { useUsageQuery } from "@/lib/queries";
import { ConsoleShell, useSession } from "@/components/shell";
import { AnimatedNumber, ErrorBanner, Panel, Skeleton } from "@/components/ui";

const GRAFANA_URL = process.env.NEXT_PUBLIC_GRAFANA_URL ?? "http://localhost:3003/d/chat-platform-overview";

export default function UsagePage() {
  return (
    <ConsoleShell>
      <UsageView />
    </ConsoleShell>
  );
}

function UsageView() {
  const { session } = useSession();
  const usageQuery = useUsageQuery(session.token);
  const usage = usageQuery.data;
  const error = usageQuery.error ? (usageQuery.error instanceof ApiError ? usageQuery.error.message : String(usageQuery.error)) : null;

  return (
    <div>
      <div className="mb-8 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-text">Usage</h1>
          <p className="mt-1.5 text-[15px] text-text-muted">How much of your plan you&apos;re using right now.</p>
        </div>
        <a
          href={GRAFANA_URL}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-2 rounded-xl border border-border bg-surface-2 px-4 py-2.5 text-[15px] text-text-muted transition-colors duration-150 hover:border-text-faint hover:text-text"
        >
          <BarChart3 className="h-4 w-4" />
          Detailed metrics in Grafana
          <ExternalLink className="h-3.5 w-3.5" />
        </a>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      {!usage && !error && (
        <div className="flex flex-col gap-4">
          <Skeleton className="h-32" />
          <Skeleton className="h-40" />
        </div>
      )}

      {usage && (
        <>
          <Panel className="mb-6">
            <div className="mb-5 flex items-end justify-between">
              <div>
                <div className="text-[13px] font-medium uppercase tracking-wide text-text-faint">Apps on the {usage.tier} plan</div>
                <div className="mt-1.5 flex items-baseline gap-1.5">
                  <AnimatedNumber value={usage.apps.used} className="text-4xl font-semibold text-text" />
                  <span className="text-xl text-text-faint">/ {usage.apps.limit}</span>
                </div>
              </div>
            </div>
            <div className="h-2.5 overflow-hidden rounded-full bg-surface-2">
              <motion.div
                className="h-full rounded-full bg-accent"
                initial={{ width: 0 }}
                animate={{ width: `${Math.min(100, (usage.apps.used / Math.max(usage.apps.limit, 1)) * 100)}%` }}
                transition={{ duration: 0.6, ease: [0.16, 1, 0.3, 1] }}
              />
            </div>
          </Panel>

          <Panel animate={false}>
            <h2 className="mb-5 text-base font-semibold text-text">Per-app activity</h2>
            {usage.apps_detail.length === 0 && <p className="text-[15px] text-text-muted">Create an app to see activity here.</p>}
            <div className="flex flex-col gap-2">
              {usage.apps_detail.map((app, i) => (
                <motion.div
                  key={app.app_id}
                  initial={{ opacity: 0, y: 6 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ delay: i * 0.05 }}
                  className="flex items-center justify-between rounded-xl border border-border-soft px-4 py-3.5"
                >
                  <div className="min-w-0">
                    <div className="truncate text-[15px] text-text">{app.name}</div>
                    <div className="font-mono text-xs text-text-faint">app_id: {app.app_id}</div>
                  </div>
                  <div className="flex items-center gap-5">
                    <div className="flex items-center gap-2 text-text-muted">
                      <UsersIcon className="h-4 w-4" />
                      <span className="font-mono text-[15px] text-text">{app.users}</span>
                    </div>
                    <div className="flex items-center gap-2 text-text-muted">
                      <MessageCircle className="h-4 w-4" />
                      <span className="font-mono text-[15px] text-text">{app.channels}</span>
                    </div>
                  </div>
                </motion.div>
              ))}
            </div>
          </Panel>
        </>
      )}
    </div>
  );
}
