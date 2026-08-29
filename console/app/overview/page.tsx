"use client";

import { useEffect, useState } from "react";
import { AnimatePresence, motion } from "framer-motion";
import { ArrowUpRight, Boxes, ChevronDown, MessagesSquare, Send, ShieldOff, Users as UsersIcon } from "lucide-react";
import { getMessagesUsage, getUsage, listApps, listDashboardBlocks, ApiError } from "@/lib/api";
import type { MessagesUsage, Usage } from "@/lib/types";
import { ConsoleShell, useSession } from "@/components/shell";
import { WorldMap } from "@/components/worldmap";
import { AnimatedNumber, ErrorBanner, Panel, Skeleton } from "@/components/ui";

function regionLabel(region: string) {
  return { eu: "Europe", us: "North America", asia: "Asia Pacific" }[region] ?? region;
}

export default function OverviewPage() {
  return (
    <ConsoleShell>
      <OverviewView />
    </ConsoleShell>
  );
}

function OverviewView() {
  const { session } = useSession();
  const [usage, setUsage] = useState<Usage | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getUsage(session.token)
      .then(setUsage)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }, [session.token]);

  const totalUsers = usage?.apps_detail.reduce((sum, a) => sum + a.users, 0) ?? 0;
  const totalChannels = usage?.apps_detail.reduce((sum, a) => sum + a.channels, 0) ?? 0;

  return (
    <div>
      <div className="mb-8">
        <h1 className="text-2xl font-semibold text-text">Overview</h1>
        <p className="mt-1.5 text-[15px] text-text-muted">A live snapshot of {session.org.name}&apos;s chat platform.</p>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      {!usage && !error && (
        <div className="mb-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-5">
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
        </div>
      )}

      {usage && (
        <div className="mb-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-5">
          <StatCard
            index={0}
            icon={<Boxes className="h-5 w-5" />}
            label="Apps"
            value={usage.apps.used}
            suffix={` / ${usage.apps.limit}`}
            subtitle={`on the ${usage.tier} plan`}
          />
          <StatCard index={1} icon={<UsersIcon className="h-5 w-5" />} label="End-users" value={totalUsers} subtitle="across all apps" />
          <StatCard index={2} icon={<MessagesSquare className="h-5 w-5" />} label="Channels" value={totalChannels} subtitle="across all apps" />
          <MessagesStatCard index={3} />
          <BlockedStatCard index={4} />
        </div>
      )}

      <div className="mb-6">
        <WorldMap />
      </div>

      {usage && (
        <Panel animate={false}>
          <div className="mb-5 flex items-center justify-between">
            <h2 className="text-base font-semibold text-text">Your apps</h2>
            <a href="/apps" className="flex items-center gap-1 text-sm text-text-muted transition-colors duration-150 hover:text-accent">
              View all
              <ArrowUpRight className="h-3.5 w-3.5" />
            </a>
          </div>
          {usage.apps_detail.length === 0 && <p className="text-[15px] text-text-muted">No apps yet — create your first one to get started.</p>}
          <div className="flex flex-col gap-2">
            {usage.apps_detail.slice(0, 5).map((app, i) => (
              <motion.a
                key={app.app_id}
                href={`/apps/${app.app_id}`}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.05 }}
                className="flex items-center justify-between rounded-xl border border-border-soft px-4 py-3.5 transition-colors duration-150 hover:border-accent/30"
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
                    <MessagesSquare className="h-4 w-4" />
                    <span className="font-mono text-[15px] text-text">{app.channels}</span>
                  </div>
                </div>
              </motion.a>
            ))}
          </div>
        </Panel>
      )}
    </div>
  );
}

function StatCard({
  index,
  icon,
  label,
  value,
  suffix,
  subtitle,
}: {
  index: number;
  icon: React.ReactNode;
  label: string;
  value: number;
  suffix?: string;
  subtitle: string;
}) {
  return (
    <motion.div
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ delay: index * 0.06, duration: 0.3, ease: [0.16, 1, 0.3, 1] }}
    >
      <Panel animate={false} className="transition-colors duration-150 hover:border-accent/25">
        <div className="mb-4 flex items-center gap-3">
          <span className="grid h-11 w-11 shrink-0 place-items-center rounded-full bg-accent-soft text-accent">{icon}</span>
          <span className="text-[13px] font-medium uppercase tracking-wide text-text-muted">{label}</span>
        </div>
        <div className="flex items-baseline gap-1.5">
          <AnimatedNumber value={value} className="text-4xl font-semibold text-text" />
          {suffix && <span className="text-xl text-text-faint">{suffix}</span>}
        </div>
        <div className="mt-1.5 text-sm text-text-faint">{subtitle}</div>
      </Panel>
    </motion.div>
  );
}

