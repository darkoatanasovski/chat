"use client";

import { useState, type FormEvent } from "react";
import { AnimatePresence, motion } from "framer-motion";
import {
  ArrowUpRight,
  Boxes,
  Check,
  ChevronDown,
  Copy,
  Globe2,
  MessagesSquare,
  Send,
  ShieldOff,
  Users as UsersIcon,
} from "lucide-react";
import { ApiError } from "@/lib/api";
import {
  useAppsMessagesDailyQuery,
  useCreateAppMutation,
  useOrgBlockedCountQuery,
  useOrgMessagesUsageQuery,
  useUsageQuery,
} from "@/lib/queries";
import type { CreatedApp } from "@/lib/types";
import { ConsoleShell, useSession } from "@/components/shell";
import { WorldMap } from "@/components/worldmap";
import { AnimatedNumber, Button, ErrorBanner, Input, Label, Panel, Skeleton, Sparkline, WizardProgress } from "@/components/ui";

// Matches the backend's dashboardDailyWindowDays (cmd/api/handlers_dashboard.go).
const DAILY_WINDOW = 7;

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

// Org-wide summary across every app — totals here, the breakdown for one
// app in particular lives on that app's own Dashboard tab
// (console/app/console/apps/[id]/page.tsx). Zero apps is exclusively
// "not yet onboarded" (apps are never deleted), so that state takes over
// this whole page as step 2 of the signup wizard (FirstAppStep) instead.
function OverviewView() {
  const { session } = useSession();
  const usageQuery = useUsageQuery(session.token);
  const usage = usageQuery.data;
  const error = usageQuery.error ? (usageQuery.error instanceof ApiError ? usageQuery.error.message : String(usageQuery.error)) : null;

  const totalUsers = usage?.apps_detail.reduce((sum, a) => sum + a.users, 0) ?? 0;
  const totalChannels = usage?.apps_detail.reduce((sum, a) => sum + a.channels, 0) ?? 0;

  if (usage && usage.apps.used === 0) {
    return <FirstAppStep onCreated={() => usageQuery.refetch()} />;
  }

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
        <div className="mb-4 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
        </div>
      )}
      {!usage && !error && <Skeleton className="mb-6 h-40 w-full" />}

      {usage && (
        <div className="mb-4 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <StatCard
            index={0}
            icon={<Boxes className="h-5 w-5" />}
            label="Apps"
            value={usage.apps.used}
            suffix={` / ${usage.apps.limit}`}
            subtitle={`on the ${usage.tier} plan`}
          />
          <StatCard index={1} icon={<UsersIcon className="h-5 w-5" />} label="Users" value={totalUsers} subtitle="across all apps" />
          <StatCard index={2} icon={<MessagesSquare className="h-5 w-5" />} label="Channels" value={totalChannels} subtitle="across all apps" />
          <BlockedStatCard index={3} />
        </div>
      )}

      {/* Its own row, not a fifth grid tile — the daily trend needs real
          width to read, and it's the one stat card here with a chart. */}
      {usage && (
        <div className="mb-6">
          <MessagesStatCard index={4} />
        </div>
      )}

      <div className="mb-6">
        <WorldMap />
      </div>

      {usage && (
        <Panel animate={false}>
          <div className="mb-5 flex items-center justify-between">
            <h2 className="text-base font-semibold text-text">Your apps</h2>
            <a href="/console/apps" className="flex items-center gap-1 text-sm text-text-muted transition-colors duration-150 hover:text-accent">
              View all
              <ArrowUpRight className="h-3.5 w-3.5" />
            </a>
          </div>
          {usage.apps_detail.length === 0 && <p className="text-[15px] text-text-muted">No apps yet — create your first one to get started.</p>}
          <div className="flex flex-col gap-2">
            {usage.apps_detail.slice(0, 5).map((app, i) => (
              <motion.a
                key={app.app_id}
                href={`/console/apps/${app.app_id}`}
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
  const { data: total } = useOrgBlockedCountQuery(session.token, session.org.org_id);

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
  const [expanded, setExpanded] = useState(false);
  const { data: usage } = useOrgMessagesUsageQuery(session.token, session.org.org_id);
  const { data: dailyRes } = useAppsMessagesDailyQuery(session.token);

  // Org-wide daily trend: sum every app's per-day count for each of the
  // last DAILY_WINDOW days (handleDashboardAppsMessagesDaily already
  // aligns every app's `daily` array to the same date order, so summing
  // index-by-index is safe without re-checking dates here).
  const daily = dailyRes
    ? dailyRes.apps.reduce((totals, app) => {
        app.daily.forEach((count, i) => {
          totals[i] += count;
        });
        return totals;
      }, new Array(dailyRes.days.length).fill(0))
    : null;

  const today = daily ? daily[daily.length - 1] : 0;

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
        {/* A full-width row (see the Overview page's layout above) gets a
            side-by-side split instead of the narrow stat-tile's vertical
            stack: the total on the left, the trend given real width on the
            right, so the chart is actually legible rather than squeezed
            into a five-across grid tile. */}
        <div className="mt-5 flex flex-col gap-6 sm:flex-row sm:items-center">
          <div className="shrink-0">
            <div className="flex items-baseline gap-1.5">
              <AnimatedNumber value={usage?.total ?? 0} className="text-4xl font-semibold text-text" />
            </div>
            <div className="mt-1.5 text-sm text-text-faint">sent across all apps</div>
          </div>
          <div className="flex-1 sm:border-l sm:border-border-soft sm:pl-6">
            <Sparkline values={daily ?? new Array(DAILY_WINDOW).fill(0)} height={48} />
            <div className="mt-1.5 flex items-center justify-between text-[11px] text-text-faint">
              <span>last {DAILY_WINDOW} days</span>
              <span>
                <span className="text-text-muted">{today.toLocaleString()}</span> today
              </span>
            </div>
          </div>
        </div>

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

const WIZARD_LABELS = ["Organization", "First app"];
const CONFETTI_COLORS = ["bg-emerald-400", "bg-amber-400", "bg-rose-400", "bg-cyan-400", "bg-violet-400", "bg-blue-400"];

// FirstAppStep is step 2 of the signup wizard (step 1 is /signup) — it
// takes over the whole Overview content area whenever an org has zero
// apps. Since apps are never deleted in this platform, that's exclusively
// the not-yet-onboarded state, so this same branch also catches an
// existing account that somehow never finished onboarding, with no
// separate route to keep in sync.
function FirstAppStep({ onCreated }: { onCreated: () => void }) {
  const { session } = useSession();
  const createAppMutation = useCreateAppMutation(session.token, session.org.org_id);
  const [name, setName] = useState("");
  const [why, setWhy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<CreatedApp | null>(null);
  const [copiedKey, setCopiedKey] = useState(false);
  const [copiedSecret, setCopiedSecret] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const app = await createAppMutation.mutateAsync(name.trim());
      setCreated(app);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  function copy(text: string, which: "key" | "secret") {
    navigator.clipboard
      .writeText(text)
      .then(() => {
        const set = which === "key" ? setCopiedKey : setCopiedSecret;
        set(true);
        setTimeout(() => set(false), 1500);
      })
      .catch(() => setError("Couldn't copy automatically — select and copy the text manually."));
  }

  return (
    <div className="flex min-h-[70vh] items-center justify-center">
      <div className="w-full max-w-md">
        <WizardProgress step={2} total={2} labels={WIZARD_LABELS} />

        <AnimatePresence mode="wait">
          {!created ? (
            <motion.div key="form" initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -8 }} transition={{ duration: 0.25 }}>
              <div className="mb-7 flex flex-col items-center gap-3.5 text-center">
                <span className="grid h-14 w-14 place-items-center rounded-full bg-accent-soft text-accent">
                  <Globe2 className="h-6 w-6" />
                </span>
                <h1 className="text-2xl font-semibold text-text">Create your first app</h1>
                <p className="text-[15px] text-text-muted">
                  This is where you&apos;ll build something like Messenger, Slack, or Discord — global from the very first message. An app
                  is an isolated chat instance with its own end-users, channels, and API keys.
                </p>
              </div>

              <button
                type="button"
                onClick={() => setWhy((v) => !v)}
                className="mb-4 flex w-full items-center justify-center gap-1.5 text-[13px] text-text-muted transition-colors duration-150 hover:text-text"
              >
                Why isn&apos;t there a region setting?
                <motion.span animate={{ rotate: why ? 180 : 0 }} transition={{ duration: 0.2 }}>
                  <ChevronDown className="h-3.5 w-3.5" />
                </motion.span>
              </button>
              <AnimatePresence initial={false}>
                {why && (
                  <motion.div
                    initial={{ height: 0, opacity: 0 }}
                    animate={{ height: "auto", opacity: 1 }}
                    exit={{ height: 0, opacity: 0 }}
                    transition={{ duration: 0.25, ease: [0.16, 1, 0.3, 1] }}
                    className="overflow-hidden"
                  >
                    <p className="mb-5 rounded-xl border border-border-soft bg-surface-2/40 px-4 py-3.5 text-[13px] text-text-muted">
                      Most chat platforms make you pin an app to one region, then your users on the other side of the planet pay for it in
                      latency. Every app you create here already spans all three regions — messages route through whichever is closest to
                      the sender, and channels stay consistent globally behind the scenes. You build once; it&apos;s already fast
                      everywhere.
                    </p>
                  </motion.div>
                )}
              </AnimatePresence>

              <Panel>
                <form onSubmit={handleSubmit} className="flex flex-col gap-4">
                  <div>
                    <Label>App name</Label>
                    <Input required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Production" />
                  </div>
                  <Button type="submit" variant="primary" loading={createAppMutation.isPending} className="justify-center">
                    Create app
                  </Button>
                  {error && <ErrorBanner>{error}</ErrorBanner>}
                </form>
              </Panel>
            </motion.div>
          ) : (
            <motion.div key="success" initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.3 }}>
              <div className="mb-7 flex flex-col items-center gap-3.5 text-center">
                <ConfettiBurst />
                <h1 className="text-2xl font-semibold text-text">You&apos;re all set</h1>
                <p className="text-[15px] text-text-muted">
                  <span className="font-medium text-text">{created.name}</span> is live across every region right now.
                </p>
              </div>
              <Panel>
                <div className="flex flex-col gap-4">
                  <ErrorBanner>
                    <span className="text-text">
                      This secret is shown <strong>once</strong> — copy it now. You can always generate a new credential later from this
                      app&apos;s Credentials tab.
                    </span>
                  </ErrorBanner>
                  <div>
                    <Label>API key</Label>
                    <div className="flex items-center gap-2">
                      <code className="flex-1 truncate rounded-lg border border-border bg-surface-2 px-3 py-2 font-mono text-xs text-text">
                        {created.credential.key}
                      </code>
                      <Button
                        variant="secondary"
                        onClick={() => copy(created.credential.key, "key")}
                        icon={copiedKey ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
                      />
                    </div>
                  </div>
                  <div>
                    <Label>API secret</Label>
                    <div className="flex items-center gap-2">
                      <code className="flex-1 truncate rounded-lg border border-border bg-surface-2 px-3 py-2 font-mono text-xs text-text">
                        {created.credential.secret}
                      </code>
                      <Button
                        variant="secondary"
                        onClick={() => copy(created.credential.secret ?? "", "secret")}
                        icon={copiedSecret ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
                      />
                    </div>
                  </div>
                  <Button variant="primary" className="justify-center" onClick={onCreated}>
                    Go to your dashboard
                  </Button>
                </div>
              </Panel>
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  );
}

// A small one-shot burst of dots behind the success checkmark — no confetti
// library needed for six divs that radiate out and fade once on mount.
function ConfettiBurst() {
  const dots = CONFETTI_COLORS.map((color, i) => {
    const angle = (i / CONFETTI_COLORS.length) * Math.PI * 2;
    return { color, x: Math.cos(angle) * 42, y: Math.sin(angle) * 42, delay: i * 0.03 };
  });

  return (
    <div className="relative grid h-14 w-14 place-items-center">
      {dots.map((d, i) => (
        <motion.span
          key={i}
          className={`absolute h-1.5 w-1.5 rounded-full ${d.color}`}
          initial={{ x: 0, y: 0, opacity: 1, scale: 1 }}
          animate={{ x: d.x, y: d.y, opacity: 0, scale: 0.4 }}
          transition={{ duration: 0.6, delay: 0.1 + d.delay, ease: [0.16, 1, 0.3, 1] }}
        />
      ))}
      <motion.span
        initial={{ scale: 0 }}
        animate={{ scale: 1 }}
        transition={{ type: "spring", stiffness: 400, damping: 18, delay: 0.05 }}
        className="grid h-14 w-14 place-items-center rounded-full bg-success-soft text-success"
      >
        <Check className="h-6 w-6" />
      </motion.span>
    </div>
  );
}
