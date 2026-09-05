import type { Metadata } from "next";
import { Boxes, Check, FlaskConical, Globe2, Languages, MessagesSquare, Pin, Radio, ShieldCheck, ShieldOff, SlidersHorizontal, Users, Vote } from "lucide-react";
import { ChatMockup } from "@/components/landing/chat-mockup";
import DemoChat from "@/components/demo-chat";
import { PLANS } from "@/lib/plans";

const TITLE = "Real-time chat infrastructure, built to go global";
const DESCRIPTION =
  "Add multi-region, real-time chat to your product in an afternoon — channels and threads, reactions and polls, search, translation, moderation, and presence, delivered fast across Europe, North America, and Asia from a single API.";

export const metadata: Metadata = {
  title: TITLE,
  description: DESCRIPTION,
  openGraph: {
    title: TITLE,
    description: DESCRIPTION,
    siteName: "Chat Platform",
    type: "website",
  },
  twitter: {
    card: "summary",
    title: TITLE,
    description: DESCRIPTION,
  },
};

const JSON_LD = {
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  name: "Chat Platform",
  applicationCategory: "DeveloperApplication",
  operatingSystem: "Any",
  description: DESCRIPTION,
  offers: PLANS.filter((p) => p.tier !== "ENTERPRISE").map((p) => ({
    "@type": "Offer",
    name: p.name,
    price: p.price.replace("$", ""),
    priceCurrency: "USD",
  })),
};

const FEATURES = [
  { icon: Globe2, title: "Global by default", desc: "Every app spans EU, US, and Asia from the first message. Delivery routes through the region closest to the sender — no region picker, no per-region deploys." },
  { icon: Radio, title: "Real-time everything", desc: "A WebSocket gateway with typing indicators, online presence, read receipts, message reminders, and connect/disconnect events out of the box." },
  { icon: MessagesSquare, title: "Rich messaging", desc: "Threads and replies, quotes, edits, location sharing, and automatic link previews — with attachment fields on every message, ready to point at your own file storage." },
  { icon: Vote, title: "Reactions & polls", desc: "Eight built-in reactions and single- or multi-select polls you can attach to any message, with live tallies pushed to every member." },
  { icon: Pin, title: "Pins & bookmarks", desc: "Channel-shared pins everyone sees, plus private bookmark folders each user keeps to themselves." },
  { icon: Languages, title: "Search & translation", desc: "Full-text search across a channel's messages and on-demand translation into any language, cached per message so repeat reads are free." },
  { icon: ShieldOff, title: "Moderation & safety", desc: "Bidirectional blocking enforced at delivery time, per-channel mutes, and approve-before-send queues for pending messages." },
  { icon: SlidersHorizontal, title: "Per-app capabilities", desc: "Toggle around twenty chat features on or off per app, so each product exposes exactly the surface it needs — enforced server-side." },
  { icon: FlaskConical, title: "Interactive playground", desc: "Try every feature against your own app right from the console, as real end-users, and copy the exact SDK, fetch, or cURL snippet." },
] as const;

const PERSONAS = [
  { icon: Users, title: "Communities & marketplaces", desc: "Channels, membership, and moderation built in, so buyers and sellers can just talk." },
  { icon: Boxes, title: "Multi-tenant SaaS", desc: "One platform, many isolated apps — each with its own users, channels, and API keys." },
  { icon: Globe2, title: "Global products", desc: "Ship to every region at once. Messages route through whichever is closest to the sender." },
] as const;

const STEPS = [
  { title: "Create an app", desc: "Spin up an isolated chat instance with its own users, channels, and credentials in seconds." },
  { title: "Add users & channels", desc: "Through the REST API from your backend, or straight from the console while you're prototyping." },
  { title: "Ship real-time messaging", desc: "Delivery is already fast everywhere — nothing to tune, nothing to scale yourself." },
] as const;

