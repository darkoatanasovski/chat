"use client";

import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useParams } from "next/navigation";
import { AnimatePresence, motion } from "framer-motion";
import {
  ArrowLeft,
  ArrowRight,
  CalendarClock,
  Check,
  ChevronDown,
  Copy,
  Eye,
  EyeOff,
  Hash,
  KeyRound,
  ListChecks,
  MessagesSquare,
  Plus,
  Send,
  ShieldOff,
  Terminal,
  Trash2,
  Users as UsersIcon,
  Users2,
  Vote,
  X,
} from "lucide-react";
import {
  addChannelMember,
  API_BASE,
  createCredential,
  createDashboardChannel,
  createEndUser,
  getAppMessagesUsage,
  getAppsMessagesDaily,
  listApps,
  listAppDashboardPolls,
  listChannelMembers,
  listCredentials,
  listDashboardBlocks,
  listDashboardChannels,
  listEndUsers,
  removeChannelMember,
  revealCredential,
  revokeCredential,
  updateApp,
  ApiError,
} from "@/lib/api";
import type {
  AppSummary,
  ChannelCapabilities,
  ChannelMember,
  Credential,
  DashboardBlock,
  DashboardChannel,
  DashboardPoll,
  EndUser,
  MessagesUsage,
  UpdateAppRequest,
} from "@/lib/types";
import { ConsoleShell, useSession } from "@/components/shell";
import { useToast } from "@/components/toast";
import {
  AnimatedNumber,
  Avatar,
  Badge,
  Button,
  ErrorBanner,
  formatLastSeen,
  Input,
  Label,
  Modal,
  Panel,
  Select,
  Skeleton,
  Sparkline,
  StatusDot,
  Switch,
} from "@/components/ui";

export default function AppDetailPage() {
  return (
    <ConsoleShell>
      <AppDetailView />
    </ConsoleShell>
  );
}

const TABS = [
  { id: "dashboard", label: "Dashboard" },
  { id: "credentials", label: "Credentials" },
  { id: "users", label: "End-users" },
  { id: "channels", label: "Channels" },
  { id: "blocks", label: "Blocks" },
  { id: "settings", label: "Settings" },
] as const;

type TabID = (typeof TABS)[number]["id"];

function AppDetailView() {
  const { session } = useSession();
  const params = useParams<{ id: string }>();
  const appId = Number(params.id);

  const [app, setApp] = useState<AppSummary | null>(null);
  const [tab, setTab] = useState<TabID>("dashboard");

  useEffect(() => {
    listApps(session.token, session.org.org_id)
      .then((apps) => setApp(apps.find((a) => a.app_id === appId) ?? null))
      .catch(() => {});
  }, [session.token, session.org.org_id, appId]);

  return (
    <div>
      <a href="/console/apps" className="mb-6 inline-flex items-center gap-1.5 text-[15px] text-text-muted transition-colors duration-150 hover:text-text">
        <ArrowLeft className="h-4 w-4" />
        Apps
      </a>

      <div className="mb-8 flex items-center gap-3.5">
        <span className="grid h-12 w-12 place-items-center rounded-full bg-accent-soft text-accent">
          <KeyRound className="h-5.5 w-5.5" />
        </span>
        <div>
          <h1 className="text-2xl font-semibold text-text">{app?.name ?? <Skeleton className="h-7 w-40" />}</h1>
          <p className="mt-0.5 font-mono text-xs text-text-faint">app_id: {appId}</p>
        </div>
      </div>

      <SdkSetupPanel appId={appId} />

      <div className="mb-7 flex items-center gap-1 border-b border-border-soft">
        {TABS.map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={`relative px-4 py-3 text-[15px] transition-colors duration-150 ${
              tab === t.id ? "text-accent font-medium" : "text-text-muted hover:text-text"
            }`}
          >
            {t.label}
            {tab === t.id && <motion.span layoutId="app-tab-active" className="absolute inset-x-0 -bottom-px h-0.5 rounded-full bg-accent" />}
          </button>
        ))}
      </div>

      {tab === "dashboard" && <DashboardTab appId={appId} />}
      {tab === "credentials" && <CredentialsTab appId={appId} />}
      {tab === "users" && <EndUsersTab appId={appId} />}
      {tab === "channels" && <ChannelsTab appId={appId} />}
      {tab === "blocks" && <BlocksTab appId={appId} />}
      {tab === "settings" && <SettingsTab appId={appId} app={app} onUpdated={setApp} />}
    </div>
  );
}

function regionLabel(region: string) {
  return { eu: "Europe", us: "North America", asia: "Asia Pacific" }[region] ?? region;
}

// ---- SDK setup ----

const SDK_LANGUAGES = [
  { id: "node", label: "Node.js", hasSdk: true },
  { id: "python", label: "Python", hasSdk: false },
  { id: "go", label: "Go", hasSdk: false },
  { id: "curl", label: "cURL", hasSdk: false },
] as const;

type SdkLanguage = (typeof SDK_LANGUAGES)[number]["id"];

// Per-app, not global — different apps in the same org can legitimately be
// built in different stacks, so the remembered language is keyed by appId.
const SDK_LANG_STORAGE_PREFIX = "chat-console:sdk-lang:";

function readStoredLanguage(appId: number): SdkLanguage | null {
  try {
    const stored = window.localStorage.getItem(`${SDK_LANG_STORAGE_PREFIX}${appId}`);
    return SDK_LANGUAGES.some((l) => l.id === stored) ? (stored as SdkLanguage) : null;
  } catch {
    // Private browsing / storage disabled — the picker still works, it
    // just won't be remembered on the next visit.
    return null;
  }
}

function writeStoredLanguage(appId: number, lang: SdkLanguage) {
  try {
    window.localStorage.setItem(`${SDK_LANG_STORAGE_PREFIX}${appId}`, lang);
  } catch {
    // See readStoredLanguage — non-fatal if storage isn't available.
  }
}

