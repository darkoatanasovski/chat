// Scopes the pastel "console" theme (app/globals.css's `.console-theme`
// block) to everything under /console — the authenticated dashboard plus
// login/signup — without touching the marketing site's dark theme at
// app/page.tsx, which sits outside this segment and never picks up this
// class.
export default function ConsoleLayout({ children }: { children: React.ReactNode }) {
  return <div className="console-theme min-h-screen bg-bg text-text">{children}</div>;
}