function BlockedStatCard({ index }: { index: number }) {
  const { session } = useSession();
  const [total, setTotal] = useState<number | null>(null);

  useEffect(() => {
    listApps(session.token, session.org.org_id)
      .then((apps) => Promise.all(apps.map((app) => listDashboardBlocks(session.token, app.app_id))))
      .then((perApp) => setTotal(perApp.reduce((sum, blocks) => sum + blocks.length, 0)))
      .catch(() => {});
  }, [session.token, session.org.org_id]);

  return (
    <motion.div
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ delay: index * 0.06, duration: 0.3, ease: [0.16, 1, 0.3, 1] }}
    >
      <Panel animate={false} className="transition-colors duration-150 hover:border-accent/25">
        <div className="mb-4 flex items-center gap-3">
          <span className="grid h-11 w-11 shrink-0 place-items-center rounded-full bg-danger-soft text-danger">
            <ShieldOff className="h-5 w-5" />
          </span>
          <span className="text-[13px] font-medium uppercase tracking-wide text-text-muted">Blocked</span>
        </div>
        <AnimatedNumber value={total ?? 0} className="text-4xl font-semibold text-text" />
        <div className="mt-1.5 text-sm text-text-faint">users across all apps</div>
      </Panel>
    </motion.div>
  );
}

function MessagesStatCard({ index }: { index: number }) {
  const { session } = useSession();
  const [usage, setUsage] = useState<MessagesUsage | null>(null);
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    getMessagesUsage(session.token)
      .then(setUsage)
      .catch(() => {});
  }, [session.token]);

  return (
    <motion.div
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ delay: index * 0.06, duration: 0.3, ease: [0.16, 1, 0.3, 1] }}
    >
      <Panel animate={false} className="transition-colors duration-150 hover:border-accent/25">
        <button
          onClick={() => setExpanded((v) => !v)}
          disabled={!usage}
          className="flex w-full items-center justify-between gap-3 text-left disabled:cursor-default"
        >
          <div className="flex items-center gap-3">
            <span className="grid h-11 w-11 shrink-0 place-items-center rounded-full bg-accent-soft text-accent">
              <Send className="h-5 w-5" />
            </span>
            <span className="text-[13px] font-medium uppercase tracking-wide text-text-muted">Messages</span>
          </div>
          {usage && (
            <motion.span animate={{ rotate: expanded ? 180 : 0 }} transition={{ duration: 0.2 }} className="text-text-faint">
              <ChevronDown className="h-4 w-4" />
            </motion.span>
          )}
        </button>
        <div className="mt-4 flex items-baseline gap-1.5">
          <AnimatedNumber value={usage?.total ?? 0} className="text-4xl font-semibold text-text" />
        </div>
        <div className="mt-1.5 text-sm text-text-faint">sent across all apps</div>

        <AnimatePresence initial={false}>
          {expanded && usage && (
            <motion.div
              initial={{ height: 0, opacity: 0 }}
              animate={{ height: "auto", opacity: 1 }}
              exit={{ height: 0, opacity: 0 }}
              transition={{ duration: 0.25, ease: [0.16, 1, 0.3, 1] }}
              className="overflow-hidden"
            >
              <div className="mt-5 flex flex-col gap-2.5 border-t border-border-soft pt-4">
                {usage.by_region.map((r, i) => (
                  <motion.div
                    key={r.region}
                    initial={{ opacity: 0, x: -4 }}
                    animate={{ opacity: 1, x: 0 }}
                    transition={{ delay: i * 0.04 }}
                    className="flex items-center justify-between text-[13px]"
                  >
                    <span className="text-text-muted">{regionLabel(r.region)}</span>
                    <AnimatedNumber value={r.messages} className="font-mono text-text" />
                  </motion.div>
                ))}
              </div>
            </motion.div>
          )}
        </AnimatePresence>
      </Panel>
    </motion.div>
  );
}