// The Node.js example uses our own SDK (@chat-platform/sdk) — its server
// module handles the app-token exchange (POST /apps/token) internally, so
// the example itself stays a two-step "mint a user, then act as them"
// story even though a third hop happens under the hood. No official SDK
// exists yet for the other languages, so those show the real HTTP calls,
// including that exchange explicitly.
function sdkSnippet(lang: SdkLanguage, apiBase: string, key: string, secret: string): string {
  switch (lang) {
    case "node":
      return `import { createServerClient } from "@chat-platform/sdk/server";
import { createChatClient } from "@chat-platform/sdk/client";

// 1. Server-side: mint a token for one of your end-users
const server = createServerClient({
  baseUrl: "${apiBase}",
  appCredentials: { key: "${key}", secret: "${secret}" },
});
const { token } = await server.endUsers.create({ display_name: "Jane Doe", region: "eu" });

// 2. Client-side: use that token to talk to the API
const client = createChatClient({ baseUrl: "${apiBase}", token });
await client.messages.send("CHANNEL_ID", crypto.randomUUID(), "Hello!");`;
    case "python":
      return `import base64, uuid, requests

# 1. Exchange your app's key+secret for a short-lived app token
auth = base64.b64encode(b"${key}:${secret}").decode()
app_token = requests.post(
    "${apiBase}/apps/token",
    headers={"Authorization": f"Basic {auth}"},
).json()["token"]

# 2. Server-side: mint a token for one of your end-users
res = requests.post(
    "${apiBase}/users",
    headers={"Authorization": f"Bearer {app_token}"},
    json={"display_name": "Jane Doe", "region": "eu"},
)
token = res.json()["token"]

# 3. Client-side: use that token to talk to the API
requests.post(
    "${apiBase}/channels/CHANNEL_ID/messages",
    headers={"Authorization": f"Bearer {token}"},
    json={"client_message_id": str(uuid.uuid4()), "body": "Hello!"},
)`;
    case "go":
      return `// 1. Exchange your app's key+secret for a short-lived app token
tokenReq, _ := http.NewRequest("POST", "${apiBase}/apps/token", nil)
tokenReq.SetBasicAuth("${key}", "${secret}")
tokenResp, _ := http.DefaultClient.Do(tokenReq)
var appToken struct{ Token string \`json:"token"\` }
json.NewDecoder(tokenResp.Body).Decode(&appToken)

// 2. Server-side: mint a token for one of your end-users
req, _ := http.NewRequest("POST", "${apiBase}/users",
    strings.NewReader(\`{"display_name":"Jane Doe","region":"eu"}\`))
req.Header.Set("Authorization", "Bearer "+appToken.Token)
req.Header.Set("Content-Type", "application/json")
resp, _ := http.DefaultClient.Do(req)

var created struct{ Token string \`json:"token"\` }
json.NewDecoder(resp.Body).Decode(&created)

// 3. Client-side: use that token to talk to the API
body := \`{"client_message_id":"\` + uuid.NewString() + \`","body":"Hello!"}\`
msgReq, _ := http.NewRequest("POST", "${apiBase}/channels/CHANNEL_ID/messages", strings.NewReader(body))
msgReq.Header.Set("Authorization", "Bearer "+created.Token)
msgReq.Header.Set("Content-Type", "application/json")
http.DefaultClient.Do(msgReq)`;
    case "curl":
      return `# 1. Exchange your app's key+secret for a short-lived app token
APP_TOKEN=$(curl -s -u ${key}:${secret} ${apiBase}/apps/token | jq -r .token)

# 2. Server-side: mint a token for one of your end-users
USER_TOKEN=$(curl -s -H "Authorization: Bearer $APP_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"display_name":"Jane Doe","region":"eu"}' \\
  ${apiBase}/users | jq -r .token)

# 3. Client-side: use that token to talk to the API
curl -H "Authorization: Bearer $USER_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"client_message_id":"'$(uuidgen)'","body":"Hello!"}' \\
  ${apiBase}/channels/CHANNEL_ID/messages`;
  }
}

// Shared by the top-of-page SDK setup panel (placeholder credentials) and
// the "new credential generated" modal (the real key, and the real secret
// while it's revealed) — same language tabs, same snippet renderer, same
// per-app remembered language, so switching languages in either place
// stays consistent with the other.
function SdkSnippetBlock({
  language,
  onLanguageChange,
  apiBase,
  credentialKey,
  credentialSecret,
}: {
  language: SdkLanguage;
  onLanguageChange: (lang: SdkLanguage) => void;
  apiBase: string;
  credentialKey: string;
  credentialSecret: string;
}) {
  const toast = useToast();
  const [copied, setCopied] = useState(false);
  const active = SDK_LANGUAGES.find((l) => l.id === language) ?? SDK_LANGUAGES[0];
  const code = sdkSnippet(language, apiBase, credentialKey, credentialSecret);

  function copySnippet() {
    navigator.clipboard
      .writeText(code)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => toast("error", "Couldn't copy automatically — select and copy the text manually."));
  }

  return (
    <div>
      <div className="mb-3 flex items-center gap-1.5">
        {SDK_LANGUAGES.map((l) => (
          <button
            key={l.id}
            onClick={() => onLanguageChange(l.id)}
            className={`rounded-lg px-3 py-1.5 text-[13px] transition-colors duration-150 ${
              language === l.id
                ? "bg-accent-soft font-medium text-accent"
                : "text-text-muted hover:bg-surface-2 hover:text-text"
            }`}
          >
            {l.label}
          </button>
        ))}
      </div>
      <div className="relative">
        <pre className="overflow-x-auto rounded-xl border border-border bg-surface-2 p-4 font-mono text-[13px] leading-relaxed text-text">
          <code>{code}</code>
        </pre>
        <button
          onClick={copySnippet}
          className="absolute right-3 top-3 rounded-lg border border-border bg-surface p-1.5 text-text-faint transition-colors duration-150 hover:text-text"
          title="Copy snippet"
        >
          {copied ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
        </button>
      </div>
      <p className="mt-3 text-xs text-text-faint">
        {active.hasSdk ? (
          <>
            Uses the official <code className="font-mono text-text-muted">@chat-platform/sdk</code> package.
          </>
        ) : (
          "No official SDK for this language yet — calls the REST API directly."
        )}
      </p>
    </div>
  );
}

