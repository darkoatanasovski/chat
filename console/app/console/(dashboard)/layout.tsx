// Renders the authenticated dashboard chrome (sidebar, header, session gate,
// usage gauge — see components/shell.tsx) ONCE, as a layout shared by every
// route in this group. Because a Next.js layout persists across navigations
// between its child routes — only the changed `page` segment re-renders —
// the shell no longer unmounts and remounts on every page change. That was
// the cause of the full-screen session spinner flashing on each navigation
// and the sidebar re-mounting: previously each page wrapped its own
// <ConsoleShell>, so the whole shell (and its session gate) tore down and
// rebuilt on every route change. React Query's cache already survives
// navigation (see components/query-provider.tsx), so with the shell kept
// mounted, cached data is simply reused and only refetched when it goes
// stale or a mutation invalidates it.
//
// This is a route group: the "(dashboard)" segment is ignored in the URL, so
// these pages keep their paths (/console/apps, /console/team, …). The
// unauthenticated pages (login, signup, invite) live outside this group, so
// they render under app/console/layout.tsx's theme wrapper WITHOUT the shell.
import { ConsoleShell } from "@/components/shell";

export default function DashboardLayout({ children }: { children: React.ReactNode }) {
  return <ConsoleShell>{children}</ConsoleShell>;
}