export default function LandingPage() {
  return (
    <div className="min-h-screen">
      {/* Deliberately no session-based redirect here — the marketing page
          stays reachable at "/" even for a signed-in visitor (e.g. someone
          coming back to check pricing or share the link), matching
          LandingNav's plain "Log in" / "Get started" links rather than
          routing them straight into the console. */}
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(JSON_LD) }} />

      <LandingNav />

      <main>
        <section className="mx-auto grid max-w-6xl grid-cols-1 items-center gap-12 px-6 pb-20 pt-16 lg:grid-cols-2 lg:pt-24">
          <div>
            <h1 className="text-4xl font-semibold leading-[1.1] text-text sm:text-5xl">
              Real-time chat, built to go <span className="text-accent">global</span>
            </h1>
            <p className="mt-5 max-w-xl text-lg text-text-muted">{DESCRIPTION}</p>
            <div className="mt-8 flex flex-wrap items-center gap-3">
              <a
                href="/console/signup"
                className="inline-flex items-center justify-center gap-2 rounded-xl bg-accent px-5 py-3 text-[15px] font-semibold text-bg transition-colors duration-150 hover:bg-accent-strong"
              >
                Get started for free
              </a>
              <a
                href="#how-it-works"
                className="inline-flex items-center justify-center gap-2 rounded-xl border border-border px-5 py-3 text-[15px] text-text-muted transition-colors duration-150 hover:border-text-faint hover:text-text"
              >
                See how it works
              </a>
            </div>
          </div>
          <ChatMockup />
        </section>

        <section id="try" className="mx-auto max-w-6xl px-6 py-20">
          <h2 className="text-center text-2xl font-semibold text-text">Try it live</h2>
          <p className="mx-auto mt-3 max-w-xl text-center text-sm text-text-muted">
            Pick a name, join the shared Lobby, and test the real thing — messages, reactions,
            edits, and typing indicators, delivered over the live platform.
          </p>
          <div className="mt-10">
            <DemoChat />
          </div>
        </section>

        <TrustBar />

        <DottedDivider />

        <section className="mx-auto max-w-6xl px-6 py-20">
          <h2 className="text-center text-2xl font-semibold text-text">Built for products that talk back</h2>
          <div className="mt-12 grid grid-cols-1 gap-10 sm:grid-cols-3">
            {PERSONAS.map((p) => (
              <div key={p.title} className="flex flex-col items-center text-center">
                <span className="mb-4 grid h-14 w-14 place-items-center rounded-full bg-accent-soft text-accent">
                  <p.icon className="h-6 w-6" />
                </span>
                <h3 className="text-[15px] font-semibold uppercase tracking-wide text-text">{p.title}</h3>
                <p className="mt-2 max-w-xs text-sm text-text-muted">{p.desc}</p>
              </div>
            ))}
          </div>
        </section>

        <section id="how-it-works" className="mx-auto max-w-5xl px-6 py-20">
          <h2 className="text-center text-2xl font-semibold text-text">
            From zero to real-time chat in <span className="text-accent">3 simple steps</span>
          </h2>
          <div className="mt-14 grid grid-cols-1 gap-10 sm:grid-cols-3">
            {STEPS.map((s, i) => (
              <div key={s.title} className="relative flex flex-col items-center text-center">
                {i < STEPS.length - 1 && (
                  <div className="absolute left-1/2 top-6 hidden h-px w-full bg-border sm:block" style={{ transform: "translateX(50%)" }} />
                )}
                <span className="relative z-10 mb-5 grid h-12 w-12 place-items-center rounded-full border-2 border-accent bg-bg text-lg font-semibold text-accent">
                  {i + 1}
                </span>
                <h3 className="text-[15px] font-semibold text-text">{s.title}</h3>
                <p className="mt-2 max-w-xs text-sm text-text-muted">{s.desc}</p>
              </div>
            ))}
          </div>
        </section>

        <DottedDivider />

        <section id="features" className="mx-auto max-w-6xl px-6 py-20">
          <h2 className="text-center text-2xl font-semibold text-text">Everything you need to ship chat</h2>
          <div className="mt-12 grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-3">
            {FEATURES.map((f) => (
              <div key={f.title} className="rounded-2xl border border-border-soft p-6">
                <span className="mb-4 grid h-11 w-11 place-items-center rounded-full bg-accent-soft text-accent">
                  <f.icon className="h-5 w-5" />
                </span>
                <h3 className="text-[15px] font-semibold text-text">{f.title}</h3>
                <p className="mt-2 text-sm text-text-muted">{f.desc}</p>
              </div>
            ))}
          </div>
        </section>

        <ConsolePreview />

        <section id="pricing" className="mx-auto max-w-6xl px-6 py-20">
          <h2 className="text-center text-2xl font-semibold text-text">Simple, predictable pricing</h2>
          <p className="mx-auto mt-3 max-w-xl text-center text-[15px] text-text-muted">
            Every plan enforces its limits server-side — what you see here is what your app actually gets.
          </p>
          <div className="mt-12 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            {PLANS.map((plan) => (
              <div key={plan.tier} className="flex flex-col rounded-2xl border border-border-soft p-6">
                <h3 className="text-lg font-semibold text-text">{plan.name}</h3>
                <div className="mt-2 flex items-baseline gap-1">
                  <span className="text-3xl font-semibold text-text">{plan.price}</span>
                  {plan.period && <span className="text-sm text-text-faint">{plan.period}</span>}
                </div>
                <p className="mt-3 text-sm text-text-muted">{plan.tagline}</p>
                <ul className="mt-5 flex flex-1 flex-col gap-2.5">
                  {plan.features.map((f) => (
                    <li key={f} className="flex items-start gap-2 text-[13px] text-text-muted">
                      <Check className="mt-0.5 h-3.5 w-3.5 shrink-0 text-accent" />
                      {f}
                    </li>
                  ))}
                </ul>
                <a
                  href="/console/signup"
                  className="mt-6 inline-flex items-center justify-center gap-2 rounded-xl border border-border bg-surface-2 px-4 py-2.5 text-[15px] text-text transition-colors duration-150 hover:border-text-faint"
                >
                  {plan.tier === "ENTERPRISE" ? "Talk to us" : "Get started"}
                </a>
              </div>
            ))}
          </div>
        </section>

        <section className="mx-auto max-w-4xl px-6 py-24 text-center">
          <h2 className="text-3xl font-semibold text-text">Ready to build real-time chat?</h2>
          <p className="mx-auto mt-3 max-w-md text-[15px] text-text-muted">Create your first app and get a working API key in under two minutes.</p>
          <a
            href="/console/signup"
            className="mt-8 inline-flex items-center justify-center gap-2 rounded-xl bg-accent px-6 py-3.5 text-[15px] font-semibold text-bg transition-colors duration-150 hover:bg-accent-strong"
          >
            Get started for free
          </a>
        </section>
      </main>

      <LandingFooter />
    </div>
  );
}