function SdkSetupPanel({ appId }: { appId: number }) {
  const { session } = useSession();
  const [collapsed, setCollapsed] = useState(false);
  const [language, setLanguage] = useState<SdkLanguage>("node");
  // Set once the app's total-message count resolves, so a manual toggle
  // made while that's still loading doesn't get overridden afterward.
  const userToggledRef = useRef(false);

  useEffect(() => {
    const stored = readStoredLanguage(appId);
    if (stored) setLanguage(stored);
  }, [appId]);

  useEffect(() => {
    // "At least one request from the client" — reuse the same per-app
    // message totals the Apps grid and Overview page already fetch.
    getAppsMessagesDaily(session.token)
      .then((res) => {
        const total = res.apps.find((a) => a.app_id === appId)?.total ?? 0;
        if (!userToggledRef.current) setCollapsed(total > 0);
      })
      .catch(() => {});
  }, [session.token, appId]);

  function chooseLanguage(lang: SdkLanguage) {
    setLanguage(lang);
    writeStoredLanguage(appId, lang);
  }

  return (
    <div className="mb-7">
      <Panel animate={false}>
        <button
          onClick={() => {
            userToggledRef.current = true;
            setCollapsed((v) => !v);
          }}
          className="flex w-full items-center justify-between gap-3 text-left"
        >
          <div className="flex items-center gap-3">
            <span className="grid h-11 w-11 shrink-0 place-items-center rounded-full bg-accent-soft text-accent">
              <Terminal className="h-5 w-5" />
            </span>
            <div>
              <div className="text-[15px] font-medium text-text">SDK setup</div>
              <div className="text-sm text-text-faint">Mint an end-user token and send your first message</div>
            </div>
          </div>
          <motion.span animate={{ rotate: collapsed ? 0 : 180 }} transition={{ duration: 0.2 }} className="text-text-faint">
            <ChevronDown className="h-4 w-4" />
          </motion.span>
        </button>

        <AnimatePresence initial={false}>
          {!collapsed && (
            <motion.div
              initial={{ height: 0, opacity: 0 }}
              animate={{ height: "auto", opacity: 1 }}
              exit={{ height: 0, opacity: 0 }}
              transition={{ duration: 0.25, ease: [0.16, 1, 0.3, 1] }}
              className="overflow-hidden"
            >
              <div className="mt-5 border-t border-border-soft pt-5">
                <SdkSnippetBlock
                  language={language}
                  onLanguageChange={chooseLanguage}
                  apiBase={API_BASE}
                  credentialKey="YOUR_APP_KEY"
                  credentialSecret="YOUR_APP_SECRET"
                />
                <p className="mt-3 text-xs text-text-faint">
                  Generate a credential below to get this example pre-filled with a real key and a
                  reveal-to-copy secret, and swap in a real{" "}
                  <code className="font-mono text-text-muted">CHANNEL_ID</code>.
                </p>
              </div>
            </motion.div>
          )}
        </AnimatePresence>
      </Panel>
    </div>
  );
}

// ---- Dashboard ----

// Matches the backend's dashboardDailyWindowDays (cmd/api/handlers_dashboard.go).
const DAILY_WINDOW = 7;

function DashboardTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const [usage, setUsage] = useState<MessagesUsage | null>(null);
  const [daily, setDaily] = useState<number[] | null>(null);
  const [userCount, setUserCount] = useState<number | null>(null);
  const [channelCount, setChannelCount] = useState<number | null>(null);
  const [blockedCount, setBlockedCount] = useState<number | null>(null);
  const [polls, setPolls] = useState<DashboardPoll[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getAppMessagesUsage(session.token, appId)
      .then(setUsage)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
    // getAppsMessagesDaily returns every app's breakdown in one call (it
    // backs the Apps grid's per-app sparklines) — pick out just this app's
    // series rather than adding a redundant single-app daily endpoint.
    getAppsMessagesDaily(session.token)
      .then((res) => {
        const mine = res.apps.find((a) => a.app_id === appId);
        setDaily(mine ? mine.daily : new Array(res.days.length).fill(0));
      })
      .catch(() => {});
    listEndUsers(session.token, appId)
      .then((users) => setUserCount(users.length))
      .catch(() => {});
    listDashboardChannels(session.token, appId)
      .then((channels) => setChannelCount(channels.length))
      .catch(() => {});
    listDashboardBlocks(session.token, appId)
      .then((blocks) => setBlockedCount(blocks.length))
      .catch(() => {});
    listAppDashboardPolls(session.token, appId)
      .then(setPolls)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }, [session.token, appId]);

  return (
    <div>
      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <div className="mb-4 grid grid-cols-1 gap-4 sm:grid-cols-3">
        <MiniStatCard index={0} icon={<UsersIcon className="h-5 w-5" />} label="End-users" value={userCount} />
        <MiniStatCard index={1} icon={<Hash className="h-5 w-5" />} label="Channels" value={channelCount} />
        <MiniStatCard index={2} icon={<ShieldOff className="h-5 w-5" />} label="Blocked" value={blockedCount} tone="danger" />
      </div>

      <div className="mb-6">
        <AppMessagesCard usage={usage} daily={daily} />
      </div>

      <Panel animate={false}>
        <h2 className="mb-5 text-base font-semibold text-text">Polls</h2>
        {polls === null && !error && (
          <div className="flex flex-col gap-4">
            <Skeleton className="h-32" />
            <Skeleton className="h-32" />
          </div>
        )}
        {polls?.length === 0 && (
          <div className="flex flex-col items-center gap-2 py-8 text-center">
            <span className="grid h-10 w-10 place-items-center rounded-full bg-surface-2 text-text-faint">
              <Vote className="h-4.5 w-4.5" />
            </span>
            <p className="text-[15px] text-text-muted">No polls yet — they&apos;ll show up here once an end-user creates one.</p>
          </div>
        )}
        {polls && polls.length > 0 && (
          <div className="flex flex-col gap-4">
            {polls.map((p, i) => (
              <PollCard key={p.poll_id} poll={p} delay={Math.min(i * 40, 240)} />
            ))}
          </div>
        )}
      </Panel>
    </div>
  );
}

function MiniStatCard({
  index,
  icon,
  label,
  value,
  tone,
}: {
  index: number;
  icon: ReactNode;
  label: string;
  value: number | null;
  tone?: "accent" | "danger";
}) {
  const danger = tone === "danger";
  return (
    <motion.div
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ delay: index * 0.06, duration: 0.3, ease: [0.16, 1, 0.3, 1] }}
    >
      <Panel animate={false} className="transition-colors duration-150 hover:border-accent/25">
        <div className="mb-4 flex items-center gap-3">
          <span
            className={`grid h-11 w-11 shrink-0 place-items-center rounded-full ${
              danger ? "bg-danger-soft text-danger" : "bg-accent-soft text-accent"
            }`}
          >
            {icon}
          </span>
          <span className="text-[13px] font-medium uppercase tracking-wide text-text-muted">{label}</span>
        </div>
        <AnimatedNumber value={value ?? 0} className="text-4xl font-semibold text-text" />
      </Panel>
    </motion.div>
  );
}

