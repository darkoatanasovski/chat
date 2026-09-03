import type { AppSummary } from "./types";
import { CAPABILITY_GROUPS, KNOWN_COMMANDS } from "./capabilities";

// SearchItem backs the global search palette (components/global-search.tsx).
// `href` always includes everything needed to land on the right screen in
// the right state: a bare page path for top-level nav, `?tab=` for one of
// an app's tabs, and `?tab=settings&setting=<key>` (or `?tab=dashboard&
// setting=polls`) for a specific setting — the app detail page reads
// `setting` back out to scroll to and briefly highlight that row (see
// SettingHighlight in components/ui.tsx).
export interface SearchItem {
  id: string;
  label: string;
  section: string;
  keywords: string;
  href: string;
  hint?: string;
}

const STATIC_PAGES: { id: string; label: string; href: string }[] = [
  { id: "page-overview", label: "Overview", href: "/console/overview" },
  { id: "page-apps", label: "Apps", href: "/console/apps" },
  { id: "page-team", label: "Team", href: "/console/team" },
  { id: "page-usage", label: "Usage", href: "/console/usage" },
  { id: "page-billing", label: "Billing", href: "/console/billing" },
];

// Mirrors the TABS array in app/console/apps/[id]/page.tsx. Not imported
// directly from there since that file is a page component ("use client"
// default export) — duplicating this small, stable list keeps the search
// index free of a page-to-page import.
const APP_TABS: { id: string; label: string }[] = [
  { id: "dashboard", label: "Dashboard" },
  { id: "credentials", label: "Credentials" },
  { id: "users", label: "End-users" },
  { id: "channels", label: "Channels" },
  { id: "blocks", label: "Blocks" },
  { id: "settings", label: "Settings" },
];

/** Pages that exist independent of any app — always in the index. */
export function buildStaticIndex(): SearchItem[] {
  return STATIC_PAGES.map((p) => ({
    id: p.id,
    label: p.label,
    section: "Pages",
    keywords: p.label.toLowerCase(),
    href: p.href,
  }));
}

/** Per-app tabs and settings — rebuilt whenever the org's app list changes. */
export function buildAppIndex(apps: AppSummary[]): SearchItem[] {
  const items: SearchItem[] = [];

  for (const app of apps) {
    const base = `/console/apps/${app.app_id}`;
    const tabSection = app.name;
    const settingsSection = `${app.name} · Settings`;

    for (const t of APP_TABS) {
      items.push({
        id: `app-${app.app_id}-tab-${t.id}`,
        label: t.label,
        section: tabSection,
        keywords: `${t.label} ${app.name} tab`.toLowerCase(),
        href: `${base}?tab=${t.id}`,
      });
    }

    // Polls live inside the Dashboard tab, not their own tab.
    items.push({
      id: `app-${app.app_id}-polls`,
      label: "Polls",
      section: tabSection,
      keywords: `polls polling ${app.name}`.toLowerCase(),
      href: `${base}?tab=dashboard&setting=polls`,
    });

    for (const group of CAPABILITY_GROUPS) {
      for (const cap of group.items) {
        items.push({
          id: `app-${app.app_id}-cap-${cap.key}`,
          label: cap.label,
          section: settingsSection,
          keywords: `${cap.label} ${cap.hint ?? ""} ${group.title} ${app.name}`.toLowerCase(),
          href: `${base}?tab=settings&setting=${cap.key}`,
          hint: cap.hint,
        });
      }
    }

    items.push(
      {
        id: `app-${app.app_id}-setting-message_edit_enabled`,
        label: "Message Editing",
        section: settingsSection,
        keywords: `message editing edit enabled ${app.name}`.toLowerCase(),
        href: `${base}?tab=settings&setting=message_edit_enabled`,
      },
      {
        id: `app-${app.app_id}-setting-max_message_length`,
        label: "Maximum Message Length",
        section: settingsSection,
        keywords: `maximum message length max chars limit ${app.name}`.toLowerCase(),
        href: `${base}?tab=settings&setting=max_message_length`,
      },
      {
        id: `app-${app.app_id}-setting-max_thread_depth`,
        label: "Maximum Thread Depth",
        section: settingsSection,
        keywords: `maximum thread depth nesting replies ${app.name}`.toLowerCase(),
        href: `${base}?tab=settings&setting=max_thread_depth`,
      },
      {
        id: `app-${app.app_id}-setting-dynamic_partitioning`,
        label: "Dynamic Partitioning",
        section: settingsSection,
        keywords: `dynamic partitioning sharding routing ${app.name}`.toLowerCase(),
        href: `${base}?tab=settings&setting=dynamic_partitioning`,
      },
      {
        id: `app-${app.app_id}-setting-commands`,
        label: "Commands",
        section: settingsSection,
        keywords: `commands slash-commands ${KNOWN_COMMANDS.join(" ")} ${app.name}`.toLowerCase(),
        href: `${base}?tab=settings&setting=commands`,
      }
    );
  }

  return items;
}