function LandingNav() {
  return (
    <header className="border-b border-border-soft">
      <div className="mx-auto flex max-w-6xl items-center justify-between px-6 py-5">
        <a href="/" className="flex items-center gap-2.5">
          <MessagesSquare className="h-6 w-6 text-accent" strokeWidth={2.25} />
          <span className="text-lg font-semibold text-text">Chat Platform</span>
        </a>
        <nav className="flex items-center gap-6">
          <a href="#features" className="hidden text-[15px] text-text-muted transition-colors duration-150 hover:text-text sm:block">
            Features
          </a>
          <a href="#pricing" className="hidden text-[15px] text-text-muted transition-colors duration-150 hover:text-text sm:block">
            Pricing
          </a>
          <a href="/console/login" className="text-[15px] text-text-muted transition-colors duration-150 hover:text-text">
            Log in
          </a>
          <a
            href="/console/signup"
            className="inline-flex items-center justify-center rounded-xl bg-accent px-4 py-2 text-[15px] font-semibold text-bg transition-colors duration-150 hover:bg-accent-strong"
          >
            Get started
          </a>
        </nav>
      </div>
    </header>
  );
}

function LandingFooter() {
  return (
    <footer className="border-t border-border-soft">
      <div className="mx-auto flex max-w-6xl flex-col items-center gap-4 px-6 py-10 text-center sm:flex-row sm:justify-between sm:text-left">
        <a href="/" className="flex items-center gap-2.5">
          <MessagesSquare className="h-5 w-5 text-accent" strokeWidth={2.25} />
          <span className="text-[15px] font-semibold text-text">Chat Platform</span>
        </a>
        <nav className="flex items-center gap-6 text-sm text-text-muted">
          <a href="#features" className="transition-colors duration-150 hover:text-text">
            Features
          </a>
          <a href="#pricing" className="transition-colors duration-150 hover:text-text">
            Pricing
          </a>
          <a href="/console/login" className="transition-colors duration-150 hover:text-text">
            Log in
          </a>
          <a href="/console/signup" className="transition-colors duration-150 hover:text-text">
            Sign up
          </a>
        </nav>
        <span className="text-xs text-text-faint">© {new Date().getFullYear()} Chat Platform</span>
      </div>
    </footer>
  );
}