function AppMessagesCard({ usage, daily }: { usage: MessagesUsage | null; daily: number[] | null }) {
  const [expanded, setExpanded] = useState(false);
  const today = daily ? daily[daily.length - 1] : 0;

  return (
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
      <div className="mt-5 flex flex-col gap-6 sm:flex-row sm:items-center">
        <div className="shrink-0">
          <div className="flex items-baseline gap-1.5">
            <AnimatedNumber value={usage?.total ?? 0} className="text-4xl font-semibold text-text" />
          </div>
          <div className="mt-1.5 text-sm text-text-faint">sent for this app</div>
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
  );
}

function formatPollDate(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

function PollCard({ poll, delay }: { poll: DashboardPoll; delay: number }) {
  const totalVotes = poll.options.reduce((sum, o) => sum + o.vote_count, 0);
  const closed = !!poll.closes_at && new Date(poll.closes_at).getTime() < Date.now();
  const leadingCount = Math.max(0, ...poll.options.map((o) => o.vote_count));

  return (
    <motion.div initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: delay / 1000, duration: 0.25 }}>
      <Panel animate={false} className="flex flex-col gap-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-[16px] font-medium leading-snug text-text">{poll.question}</p>
            <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[13px] text-text-faint">
              <span className="inline-flex items-center gap-1">
                <Hash className="h-3.5 w-3.5" />
                {poll.channel_name}
              </span>
              <span>&middot;</span>
              <span className="inline-flex items-center gap-1">
                <CalendarClock className="h-3.5 w-3.5" />
                {formatPollDate(poll.created_at)}
              </span>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {poll.multi_select && (
              <Badge tone="accent" icon={<ListChecks className="h-3 w-3" />}>
                multi-select
              </Badge>
            )}
            <Badge tone={closed ? "danger" : "success"}>{closed ? "closed" : "open"}</Badge>
          </div>
        </div>

        <div className="flex flex-col gap-2.5">
          {poll.options.map((opt) => {
            const share = totalVotes > 0 ? Math.round((opt.vote_count / totalVotes) * 100) : 0;
            const isLeading = totalVotes > 0 && opt.vote_count === leadingCount;
            return (
              <div key={opt.option_id}>
                <div className="mb-1 flex items-baseline justify-between gap-3 text-[14px]">
                  <span className={isLeading ? "font-medium text-text" : "text-text-muted"}>{opt.label}</span>
                  <span className="shrink-0 font-mono text-[12.5px] text-text-faint">
                    {opt.vote_count} &middot; {share}%
                  </span>
                </div>
                <div className="h-1.5 overflow-hidden rounded-full bg-surface-2">
                  <div
                    className={isLeading ? "h-full rounded-full bg-accent" : "h-full rounded-full bg-text-faint/50"}
                    style={{ width: `${share}%` }}
                  />
                </div>
              </div>
            );
          })}
        </div>

        <div className="flex items-center gap-1.5 text-[13px] text-text-faint">
          <Users2 className="h-3.5 w-3.5" />
          {poll.total_voters} {poll.total_voters === 1 ? "voter" : "voters"}
          {poll.closes_at && !closed && <span>&middot; closes {formatPollDate(poll.closes_at)}</span>}
        </div>
      </Panel>
    </motion.div>
  );
}

// ---- Credentials ----

function CredentialsTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const toast = useToast();
  const [credentials, setCredentials] = useState<Credential[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [newCredential, setNewCredential] = useState<Credential | null>(null);
  const [revoking, setRevoking] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);
  const [secretRevealed, setSecretRevealed] = useState(false);
  // Revealed secrets are cached per credential_id once fetched, so toggling
  // visibility back on for the same row doesn't re-hit the API — but each
  // one only ever gets fetched after an explicit click, never eagerly for
  // the whole list.
  const [revealedSecrets, setRevealedSecrets] = useState<Record<string, string>>({});
  const [revealingId, setRevealingId] = useState<string | null>(null);
  const [visibleSecretId, setVisibleSecretId] = useState<string | null>(null);
  const [snippetLanguage, setSnippetLanguage] = useState<SdkLanguage>("node");

  useEffect(() => {
    const stored = readStoredLanguage(appId);
    if (stored) setSnippetLanguage(stored);
  }, [appId]);

  function chooseSnippetLanguage(lang: SdkLanguage) {
    setSnippetLanguage(lang);
    writeStoredLanguage(appId, lang);
  }

  function refresh() {
    listCredentials(session.token, appId)
      .then(setCredentials)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }

  useEffect(refresh, [session.token, appId]);

  async function handleCreateCredential() {
    setError(null);
    try {
      const cred = await createCredential(session.token, appId);
      setNewCredential(cred);
      setSecretRevealed(false);
      refresh();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  async function handleRevoke(credentialId: string) {
    setRevoking(credentialId);
    try {
      await revokeCredential(session.token, appId, credentialId);
      toast("success", "Credential revoked");
      refresh();
    } catch (err) {
      toast("error", err instanceof ApiError ? err.message : String(err));
    } finally {
      setRevoking(null);
    }
  }

  async function handleToggleReveal(credentialId: string) {
    if (visibleSecretId === credentialId) {
      setVisibleSecretId(null);
      return;
    }
    if (revealedSecrets[credentialId] !== undefined) {
      setVisibleSecretId(credentialId);
      return;
    }
    setRevealingId(credentialId);
    try {
      const { secret } = await revealCredential(session.token, appId, credentialId);
      setRevealedSecrets((prev) => ({ ...prev, [credentialId]: secret }));
      setVisibleSecretId(credentialId);
    } catch (err) {
      toast("error", err instanceof ApiError ? err.message : String(err));
    } finally {
      setRevealingId(null);
    }
  }

  function copy(text: string, id: string) {
    navigator.clipboard
      .writeText(text)
      .then(() => {
        setCopied(id);
        setTimeout(() => setCopied(null), 1500);
      })
      .catch(() => toast("error", "Couldn't copy automatically — select and copy the text manually."));
  }

  return (
    <div>
      <div className="mb-5 flex justify-end">
        <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={handleCreateCredential}>
          Generate credential
        </Button>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <Panel animate={false}>
        {credentials === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        {credentials?.length === 0 && <EmptyState text="No credentials yet — generate one above." />}
        <div className="flex flex-col gap-2">
          {credentials?.map((c, i) => {
            const active = !c.revoked_at;
            const secretVisible = visibleSecretId === c.credential_id;
            return (
              <motion.div
                key={c.credential_id}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.04 }}
                className="flex flex-col gap-2 rounded-xl border border-border-soft px-4 py-3.5"
              >
                <div className="flex items-center gap-4">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <code className="truncate font-mono text-[13px] text-text">{c.key}</code>
                      <button
                        onClick={() => copy(c.key, c.credential_id)}
                        className="shrink-0 text-text-faint transition-colors duration-150 hover:text-text"
                        title="Copy key"
                      >
                        {copied === c.credential_id ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
                      </button>
                    </div>
                    <div className="mt-1 text-xs text-text-faint">
                      Created {new Date(c.created_at).toLocaleDateString()}
                      {c.revoked_at && ` · Revoked ${new Date(c.revoked_at).toLocaleDateString()}`}
                    </div>
                  </div>
                  <Badge tone={active ? "success" : "default"}>{active ? "active" : "revoked"}</Badge>
                  <Button
                    variant="ghost"
                    className="px-2.5"
                    loading={revealingId === c.credential_id}
                    onClick={() => handleToggleReveal(c.credential_id)}
                    icon={secretVisible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                    title={secretVisible ? "Hide secret" : "Reveal secret"}
                  />
                  {active && (
                    <Button
                      variant="ghost"
                      className="px-2.5 text-danger hover:bg-danger-soft"
                      loading={revoking === c.credential_id}
                      onClick={() => handleRevoke(c.credential_id)}
                      icon={<Trash2 className="h-4 w-4" />}
                    />
                  )}
                </div>
                {secretVisible && (
                  <div className="flex items-center gap-2 rounded-lg border border-border-soft bg-surface-2 px-3 py-2">
                    <code className="flex-1 truncate font-mono text-[13px] text-text">
                      {revealedSecrets[c.credential_id]}
                    </code>
                    <button
                      onClick={() => copy(revealedSecrets[c.credential_id] ?? "", `${c.credential_id}-secret`)}
                      className="shrink-0 text-text-faint transition-colors duration-150 hover:text-text"
                      title="Copy secret"
                    >
                      {copied === `${c.credential_id}-secret` ? (
                        <Check className="h-4 w-4 text-success" />
                      ) : (
                        <Copy className="h-4 w-4" />
                      )}
                    </button>
                  </div>
                )}
              </motion.div>
            );
          })}
        </div>
      </Panel>

      <Modal
        open={newCredential !== null}
        onClose={() => setNewCredential(null)}
        title="New credential generated"
        icon={<KeyRound className="h-4 w-4 text-accent" />}
        widthClass="max-w-lg"
      >
        {newCredential && (
          <div className="flex flex-col gap-4">
            <ErrorBanner>
              <span className="text-text">
                This secret is shown <strong>once</strong> — copy it now.
              </span>
            </ErrorBanner>
            <div>
              <Label>API key</Label>
              <div className="flex items-center gap-2">
                <code className="flex-1 truncate rounded-xl border border-border bg-surface-2 px-3.5 py-2.5 font-mono text-[13px] text-text">
                  {newCredential.key}
                </code>
                <Button
                  variant="secondary"
                  onClick={() => copy(newCredential.key, "new-key")}
                  icon={copied === "new-key" ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
                />
              </div>
            </div>
            <div>
              <Label>API secret</Label>
              <div className="flex items-center gap-2">
                <code className="flex-1 truncate rounded-xl border border-border bg-surface-2 px-3.5 py-2.5 font-mono text-[13px] text-text">
                  {secretRevealed ? newCredential.secret : "•".repeat(28)}
                </code>
                <Button
                  variant="secondary"
                  onClick={() => setSecretRevealed((v) => !v)}
                  icon={secretRevealed ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                  title={secretRevealed ? "Hide secret" : "Reveal secret"}
                />
                <Button
                  variant="secondary"
                  onClick={() => copy(newCredential.secret ?? "", "new-secret")}
                  icon={copied === "new-secret" ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
                />
              </div>
            </div>
            <div className="border-t border-border-soft pt-4">
              <Label>Quick start</Label>
              <div className="mt-2">
                <SdkSnippetBlock
                  language={snippetLanguage}
                  onLanguageChange={chooseSnippetLanguage}
                  apiBase={API_BASE}
                  credentialKey={newCredential.key}
                  credentialSecret={secretRevealed ? (newCredential.secret ?? "") : "•".repeat(28)}
                />
              </div>
              {!secretRevealed && (
                <p className="mt-2 text-xs text-text-faint">Reveal the secret above to copy a ready-to-run example.</p>
              )}
            </div>
            <Button variant="primary" className="justify-center" onClick={() => setNewCredential(null)}>
              Done
            </Button>
          </div>
        )}
      </Modal>
    </div>
  );
}

// ---- End-users ----

function EndUsersTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const toast = useToast();
  const [users, setUsers] = useState<EndUser[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  function refresh() {
    listEndUsers(session.token, appId)
      .then(setUsers)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }

  useEffect(refresh, [session.token, appId]);

  return (
    <div>
      <div className="mb-5 flex justify-end">
        <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={() => setCreateOpen(true)}>
          Add end-user
        </Button>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <Panel animate={false}>
        {users === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        {users?.length === 0 && <EmptyState text="No end-users yet — add one to start testing channels." />}
        <div className="flex flex-col gap-2">
          {users?.map((u, i) => (
            <motion.div
              key={u.user_id}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.03 }}
              className="flex items-center gap-3.5 rounded-xl border border-border-soft px-4 py-3.5"
            >
              <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-surface-2 text-sm font-semibold text-text-muted">
                {u.display_name.slice(0, 1).toUpperCase()}
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[15px] text-text">{u.display_name}</div>
                <div className="mt-0.5 font-mono text-xs text-text-faint">{u.user_id}</div>
              </div>
              <span className="hidden items-center gap-1.5 text-xs text-text-faint sm:flex">
                <StatusDot online={u.status.is_online} ringClassName="ring-surface" />
                {formatLastSeen(u.status)}
              </span>
              <Badge>{u.region.toUpperCase()}</Badge>
              <span className="hidden text-sm text-text-faint sm:inline">{new Date(u.created_at).toLocaleDateString()}</span>
            </motion.div>
          ))}
        </div>
      </Panel>

      <CreateEndUserModal
        appId={appId}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          refresh();
          toast("success", "End-user added");
        }}
      />
    </div>
  );
}

function CreateEndUserModal({
  appId,
  open,
  onClose,
  onCreated,
}: {
  appId: number;
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { session } = useSession();
  const [displayName, setDisplayName] = useState("");
  const [region, setRegion] = useState("eu");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setDisplayName("");
    setRegion("eu");
    setError(null);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await createEndUser(session.token, appId, displayName.trim(), region);
      onCreated();
      onClose();
      setTimeout(reset, 200);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={() => {
        onClose();
        setTimeout(reset, 200);
      }}
      title="Add an end-user"
      icon={<UsersIcon className="h-4 w-4 text-accent" />}
      widthClass="max-w-md"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div>
          <Label>Display name</Label>
          <Input required autoFocus value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="Jane Doe" />
        </div>
        <div>
          <Label>Region</Label>
          <Select value={region} onChange={(e) => setRegion(e.target.value)}>
            <option value="eu">Europe</option>
            <option value="us">North America</option>
            <option value="asia">Asia Pacific</option>
          </Select>
        </div>
        <Button type="submit" variant="primary" loading={loading} className="justify-center">
          Add end-user
        </Button>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  );
}

// ---- Channels ----

function ChannelsTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const toast = useToast();
  const [channels, setChannels] = useState<DashboardChannel[] | null>(null);
  const [users, setUsers] = useState<EndUser[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [activeChannel, setActiveChannel] = useState<DashboardChannel | null>(null);

  function refresh() {
    listDashboardChannels(session.token, appId)
      .then(setChannels)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
    listEndUsers(session.token, appId)
      .then(setUsers)
      .catch(() => {});
  }

  useEffect(refresh, [session.token, appId]);

  return (
    <div>
      <div className="mb-5 flex justify-end">
        <Button
          variant="primary"
          icon={<Plus className="h-4 w-4" />}
          disabled={users !== null && users.length === 0}
          onClick={() => setCreateOpen(true)}
        >
          Create channel
        </Button>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      {users?.length === 0 && (
        <div className="mb-5">
          <ErrorBanner>
            <span className="text-text">Add at least one end-user first — every channel needs an end-user as its creator.</span>
          </ErrorBanner>
        </div>
      )}

      <Panel animate={false}>
        {channels === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        {channels?.length === 0 && <EmptyState text="No channels yet — create one to start testing message delivery." />}
        <div className="flex flex-col gap-2">
          {channels?.map((c, i) => (
            <motion.button
              key={c.channel_id}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.03 }}
              onClick={() => setActiveChannel(c)}
              className="flex items-center gap-3.5 rounded-xl border border-border-soft px-4 py-3.5 text-left transition-colors duration-150 hover:border-accent/30"
            >
              <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-accent-soft text-accent">
                <Hash className="h-4 w-4" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[15px] text-text">{c.name}</div>
                <div className="mt-0.5 text-xs text-text-faint">
                  Created by {c.creator_name} · {regionLabel(c.home_region)}
                </div>
              </div>
              <div className="flex items-center gap-1.5 text-text-muted">
                <UsersIcon className="h-4 w-4" />
                <span className="font-mono text-[15px] text-text">{c.member_count}</span>
              </div>
            </motion.button>
          ))}
        </div>
      </Panel>

      <CreateChannelModal
        appId={appId}
        users={users ?? []}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          refresh();
          toast("success", "Channel created");
        }}
      />

      <ManageMembersModal
        channel={activeChannel}
        appUsers={users ?? []}
        onClose={() => setActiveChannel(null)}
        onChanged={refresh}
      />
    </div>
  );
}

