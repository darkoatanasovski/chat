"use client";

import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { useRouter, usePathname } from "next/navigation";
import { motion } from "framer-motion";
import { Boxes, Gauge, LayoutDashboard, LogOut, MessagesSquare, Users } from "lucide-react";
import { loadSession, clearSession, saveSession } from "@/lib/session";
import { getUsage, ApiError } from "@/lib/api";
import type { Session, Usage } from "@/lib/types";
import { Avatar, Badge, cx } from "./ui";

const NAV_ITEMS = [
  { href: "/overview", label: "Overview", icon: LayoutDashboard },
  { href: "/apps", label: "Apps", icon: Boxes },
  { href: "/team", label: "Team", icon: Users },
  { href: "/usage", label: "Usage", icon: Gauge },
];

const SessionContext = createContext<{ session: Session; setSession: (s: Session) => void } | null>(null);

/** Reads the current console session — must be called from a page rendered
 * inside <ConsoleShell>, which guarantees a session exists by the time
 * children render at all. */
export function useSession() {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used within ConsoleShell");
  return ctx;
}

export function ConsoleShell({ children }: { children: ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [session, setSessionState] = useState<Session | null | undefined>(undefined);
  const [usage, setUsage] = useState<Usage | null>(null);

  useEffect(() => {
    const s = loadSession();
    if (!s) {
      router.replace("/login");
      return;
    }
    setSessionState(s);
  }, [router]);

  useEffect(() => {
    if (!session) return;
    getUsage(session.token)
      .then(setUsage)
      .catch(() => {});
  }, [session]);

  function setSession(s: Session) {
    saveSession(s);
    setSessionState(s);
  }

  function signOut() {
    clearSession();
    router.replace("/login");
  }

  if (session === undefined) {
    return (
      <div className="grid h-screen place-items-center">
        <div className="h-6 w-6 animate-spin rounded-full border-2 border-border border-t-accent" />
      </div>
    );
  }
  if (!session) return null;

  return (
    <SessionContext.Provider value={{ session, setSession }}>
      <div className="flex h-screen">
        <aside className="flex w-72 shrink-0 flex-col border-r border-border-soft px-5 py-7">
          <div className="mb-10 flex items-center gap-2.5 px-1">
            <MessagesSquare className="h-6 w-6 text-accent" strokeWidth={2.25} />
            <span className="text-lg font-semibold text-text">Chat Platform</span>
          </div>

          <nav className="flex flex-1 flex-col gap-1 overflow-y-auto">
            {NAV_ITEMS.map((item) => {
              const Icon = item.icon;
              const active = pathname?.startsWith(item.href);
              return (
                <a
                  key={item.href}
                  href={item.href}
                  className={cx(
                    "relative flex items-center gap-3 rounded-xl px-3.5 py-2.5 text-[15px] transition-colors duration-150",
                    active ? "text-accent font-medium" : "text-text-muted hover:text-text"
                  )}
                >
                  {active && (
                    <motion.span
                      layoutId="nav-active"
                      className="absolute inset-0 rounded-xl bg-accent-soft"
                      transition={{ type: "spring", stiffness: 500, damping: 40 }}
                    />
                  )}
                  <Icon className="relative h-5 w-5" strokeWidth={2} />
                  <span className="relative">{item.label}</span>
                </a>
              );
            })}
          </nav>

          {usage && <UsageGauge usage={usage} />}

          <div className="mt-5 flex flex-col gap-1 border-t border-border-soft pt-5">
            <div className="flex items-center gap-3 px-1 pb-2">
              <Avatar name={session.user.email} size="sm" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-text">{session.user.email}</div>
                <div className="truncate font-mono text-[11px] uppercase text-text-faint">{session.user.role}</div>
              </div>
            </div>
            <button
              onClick={signOut}
              className="flex items-center gap-3 rounded-xl px-3.5 py-2.5 text-[15px] text-text-muted transition-colors duration-150 hover:text-text"
            >
              <LogOut className="h-5 w-5" strokeWidth={2} />
              Sign out
            </button>
          </div>
        </aside>
        <div className="flex min-w-0 flex-1 flex-col">
          <header className="flex shrink-0 items-center justify-between border-b border-border-soft px-10 py-5">
            <div>
              <div className="text-[15px] font-semibold text-text">{session.org.name}</div>
              <div className="text-xs text-text-faint">Organization</div>
            </div>
            <Badge tone="accent">{session.org.tier}</Badge>
          </header>
          <main className="min-h-0 flex-1 overflow-y-auto px-10 py-9">{children}</main>
        </div>
      </div>
    </SessionContext.Provider>
  );
}

function UsageGauge({ usage }: { usage: Usage }) {
  const used = usage.apps.used;
  const limit = Math.max(usage.apps.limit, 1);
  const fraction = Math.min(1, used / limit);

  const size = 64;
  const stroke = 6;
  const radius = (size - stroke) / 2;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference * (1 - fraction);
  const nearLimit = fraction >= 0.8;

  return (
    <div className="relative flex items-center gap-3 rounded-xl border border-border-soft bg-surface-2/40 px-3 py-3">
      <svg width={size} height={size} className="shrink-0 -rotate-90">
        <circle cx={size / 2} cy={size / 2} r={radius} fill="none" stroke="var(--color-border)" strokeWidth={stroke} />
        <motion.circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          stroke={nearLimit ? "var(--color-warning)" : "var(--color-accent)"}
          strokeWidth={stroke}
          strokeLinecap="round"
          strokeDasharray={circumference}
          initial={{ strokeDashoffset: circumference }}
          animate={{ strokeDashoffset: offset }}
          transition={{ duration: 0.8, ease: [0.16, 1, 0.3, 1] }}
        />
      </svg>
      <div className="min-w-0">
        <div className="font-mono text-sm font-semibold text-text">
          {used} <span className="text-text-faint">/ {usage.apps.limit}</span>
        </div>
        <div className="text-[11px] text-text-faint">
          Apps on <span className="text-text-muted">{usage.tier}</span>
        </div>
        {nearLimit && usage.tier !== "ENTERPRISE" && <div className="mt-0.5 text-[11px] text-warning">Nearing your plan limit</div>}
      </div>
    </div>
  );
}