// A credibility strip under the hero — the compliance posture a B2B infra
// buyer scans for. Framed as the standards the platform is built around;
// swap "Built for" / labels for certified marks once audits complete.
function TrustBar() {
  const marks = ["SOC 2", "GDPR", "HIPAA", "ISO 27001"] as const;
  return (
    <section className="mx-auto max-w-6xl px-6 pb-8">
      <div className="flex flex-col items-center gap-4">
        <span className="inline-flex items-center gap-2 text-[11px] font-medium uppercase tracking-wide text-text-faint">
          <ShieldCheck className="h-3.5 w-3.5 text-accent" />
          Built for compliance from day one
        </span>
        <div className="flex flex-wrap items-center justify-center gap-2.5">
          {marks.map((m) => (
            <span
              key={m}
              className="rounded-full border border-border-soft bg-surface px-4 py-1.5 text-[13px] font-medium text-text-muted"
            >
              {m}
            </span>
          ))}
        </div>
      </div>
    </section>
  );
}

function DottedDivider() {
  const cols = 24;
  const rows = 3;
  return (
    <div className="mx-auto grid max-w-6xl gap-3 px-6 opacity-40" style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }} aria-hidden="true">
      {Array.from({ length: cols * rows }).map((_, i) => (
        <span key={i} className="mx-auto h-1 w-1 rounded-full bg-text-faint" />
      ))}
    </div>
  );
}

// A static, honest preview of the real console (not a stock screenshot) —
// same left-nav shape and stat-tile pattern as components/shell.tsx and
// app/overview/page.tsx, just non-interactive.
function ConsolePreview() {
  return (
    <section className="mx-auto max-w-5xl px-6 py-8">
      <div className="overflow-hidden rounded-2xl border border-border bg-surface shadow-2xl">
        <div className="flex items-center gap-1.5 border-b border-border-soft px-4 py-3">
          <span className="h-2.5 w-2.5 rounded-full bg-danger/60" />
          <span className="h-2.5 w-2.5 rounded-full bg-warning/60" />
          <span className="h-2.5 w-2.5 rounded-full bg-success/60" />
        </div>
        <div className="flex">
          <div className="hidden w-48 shrink-0 flex-col gap-1 border-r border-border-soft p-4 sm:flex">
            {[
              { label: "Overview", active: true },
              { label: "Apps" },
              { label: "Playground" },
              { label: "Team" },
              { label: "Usage" },
              { label: "Billing" },
            ].map((item) => (
              <div
                key={item.label}
                className={`rounded-lg px-3 py-2 text-sm ${item.active ? "bg-accent-soft font-medium text-accent" : "text-text-muted"}`}
              >
                {item.label}
              </div>
            ))}
          </div>
          <div className="flex-1 p-6">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              {[
                { label: "Apps", value: "3 / 5" },
                { label: "End-users", value: "12,480" },
                { label: "Channels", value: "214" },
              ].map((stat) => (
                <div key={stat.label} className="rounded-xl border border-border-soft p-4">
                  <div className="text-[11px] font-medium uppercase tracking-wide text-text-faint">{stat.label}</div>
                  <div className="mt-2 font-mono text-2xl font-semibold text-text">{stat.value}</div>
                </div>
              ))}
            </div>
            <div className="mt-4 rounded-xl border border-border-soft p-4">
              <div className="text-[11px] font-medium uppercase tracking-wide text-text-faint">Delivery by region</div>
              <div className="mt-3 flex items-end gap-3">
                {[{ r: "EU", h: 70 }, { r: "US", h: 100 }, { r: "Asia", h: 55 }].map((b) => (
                  <div key={b.r} className="flex flex-1 flex-col items-center gap-2">
                    <div className="w-full rounded-md bg-accent/70" style={{ height: `${b.h * 0.5}px` }} />
                    <span className="text-[11px] text-text-faint">{b.r}</span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