function CreateChannelModal({
  appId,
  users,
  open,
  onClose,
  onCreated,
}: {
  appId: number;
  users: EndUser[];
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { session } = useSession();
  const [name, setName] = useState("");
  const [creatorId, setCreatorId] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setName("");
    setCreatorId("");
    setError(null);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await createDashboardChannel(session.token, appId, name.trim(), creatorId || users[0]?.user_id || "");
      onCreated();
      onClose();
      setTimeout(reset, 200);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={() => {
        onClose();
        setTimeout(reset, 200);
      }}
      title="Create a channel"
      icon={<Hash className="h-4 w-4 text-accent" />}
      widthClass="max-w-md"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div>
          <Label>Channel name</Label>
          <Input required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="general" />
        </div>
        <div>
          <Label>Creator</Label>
          <Select value={creatorId} onChange={(e) => setCreatorId(e.target.value)}>
            {users.map((u) => (
              <option key={u.user_id} value={u.user_id}>
                {u.display_name}
              </option>
            ))}
          </Select>
        </div>
        <Button type="submit" variant="primary" loading={loading} className="justify-center">
          Create channel
        </Button>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  );
}

function ManageMembersModal({
  channel,
  appUsers,
  onClose,
  onChanged,
}: {
  channel: DashboardChannel | null;
  appUsers: EndUser[];
  onClose: () => void;
  onChanged: () => void;
}) {
  const { session } = useSession();
  const toast = useToast();
  const [members, setMembers] = useState<ChannelMember[] | null>(null);
  const [addUserId, setAddUserId] = useState("");
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  function refresh() {
    if (!channel) return;
    listChannelMembers(session.token, channel.channel_id)
      .then(setMembers)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }

  useEffect(() => {
    setMembers(null);
    setError(null);
    setAddUserId("");
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channel?.channel_id]);

  if (!channel) return null;

  const memberIds = new Set(members?.map((m) => m.user_id));
  const candidates = appUsers.filter((u) => !memberIds.has(u.user_id));

  async function handleAdd() {
    if (!channel || !addUserId) return;
    setAdding(true);
    setError(null);
    try {
      await addChannelMember(session.token, channel.channel_id, addUserId);
      setAddUserId("");
      refresh();
      onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setAdding(false);
    }
  }

  async function handleRemove(userId: string) {
    if (!channel) return;
    setRemoving(userId);
    try {
      await removeChannelMember(session.token, channel.channel_id, userId);
      refresh();
      onChanged();
    } catch (err) {
      toast("error", err instanceof ApiError ? err.message : String(err));
    } finally {
      setRemoving(null);
    }
  }

  return (
    <Modal open={channel !== null} onClose={onClose} title={`#${channel.name}`} icon={<Hash className="h-4 w-4 text-accent" />} widthClass="max-w-lg">
      <div className="flex flex-col gap-4">
        {error && <ErrorBanner>{error}</ErrorBanner>}

        {candidates.length > 0 && (
          <div className="flex items-center gap-2">
            <Select value={addUserId} onChange={(e) => setAddUserId(e.target.value)} className="flex-1">
              <option value="">Add an end-user…</option>
              {candidates.map((u) => (
                <option key={u.user_id} value={u.user_id}>
                  {u.display_name}
                </option>
              ))}
            </Select>
            <Button variant="secondary" loading={adding} disabled={!addUserId} onClick={handleAdd} icon={<Plus className="h-4 w-4" />} />
          </div>
        )}

        {members === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
          </div>
        )}
        <div className="flex flex-col gap-1.5">
          {members?.map((m) => (
            <div key={m.user_id} className="flex items-center gap-3 rounded-xl px-2 py-2">
              <span className="relative grid h-8 w-8 shrink-0 place-items-center rounded-full bg-surface-2 text-xs font-semibold text-text-muted">
                {m.display_name.slice(0, 1).toUpperCase()}
                <StatusDot online={m.status.is_online} ringClassName="ring-surface" className="absolute -bottom-0.5 -right-0.5" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[15px] text-text">{m.display_name}</div>
                <div className="text-xs text-text-faint">{formatLastSeen(m.status)}</div>
              </div>
              <button
                onClick={() => handleRemove(m.user_id)}
                disabled={removing === m.user_id}
                className="rounded-lg p-1.5 text-text-faint transition-colors duration-150 hover:bg-danger-soft hover:text-danger disabled:opacity-40"
                title="Remove from channel"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          ))}
        </div>
      </div>
    </Modal>
  );
}

// ---- Blocks ----

function BlocksTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const [blocks, setBlocks] = useState<DashboardBlock[] | null>(null);
  const [names, setNames] = useState<Map<string, string>>(new Map());
  const [error, setError] = useState<string | null>(null);

  function refresh() {
    listDashboardBlocks(session.token, appId)
      .then(setBlocks)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
    listEndUsers(session.token, appId)
      .then((users) => setNames(new Map(users.map((u) => [u.user_id, u.display_name]))))
      .catch(() => {});
  }

  useEffect(refresh, [session.token, appId]);

  return (
    <div>
      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <Panel animate={false}>
        {blocks === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        {blocks?.length === 0 && <EmptyState text="No blocks for this app." />}
        <div className="flex flex-col gap-2">
          {blocks?.map((b, i) => (
            <motion.div
              key={`${b.blocker_user_id}-${b.blocked_user_id}`}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.03 }}
              className="flex items-center gap-3.5 rounded-xl border border-border-soft px-4 py-3.5"
            >
              <BlockedUser userId={b.blocker_user_id} name={names.get(b.blocker_user_id)} />
              <ArrowRight className="h-4 w-4 shrink-0 text-text-faint" />
              <BlockedUser userId={b.blocked_user_id} name={names.get(b.blocked_user_id)} />
            </motion.div>
          ))}
        </div>
      </Panel>
    </div>
  );
}

function BlockedUser({ userId, name }: { userId: string; name: string | undefined }) {
  const label = name ?? `${userId.slice(0, 8)}…`;
  return (
    <div className="flex min-w-0 flex-1 items-center gap-2.5">
      <Avatar name={label} size="sm" />
      <span className="truncate text-[15px] text-text">{label}</span>
    </div>
  );
}

// ---- Settings ----

// CAPABILITY_GROUPS drives the "Channel Capabilities" panel — every key
// here must match internal/apps.ChannelCapabilities' json tags exactly
// (lib/types.ts's ChannelCapabilities interface mirrors that struct field
// for field). Grouped for readability only; the backend has no concept of
// these groupings, just one flat set of 19 booleans.
const CAPABILITY_GROUPS: { title: string; items: { key: keyof ChannelCapabilities; label: string; hint?: string }[] }[] = [
  {
    title: "Realtime events",
    items: [
      { key: "typing_events", label: "Typing Events", hint: "Broadcast typing.start / typing.stop over the socket." },
      { key: "read_events", label: "Read Events", hint: "Let clients mark a channel read." },
      { key: "connection_events", label: "Connection Events", hint: "Broadcast when a member connects or disconnects." },
      { key: "custom_events", label: "Custom Events", hint: "Let clients broadcast an arbitrary event of their own." },
      { key: "delivery_events", label: "Delivery Events", hint: "Stored — not yet wired into realtime delivery." },
    ],
  },
  {
    title: "Messaging",
    items: [
      { key: "reactions", label: "Reactions" },
      { key: "threads_and_replies", label: "Threads & Replies" },
      { key: "quotes", label: "Quotes" },
      { key: "uploads", label: "Uploads", hint: "Client-supplied attachment URLs — this platform doesn't host files itself." },
      { key: "url_enrichment", label: "URL Enrichment", hint: "Best-effort link preview fetched after send." },
      { key: "location_sharing", label: "Location Sharing" },
      { key: "polls", label: "Polls" },
      { key: "message_count", label: "Message Count", hint: "Stored — reply counts are always tracked regardless of this toggle." },
      { key: "strict_last_message_time", label: "Strict Last Message Time", hint: "Stored — inert until this platform has a system-message concept." },
    ],
  },
  {
    title: "Moderation & reminders",
    items: [
      { key: "mutes", label: "Mutes", hint: "Per-channel and one-directional — not enforced in delivery filtering." },
      { key: "pending_messages", label: "Pending Messages", hint: "New messages need approval before other members see them." },
      { key: "message_reminders", label: "Message Reminders" },
      { key: "unread_reminders", label: "Unread Reminders" },
      { key: "search", label: "Search", hint: "Simple substring search over message bodies." },
    ],
  },
];

// KNOWN_COMMANDS seeds the Commands panel's chip list — apps.App's own
// default (migrations/control/0012_channel_capabilities.sql). An app can
// still enable a command outside this set via the free-text field below;
// SettingsTab always renders the union of this list and whatever's
// currently enabled, so a custom command an owner already added stays
// visible even though it isn't one of these five.
const KNOWN_COMMANDS = ["giphy", "ban", "unban", "mute", "unmute"];

function SettingsTab({
  appId,
  app,
  onUpdated,
}: {
  appId: number;
  app: AppSummary | null;
  onUpdated: (app: AppSummary) => void;
}) {
  const { session } = useSession();
  const toast = useToast();
  // Which single field is currently mid-PATCH — disables just that control
  // (not the whole form) and doubles as the request key so two toggles
  // flipped in quick succession don't race each other's optimistic state.
  const [saving, setSaving] = useState<string | null>(null);
  const [maxLenDraft, setMaxLenDraft] = useState("");
  const [maxDepthDraft, setMaxDepthDraft] = useState("");
  const [newCommand, setNewCommand] = useState("");

  useEffect(() => {
    if (!app) return;
    setMaxLenDraft(String(app.max_message_length));
    setMaxDepthDraft(String(app.max_thread_depth));
  }, [app?.app_id, app?.max_message_length, app?.max_thread_depth]);

  async function patch(fieldKey: string, body: UpdateAppRequest) {
    setSaving(fieldKey);
    try {
      const updated = await updateApp(session.token, appId, body);
      onUpdated(updated);
    } catch (err) {
      toast("error", err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(null);
    }
  }

  function toggleCapability(key: keyof ChannelCapabilities, next: boolean) {
    patch(key, { channel_capabilities: { [key]: next } });
  }

  function toggleCommand(cmd: string) {
    if (!app) return;
    const next = app.enabled_commands.includes(cmd)
      ? app.enabled_commands.filter((c) => c !== cmd)
      : [...app.enabled_commands, cmd];
    patch("enabled_commands", { enabled_commands: next });
  }

  function addCustomCommand() {
    const cmd = newCommand.trim().toLowerCase();
    if (!cmd || !app || app.enabled_commands.includes(cmd)) return;
    patch("enabled_commands", { enabled_commands: [...app.enabled_commands, cmd] });
    setNewCommand("");
  }

  if (!app) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-56 w-full rounded-2xl" />
        <Skeleton className="h-40 w-full rounded-2xl" />
        <Skeleton className="h-32 w-full rounded-2xl" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {CAPABILITY_GROUPS.map((group) => (
        <Panel key={group.title}>
          <h3 className="mb-1 text-[15px] font-semibold text-text">{group.title}</h3>
          <p className="mb-4 text-sm text-text-faint">
            Every toggle here is read live from the next request onward — no restart or redeploy needed.
          </p>
          <div className="divide-y divide-border-soft">
            {group.items.map((item) => (
              <div key={item.key} className="flex items-center justify-between gap-4 py-3.5 first:pt-0 last:pb-0">
                <div className="min-w-0 pr-4">
                  <div className="text-[15px] text-text">{item.label}</div>
                  {item.hint && <div className="mt-0.5 text-xs text-text-faint">{item.hint}</div>}
                </div>
                <Switch
                  checked={app.channel_capabilities[item.key]}
                  disabled={saving === item.key}
                  onChange={(next) => toggleCapability(item.key, next)}
                  label={item.label}
                />
              </div>
            ))}
          </div>
        </Panel>
      ))}

      <Panel>
        <h3 className="mb-1 text-[15px] font-semibold text-text">Message editing & threads</h3>
        <p className="mb-4 text-sm text-text-faint">Applies to every send/edit for this app.</p>
        <div className="divide-y divide-border-soft">
          <div className="flex items-center justify-between gap-4 py-3.5 first:pt-0">
            <div>
              <div className="text-[15px] text-text">Message Editing</div>
              <div className="mt-0.5 text-xs text-text-faint">Let end-users edit their own messages.</div>
            </div>
            <Switch
              checked={app.message_edit_enabled}
              disabled={saving === "message_edit_enabled"}
              onChange={(next) => patch("message_edit_enabled", { message_edit_enabled: next })}
              label="Message Editing"
            />
          </div>
          <div className="flex items-center justify-between gap-4 py-3.5">
            <div>
              <div className="text-[15px] text-text">Maximum Message Length</div>
              <div className="mt-0.5 text-xs text-text-faint">Characters allowed per message body.</div>
            </div>
            <Input
              type="number"
              min={1}
              value={maxLenDraft}
              onChange={(e) => setMaxLenDraft(e.target.value)}
              onBlur={() => {
                const n = Number(maxLenDraft);
                if (Number.isFinite(n) && n > 0 && n !== app.max_message_length) {
                  patch("max_message_length", { max_message_length: n });
                } else {
                  setMaxLenDraft(String(app.max_message_length));
                }
              }}
              className="w-28 text-right"
            />
          </div>
          <div className="flex items-center justify-between gap-4 py-3.5 last:pb-0">
            <div>
              <div className="text-[15px] text-text">Maximum Thread Depth</div>
              <div className="mt-0.5 text-xs text-text-faint">0 means unlimited nesting.</div>
            </div>
            <Input
              type="number"
              min={0}
              value={maxDepthDraft}
              onChange={(e) => setMaxDepthDraft(e.target.value)}
              onBlur={() => {
                const n = Number(maxDepthDraft);
                if (Number.isFinite(n) && n >= 0 && n !== app.max_thread_depth) {
                  patch("max_thread_depth", { max_thread_depth: n });
                } else {
                  setMaxDepthDraft(String(app.max_thread_depth));
                }
              }}
              className="w-28 text-right"
            />
          </div>
        </div>
      </Panel>

      <Panel>
        <h3 className="mb-1 text-[15px] font-semibold text-text">Dynamic Partitioning</h3>
        <p className="mb-4 text-sm text-text-faint">
          Stored for parity with the platform's routing docs — every channel is already sharded regardless of this
          toggle, so flipping it doesn't change delivery behavior today.
        </p>
        <div className="flex items-center justify-between gap-4">
          <div className="text-[15px] text-text">Dynamic Partitioning</div>
          <Switch
            checked={app.dynamic_partitioning}
            disabled={saving === "dynamic_partitioning"}
            onChange={(next) => patch("dynamic_partitioning", { dynamic_partitioning: next })}
            label="Dynamic Partitioning"
          />
        </div>
      </Panel>

      <Panel>
        <h3 className="mb-1 text-[15px] font-semibold text-text">Commands</h3>
        <p className="mb-4 text-sm text-text-faint">Slash-commands surfaced by the composer.</p>
        <div className="flex flex-wrap gap-2">
          {Array.from(new Set([...KNOWN_COMMANDS, ...app.enabled_commands])).map((cmd) => {
            const active = app.enabled_commands.includes(cmd);
            return (
              <button
                key={cmd}
                onClick={() => toggleCommand(cmd)}
                disabled={saving === "enabled_commands"}
                className={`rounded-full border px-3.5 py-1.5 text-[13px] transition-colors duration-150 disabled:opacity-40 ${
                  active ? "border-accent/30 bg-accent-soft text-accent" : "border-border text-text-muted hover:text-text"
                }`}
              >
                /{cmd}
              </button>
            );
          })}
        </div>
        <div className="mt-4 flex items-center gap-2">
          <Input
            placeholder="custom-command"
            value={newCommand}
            onChange={(e) => setNewCommand(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addCustomCommand();
              }
            }}
            className="max-w-[220px]"
          />
          <Button variant="secondary" onClick={addCustomCommand} icon={<Plus className="h-4 w-4" />}>
            Add
          </Button>
        </div>
      </Panel>
    </div>
  );
}

// ---- shared ----

function EmptyState({ text }: { text: ReactNode }) {
  return <p className="py-6 text-center text-[15px] text-text-muted">{text}</p>;
}
