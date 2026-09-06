"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, CheckCheck, FileText, Hash, Paperclip, Pencil, Plus, SendHorizontal, Smile } from "lucide-react";

// Live chat demo for the landing page. A visitor picks a username, the server
// route (/api/demo/session) mints them an end-user in the ENTERPRISE demo app
// and drops them in the shared Lobby, and this widget then exercises the real
// platform over the Cloudflare edge: send, react, edit, typing indicators, and
// read receipts. It's drawn in the landing theme's own tokens so it reads as a
// working product window next to the animated hero mockup — outgoing messages
// sit on the right with delivery ticks, everyone else on the left with an
// avatar, and a left sidebar lists the workspace's channels.

const API = process.env.NEXT_PUBLIC_API_BASE || "";
const WS = process.env.NEXT_PUBLIC_GATEWAY_BASE || "";

const REACTIONS: { key: string; glyph: string }[] = [
  { key: "like", glyph: "👍" },
  { key: "love", glyph: "❤️" },
  { key: "laugh", glyph: "😂" },
  { key: "celebrate", glyph: "🎉" },
  { key: "eyes", glyph: "👀" },
  { key: "rocket", glyph: "🚀" },
];
const glyph = (k: string) => REACTIONS.find((r) => r.key === k)?.glyph ?? k;

// Composer emoji picker — a broad, common set inserted into the draft.
const EMOJI_PICKER = [
  "😀", "😁", "😂", "🤣", "😊", "😍", "😎", "🤔",
  "👍", "👎", "🙏", "👏", "🙌", "🔥", "🎉", "💯",
  "❤️", "🧡", "💛", "💚", "💙", "💜", "✅", "❌",
  "⭐", "✨", "🚀", "👀", "😢", "😅", "🤝", "💡",
];

// The live demo runs in one shared channel; the rest of the list is here so
// the window reads like a real multi-channel workspace. Selecting a non-live
// channel shows a short placeholder rather than pretending to be wired up.
const CHANNELS = [
  { key: "general", name: "general", live: true },
  { key: "random", name: "random", live: false },
  { key: "introductions", name: "introductions", live: false },
  { key: "announcements", name: "announcements", live: false },
  { key: "help", name: "help", live: false },
  { key: "showcase", name: "showcase", live: false },
];

type Status = "sending" | "delivered" | "read";
type Reaction = { reaction: string; user_id: string };
type Attachment = { url: string; type?: string; filename?: string; size_bytes?: number };
type LinkPreview = { url: string; title?: string; description?: string; image_url?: string; site_name?: string };
type Message = {
  message_id: string;
  sender_id: string;
  body: string;
  created_at: string;
  sequence: number;
  edited_at?: string | null;
  reaction_counts: Record<string, number>;
  latest_reactions: Reaction[];
  attachments?: Attachment[];
  link_preview?: LinkPreview | null;
  cid?: string; // client id for optimistic sends, before the server echoes back
  pending?: boolean; // true while the POST is in flight
  k?: string; // stable render key for its whole lifecycle (optimistic → reconciled), so it never remounts/re-animates
};

const MAX_UPLOAD_BYTES = 5 * 1024 * 1024;
// True when a message's body is just our filename placeholder (a file sent
// with no caption), so we render the attachment alone without a text bubble.
function hasCaption(m: Message): boolean {
  if (!m.body) return false;
  if (m.attachments?.length === 1 && m.body === m.attachments[0].filename) return false;
  return true;
}
type Session = { token: string; userId: string; displayName: string; channelId: string };

const uuid = () => crypto.randomUUID();

// The visitor's minted session survives a page refresh so they come back as
// the same user; "leave" clears it. Wrapped in try/catch because storage can
// throw (private mode, storage disabled).
const STORAGE_KEY = "chat-demo-session";
function persistSession(s: Session) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(s));
  } catch {
    /* ignore — session just won't survive refresh */
  }
}
function readStoredSession(): Session | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const s = JSON.parse(raw) as Session;
    return s?.token && s?.userId && s?.channelId ? s : null;
  } catch {
    return null;
  }
}
function clearStoredSession() {
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* ignore */
  }
}

// Stable per-name hue so each participant keeps the same avatar colour, the
// same treatment the hero mockup uses.
function hueFor(seed: string): number {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) % 360;
  return h;
}
function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}
function timeOf(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }).toLowerCase();
}

function Avatar({ name, size = 28 }: { name: string; size?: number }) {
  const hue = hueFor(name || "?");
  return (
    <span
      className="grid shrink-0 select-none place-items-center rounded-full font-semibold text-white"
      style={{
        width: size,
        height: size,
        fontSize: size * 0.4,
        background: `linear-gradient(135deg, hsl(${hue} 70% 55%), hsl(${(hue + 30) % 360} 65% 42%))`,
      }}
      aria-hidden="true"
    >
      {initials(name)}
    </span>
  );
}

function Ticks({ status }: { status: Status }) {
  if (status === "sending") {
    return <Check className="h-3 w-3 text-text-faint" strokeWidth={2.5} />;
  }
  return (
    <CheckCheck
      className={`h-3 w-3 transition-colors duration-300 ${status === "read" ? "text-accent" : "text-text-faint"}`}
      strokeWidth={2.5}
    />
  );
}

function AttachmentView({ a, mine }: { a: Attachment; mine: boolean }) {
  const isImage = (a.type || "").startsWith("image/");
  if (isImage) {
    return (
      <a href={a.url} target="_blank" rel="noreferrer" className="mt-1 block">
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          src={a.url}
          alt={a.filename || "image"}
          className="max-h-52 max-w-full rounded-xl border border-border object-cover"
        />
      </a>
    );
  }
  return (
    <a
      href={a.url}
      target="_blank"
      rel="noreferrer"
      className={`mt-1 inline-flex items-center gap-2 rounded-xl border border-border px-3 py-2 text-xs transition-colors hover:border-accent ${
        mine ? "bg-bg text-text" : "bg-surface-2 text-text"
      }`}
    >
      <FileText className="h-4 w-4 shrink-0 text-text-muted" />
      <span className="max-w-[12rem] truncate">{a.filename || "file"}</span>
    </a>
  );
}

// Render message text with any http(s) URLs turned into clickable links, so a
// link stays reachable whether or not it has a preview card (or after the
// preview is removed). Color is inherited so it reads on both bubble grounds.
function linkify(text: string) {
  const re = /(https?:\/\/[^\s<>"']+)/g;
  const out: ReactNode[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const url = m[0];
    out.push(
      <a
        key={m.index}
        href={url}
        target="_blank"
        rel="noreferrer"
        onClick={(e) => e.stopPropagation()}
        className="underline underline-offset-2 hover:opacity-80"
      >
        {url}
      </a>,
    );
    last = m.index + url.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function LinkPreviewCard({ p, onRemove }: { p: LinkPreview; onRemove?: () => void }) {
  return (
    <a
      href={p.url}
      target="_blank"
      rel="noreferrer"
      className="demo-msg-in group/lp relative mt-1 block max-w-[18rem] overflow-hidden rounded-xl border border-border bg-bg transition-colors hover:border-accent"
    >
      {p.image_url && (
        // eslint-disable-next-line @next/next/no-img-element
        <img src={p.image_url} alt="" className="h-28 w-full object-cover" />
      )}
      <div className="px-3 py-2">
        {p.site_name && <div className="text-[10px] uppercase tracking-wide text-text-faint">{p.site_name}</div>}
        {p.title && <div className="mt-0.5 line-clamp-2 text-[13px] font-medium text-text">{p.title}</div>}
        {p.description && <div className="mt-0.5 line-clamp-2 text-[11px] text-text-muted">{p.description}</div>}
      </div>
      {onRemove && (
        <button
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onRemove();
          }}
          title="Remove preview"
          className="absolute right-1.5 top-1.5 grid h-5 w-5 place-items-center rounded-full bg-bg/80 text-xs text-text-muted opacity-0 transition-opacity hover:text-text group-hover/lp:opacity-100"
        >
          ×
        </button>
      )}
    </a>
  );
}

export default function DemoChat() {
  const [session, setSession] = useState<Session | null>(null);
  const [username, setUsername] = useState("");
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [messages, setMessages] = useState<Message[]>([]);
  const [names, setNames] = useState<Record<string, string>>({});
  const [reads, setReads] = useState<Record<string, number>>({}); // userId -> last read sequence
  const [typing, setTyping] = useState<Record<string, boolean>>({});
  const [status, setStatus] = useState<"connecting" | "open" | "closed">("connecting");
  const [draft, setDraft] = useState("");
  const [editing, setEditing] = useState<{ id: string; body: string } | null>(null);
  const [active, setActive] = useState("general");
  const [emojiOpen, setEmojiOpen] = useState(false);
  const [uploading, setUploading] = useState(false);

  const wsRef = useRef<WebSocket | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);
  const draftRef = useRef<HTMLInputElement | null>(null);
  const emojiRef = useRef<HTMLDivElement | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);
  const typingSentAt = useRef(0);
  const typingStopTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const typingClear = useRef<Record<string, ReturnType<typeof setTimeout>>>({});
  const markReadTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const authed = useCallback(
    (path: string, init: RequestInit = {}) =>
      fetch(API + path, {
        ...init,
        headers: {
          "content-type": "application/json",
          authorization: `Bearer ${session?.token}`,
          ...(init.headers || {}),
        },
      }),
    [session],
  );

  const loadMembers = useCallback(async (s: Session) => {
    try {
      const r = await fetch(`${API}/channels/${s.channelId}/members`, {
        headers: { authorization: `Bearer ${s.token}` },
      });
      if (!r.ok) return;
      const members: { user_id: string; display_name: string }[] = await r.json();
      setNames((prev) => {
        const next = { ...prev };
        for (const m of members) next[m.user_id] = m.display_name;
        return next;
      });
    } catch {
      /* names just fall back to "Someone" */
    }
  }, []);

  // Advance our own read watermark so other participants see our ticks turn
  // green. Best-effort: if the app doesn't have read events enabled this 4xxs
  // and we simply don't show read receipts. Debounced to once per burst.
  const markRead = useCallback(() => {
    if (!session) return;
    if (markReadTimer.current) clearTimeout(markReadTimer.current);
    markReadTimer.current = setTimeout(() => {
      authed(`/channels/${session.channelId}/read`, {
        method: "POST",
        body: JSON.stringify({}),
      }).catch(() => {});
    }, 600);
  }, [authed, session]);

  async function join() {
    const name = username.trim();
    if (!name) return;
    setJoining(true);
    setError(null);
    try {
      const r = await fetch("/api/demo/session", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ username: name }),
      });
      const data = await r.json();
      if (!r.ok) throw new Error(data.error || "could not join");
      const s: Session = data;
      persistSession(s);
      setActive("general");
      setSession(s); // the hydrate effect below loads history/members/read-state
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not join");
    } finally {
      setJoining(false);
    }
  }

  function leave() {
    clearStoredSession();
    setSession(null);
    setMessages([]);
    setNames({});
    setReads({});
    setTyping({});
    setEditing(null);
    setDraft("");
    setUsername("");
    setError(null);
  }

  // Restore a stored session on first mount so a refresh keeps the same user.
  useEffect(() => {
    const s = readStoredSession();
    if (s) {
      setActive("general");
      setNames((p) => ({ ...p, [s.userId]: s.displayName }));
      setSession(s);
    }
  }, []);

  // Whenever we have a session (fresh join or restored from storage), load its
  // history, members, and read watermarks. A stale/expired stored token surface
  // as a 401/403 here — clear it and drop back to the join screen.
  useEffect(() => {
    if (!session) return;
    const s = session;
    let cancelled = false;
    (async () => {
      try {
        const hist = await fetch(`${API}/channels/${s.channelId}/messages?limit=40`, {
          headers: { authorization: `Bearer ${s.token}` },
        });
        if (hist.status === 401 || hist.status === 403) {
          if (!cancelled) {
            clearStoredSession();
            setSession(null);
          }
          return;
        }
        if (hist.ok && !cancelled) {
          setMessages(((await hist.json()) as Message[]).slice().reverse());
        }
      } catch {
        /* offline / transient — WS reconnect + next load will recover */
      }
      if (cancelled) return;
      await loadMembers(s);
      try {
        const rs = await fetch(`${API}/channels/${s.channelId}/read-state`, {
          headers: { authorization: `Bearer ${s.token}` },
        });
        if (rs.ok && !cancelled) {
          const rows: { user_id: string; last_read_sequence: number }[] = await rs.json();
          setReads(Object.fromEntries(rows.map((row) => [row.user_id, row.last_read_sequence])));
        }
      } catch {
        /* ticks just stay "delivered" */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [session, loadMembers]);

  // WebSocket lifecycle
  useEffect(() => {
    if (!session) return;
    const ws = new WebSocket(`${WS}/connect?token=${encodeURIComponent(session.token)}`);
    wsRef.current = ws;
    setStatus("connecting");
    ws.onopen = () => setStatus("open");
    ws.onclose = () => setStatus("closed");
    ws.onerror = () => setStatus("closed");
    ws.onmessage = (evt) => {
      let f: Record<string, unknown>;
      try {
        f = JSON.parse(evt.data);
      } catch {
        return;
      }
      if (f.channel_id !== session.channelId) return;
      const type = f.type as string;

      if (type === "message.created") {
        const m: Message = {
          message_id: f.message_id as string,
          sender_id: f.sender_id as string,
          body: f.body as string,
          created_at: f.created_at as string,
          sequence: f.sequence as number,
          reaction_counts: {},
          latest_reactions: [],
          attachments: (f.attachments as Attachment[]) || [],
        };
        setMessages((prev) => {
          if (prev.some((x) => x.message_id === m.message_id)) return prev;
          // Reconcile with our own optimistic bubble if it's still pending —
          // keeping its stable render key so it doesn't remount/re-animate.
          const idx = prev.findIndex((x) => x.pending && x.sender_id === m.sender_id && x.body === m.body);
          if (idx >= 0) {
            const copy = [...prev];
            copy[idx] = { ...m, k: prev[idx].k };
            return copy;
          }
          return [...prev, { ...m, k: m.message_id }];
        });
        if (!((f.sender_id as string) in names)) loadMembers(session);
        if (f.sender_id !== session.userId) markRead();
      } else if (type === "message.edited") {
        setMessages((prev) =>
          prev.map((m) =>
            m.message_id === f.message_id ? { ...m, body: f.body as string, edited_at: f.edited_at as string } : m,
          ),
        );
      } else if (type === "link_preview.updated") {
        setMessages((prev) =>
          prev.map((m) =>
            m.message_id === f.message_id ? { ...m, link_preview: (f.link_preview as LinkPreview) || null } : m,
          ),
        );
      } else if (type === "reaction.updated") {
        setMessages((prev) =>
          prev.map((m) =>
            m.message_id === f.message_id
              ? {
                  ...m,
                  reaction_counts: f.reaction_counts as Record<string, number>,
                  latest_reactions: (f.latest_reactions as Reaction[]) || [],
                }
              : m,
          ),
        );
      } else if (type === "read.updated") {
        const uid = f.user_id as string;
        const seq = (f.last_read_sequence as number) || 0;
        setReads((prev) => (prev[uid] >= seq ? prev : { ...prev, [uid]: seq }));
        if (!(uid in names)) loadMembers(session);
      } else if (type === "typing.updated") {
        const uid = f.user_id as string;
        if (uid === session.userId) return;
        clearTimeout(typingClear.current[uid]);
        if (f.typing) {
          setTyping((p) => ({ ...p, [uid]: true }));
          typingClear.current[uid] = setTimeout(() => setTyping((p) => ({ ...p, [uid]: false })), 4000);
        } else {
          setTyping((p) => ({ ...p, [uid]: false }));
        }
        if (!(uid in names)) loadMembers(session);
      }
    };
    return () => ws.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session]);

  // autoscroll + mark the newest messages read
  useEffect(() => {
    listRef.current?.scrollTo({ top: listRef.current.scrollHeight });
    if (session && messages.length) markRead();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [messages, typing]);

  // close the emoji picker on an outside click
  useEffect(() => {
    if (!emojiOpen) return;
    const onDown = (e: MouseEvent) => {
      if (emojiRef.current && !emojiRef.current.contains(e.target as Node)) setEmojiOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [emojiOpen]);

  function insertEmoji(e: string) {
    setDraft((d) => d + e);
    setEmojiOpen(false);
    draftRef.current?.focus();
  }

  function sendTyping(start: boolean) {
    const ws = wsRef.current;
    if (ws?.readyState !== WebSocket.OPEN || !session) return;
    ws.send(JSON.stringify({ type: start ? "typing.start" : "typing.stop", channel_id: session.channelId }));
  }
  function onDraftChange(v: string) {
    setDraft(v);
    const now = Date.now();
    if (now - typingSentAt.current > 2000) {
      typingSentAt.current = now;
      sendTyping(true);
    }
    if (typingStopTimer.current) clearTimeout(typingStopTimer.current);
    typingStopTimer.current = setTimeout(() => sendTyping(false), 2500);
  }

  // Core send with an optimistic bubble (so ticks advance sending → delivered)
  // that reconciles against the server's echo. Body is required by the API, so
  // a file with no caption sends its filename as the body (hidden at render).
  async function postMessage(body: string, attachments: Attachment[] = []) {
    if (!session) return;
    const cid = uuid();
    const finalBody = body || attachments[0]?.filename || "file";
    const temp: Message = {
      message_id: `tmp-${cid}`,
      cid,
      k: cid,
      pending: true,
      sender_id: session.userId,
      body: finalBody,
      created_at: new Date().toISOString(),
      sequence: Number.MAX_SAFE_INTEGER,
      reaction_counts: {},
      latest_reactions: [],
      attachments,
    };
    setMessages((prev) => [...prev, temp]);
    const r = await authed(`/channels/${session.channelId}/messages`, {
      method: "POST",
      body: JSON.stringify({
        client_message_id: cid,
        body: finalBody,
        ...(attachments.length ? { attachments } : {}),
      }),
    });
    if (!r.ok) {
      setMessages((prev) => prev.filter((x) => x.cid !== cid));
      throw new Error("send failed");
    }
    const created: Message = await r.json();
    setMessages((prev) => {
      // The WS echo may have already replaced the temp; if so, drop the temp.
      if (prev.some((x) => x.message_id === created.message_id)) {
        return prev.filter((x) => x.cid !== cid);
      }
      return prev.map((x) =>
        x.cid === cid ? { ...created, k: x.k, reaction_counts: {}, latest_reactions: [] } : x,
      );
    });
  }

  async function send() {
    const body = draft.trim();
    if (!body || !session) return;
    setDraft("");
    sendTyping(false);
    try {
      await postMessage(body);
    } catch {
      setDraft(body);
    }
  }

  async function onFilePicked(file: File | undefined) {
    if (!file || !session) return;
    if (file.size > MAX_UPLOAD_BYTES) {
      setError("file is too large (max 5 MB)");
      return;
    }
    setUploading(true);
    setError(null);
    try {
      const up = await fetch(`${API}/uploads`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${session.token}`,
          "content-type": file.type || "application/octet-stream",
        },
        body: file,
      });
      if (!up.ok) throw new Error("upload failed");
      const { url } = (await up.json()) as { url: string };
      const caption = draft.trim();
      setDraft("");
      await postMessage(caption, [
        { url, type: file.type, filename: file.name, size_bytes: file.size },
      ]);
    } catch (e) {
      setError(e instanceof Error ? e.message : "upload failed");
    } finally {
      setUploading(false);
    }
  }

  async function saveEdit() {
    if (!editing || !session) return;
    const body = editing.body.trim();
    setEditing(null);
    if (!body) return;
    await authed(`/channels/${session.channelId}/messages/${editing.id}`, {
      method: "PATCH",
      body: JSON.stringify({ body }),
    });
  }

  async function removeLinkPreview(m: Message) {
    if (!session) return;
    // optimistic clear; the link_preview.updated event confirms for everyone
    setMessages((prev) => prev.map((x) => (x.message_id === m.message_id ? { ...x, link_preview: null } : x)));
    await authed(`/channels/${session.channelId}/messages/${m.message_id}/link-preview`, { method: "DELETE" });
  }

  function myReacted(m: Message, key: string) {
    return m.latest_reactions?.some((r) => r.reaction === key && r.user_id === session?.userId);
  }
  async function toggleReaction(m: Message, key: string) {
    if (!session || m.pending) return;
    const on = myReacted(m, key);
    await authed(
      `/channels/${session.channelId}/messages/${m.message_id}/reactions${on ? "/" + key : ""}`,
      {
        method: on ? "DELETE" : "POST",
        body: on ? undefined : JSON.stringify({ reaction: key }),
      },
    );
  }

  // A message of ours counts as "read" once anyone else's watermark reaches it.
  const statusOf = useCallback(
    (m: Message): Status => {
      if (m.pending) return "sending";
      const seenByOther = Object.entries(reads).some(
        ([uid, seq]) => uid !== session?.userId && seq >= m.sequence,
      );
      return seenByOther ? "read" : "delivered";
    },
    [reads, session],
  );

  const typers = Object.keys(typing)
    .filter((u) => typing[u])
    .map((u) => names[u] || "Someone");

  const memberCount = useMemo(() => Object.keys(names).length || 1, [names]);

  // ---- join screen ----
  if (!session) {
    return (
      <div className="mx-auto w-full max-w-md rounded-2xl border border-border bg-surface p-6 text-text shadow-2xl">
        <h3 className="text-lg font-semibold">Try the live chat</h3>
        <p className="mt-1 text-sm text-text-muted">
          Pick a name and join the shared workspace. Send messages, react, edit, and watch typing
          indicators and read receipts in real time — running on the actual platform.
        </p>
        <div className="mt-4 flex gap-2">
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && join()}
            placeholder="your name"
            maxLength={40}
            className="flex-1 rounded-lg border border-border bg-bg px-3 py-2 text-sm text-text outline-none focus:border-accent"
          />
          <button
            onClick={join}
            disabled={joining || !username.trim()}
            className="rounded-lg bg-accent px-4 py-2 text-sm font-medium text-bg disabled:opacity-50"
          >
            {joining ? "Joining…" : "Join"}
          </button>
        </div>
        {error && <p className="mt-2 text-sm text-danger">{error}</p>}
      </div>
    );
  }

  const activeChannel = CHANNELS.find((c) => c.key === active) ?? CHANNELS[0];

  // ---- chat screen ----
  return (
    <div className="mx-auto flex h-[34rem] w-full max-w-4xl overflow-hidden rounded-[22px] border border-border bg-surface text-text shadow-2xl">
      {/* Sidebar */}
      <aside className="hidden w-56 shrink-0 flex-col border-r border-border-soft bg-bg/40 sm:flex">
        <div className="flex items-center gap-2.5 border-b border-border-soft px-4 py-3.5">
          <Avatar name="Demo Workspace" size={30} />
          <div className="min-w-0">
            <div className="truncate text-sm font-semibold">Demo Workspace</div>
            <div className="flex items-center gap-1.5 text-[11px] text-text-muted">
              <span className={`h-1.5 w-1.5 rounded-full ${status === "open" ? "bg-accent" : "bg-text-faint"}`} />
              {status === "open" ? "live" : status}
            </div>
          </div>
        </div>
        <nav className="flex-1 overflow-y-auto px-2 py-3">
          <div className="px-2 pb-1.5 text-[10px] font-semibold uppercase tracking-wider text-text-faint">
            Channels
          </div>
          {CHANNELS.map((c) => {
            const on = c.key === active;
            return (
              <button
                key={c.key}
                onClick={() => setActive(c.key)}
                className={`group flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-sm ${
                  on ? "bg-accent-soft text-text" : "text-text-muted hover:bg-surface-2 hover:text-text"
                }`}
              >
                <Hash className={`h-3.5 w-3.5 ${on ? "text-accent" : "text-text-faint"}`} />
                <span className="truncate">{c.name}</span>
                {c.live && (
                  <span className="ml-auto h-1.5 w-1.5 rounded-full bg-accent" title="live demo channel" />
                )}
              </button>
            );
          })}
        </nav>
        <div className="flex items-center gap-2 border-t border-border-soft px-3 py-3">
          <Avatar name={session.displayName} size={28} />
          <div className="min-w-0 flex-1">
            <div className="truncate text-xs font-medium text-text">{session.displayName}</div>
            <div className="text-[10px] text-text-faint">you</div>
          </div>
          <button
            onClick={leave}
            className="rounded px-1.5 py-1 text-[11px] text-text-faint hover:text-text"
          >
            leave
          </button>
        </div>
      </aside>

      {/* Chat pane */}
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-2 border-b border-border-soft px-4 py-3.5">
          <Hash className="h-4 w-4 text-text-faint" />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">{activeChannel.name}</div>
            <div className="flex items-center gap-1.5 text-[11px] text-text-muted">
              <span className={`h-1.5 w-1.5 rounded-full ${status === "open" ? "bg-accent" : "bg-text-faint"}`} />
              {memberCount} {memberCount === 1 ? "member" : "members"}
              {status === "open" ? " · live" : ` · ${status}`}
            </div>
          </div>
          <button
            onClick={leave}
            className="text-xs text-text-faint hover:text-text sm:hidden"
          >
            leave
          </button>
        </div>

        {!activeChannel.live ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-3 px-8 text-center">
            <div className="grid h-12 w-12 place-items-center rounded-full bg-surface-2 text-text-faint">
              <Hash className="h-5 w-5" />
            </div>
            <p className="text-sm text-text-muted">
              <span className="font-medium text-text">#{activeChannel.name}</span> is part of the full
              workspace. The live demo runs in{" "}
              <button onClick={() => setActive("general")} className="font-medium text-accent hover:underline">
                #general
              </button>
              — jump back in to keep chatting.
            </p>
          </div>
        ) : (
          <>
            <div ref={listRef} className="flex flex-1 flex-col gap-3 overflow-y-auto px-4 py-5">
              {messages.length === 0 && (
                <p className="m-auto text-sm text-text-faint">No messages yet — say hi 👋</p>
              )}
              {messages.map((m) => {
                const mine = m.sender_id === session.userId;
                const editable = mine && !m.pending && editing?.id !== m.message_id;
                const reactionPills = Object.entries(m.reaction_counts || {}).filter(([, c]) => c > 0);

                if (mine) {
                  return (
                    <div key={m.k ?? m.message_id} className="demo-msg-in group flex flex-col items-end">
                      {editing?.id === m.message_id ? (
                        <div className="flex w-full max-w-[80%] items-center gap-2">
                          <input
                            value={editing.body}
                            onChange={(e) => setEditing({ id: m.message_id, body: e.target.value })}
                            onKeyDown={(e) =>
                              e.key === "Enter" ? saveEdit() : e.key === "Escape" ? setEditing(null) : null
                            }
                            className="flex-1 rounded-lg border border-border bg-bg px-2.5 py-1.5 text-[13px] text-text outline-none focus:border-accent"
                            autoFocus
                          />
                          <button onClick={saveEdit} className="text-xs text-accent">save</button>
                          <button onClick={() => setEditing(null)} className="text-xs text-text-faint">cancel</button>
                        </div>
                      ) : (
                        <>
                          {hasCaption(m) && (
                            <div className="max-w-[80%] whitespace-pre-wrap break-words rounded-2xl rounded-br-md bg-accent px-3.5 py-2 text-[13px] leading-snug text-bg">
                              {linkify(m.body)}
                            </div>
                          )}
                          {m.attachments?.map((a, i) => (
                            <AttachmentView key={i} a={a} mine />
                          ))}
                          {m.link_preview && (
                            <LinkPreviewCard p={m.link_preview} onRemove={() => removeLinkPreview(m)} />
                          )}
                        </>
                      )}
                      <div className="mt-1 flex items-center gap-1.5 pr-1 text-[10px] text-text-faint">
                        {editable && (
                          <button
                            onClick={() => setEditing({ id: m.message_id, body: m.body })}
                            className="inline-flex items-center gap-0.5 text-text-faint transition-colors hover:text-accent"
                          >
                            <Pencil className="h-2.5 w-2.5" /> edit
                          </button>
                        )}
                        {m.edited_at && <span>edited</span>}
                        <span>{timeOf(m.created_at)}</span>
                        <Ticks status={statusOf(m)} />
                      </div>
                      {reactionPills.length > 0 && (
                        <div className="mt-1 flex flex-wrap justify-end gap-1">
                          {reactionPills.map(([k, c]) => (
                            <button
                              key={k}
                              onClick={() => toggleReaction(m, k)}
                              className={`rounded-full border px-1.5 py-0.5 text-[11px] ${
                                myReacted(m, k)
                                  ? "border-accent/50 bg-accent-soft text-text"
                                  : "border-border bg-bg text-text-muted"
                              }`}
                            >
                              {glyph(k)} {c}
                            </button>
                          ))}
                        </div>
                      )}
                      <div className="mt-0.5 flex gap-0.5 opacity-0 transition-opacity duration-150 group-hover:opacity-100">
                        {REACTIONS.map((r) => (
                          <button
                            key={r.key}
                            onClick={() => toggleReaction(m, r.key)}
                            title={r.key}
                            className="rounded px-1 text-xs transition-transform duration-150 hover:scale-[1.35]"
                          >
                            {r.glyph}
                          </button>
                        ))}
                      </div>
                    </div>
                  );
                }

                return (
                  <div key={m.k ?? m.message_id} className="demo-msg-in group flex items-end gap-2">
                    <Avatar name={names[m.sender_id] || "Someone"} />
                    <div className="flex max-w-[80%] flex-col items-start">
                      <span
                        className="mb-1 pl-1 text-[11px] font-medium"
                        style={{ color: `hsl(${hueFor(names[m.sender_id] || m.sender_id)} 60% 62%)` }}
                      >
                        {names[m.sender_id] || "Someone"}
                      </span>
                      {hasCaption(m) && (
                        <div className="whitespace-pre-wrap break-words rounded-2xl rounded-bl-md bg-surface-2 px-3.5 py-2 text-[13px] leading-snug text-text">
                          {linkify(m.body)}
                        </div>
                      )}
                      {m.attachments?.map((a, i) => (
                        <AttachmentView key={i} a={a} mine={false} />
                      ))}
                      {m.link_preview && <LinkPreviewCard p={m.link_preview} />}
                      <div className="mt-1 flex items-center gap-1.5 pl-1">
                        {reactionPills.length > 0 && (
                          <span className="inline-flex items-center gap-1">
                            {reactionPills.map(([k, c]) => (
                              <button
                                key={k}
                                onClick={() => toggleReaction(m, k)}
                                className={`inline-flex items-center gap-0.5 rounded-full border px-1.5 py-0.5 text-[11px] ${
                                  myReacted(m, k)
                                    ? "border-accent/50 bg-accent-soft text-text"
                                    : "border-border bg-bg text-text-muted"
                                }`}
                              >
                                <span className="text-[12px] leading-none">{glyph(k)}</span> {c}
                              </button>
                            ))}
                          </span>
                        )}
                        {m.edited_at && <span className="text-[10px] text-text-faint">edited</span>}
                        <span className="text-[10px] text-text-faint">{timeOf(m.created_at)}</span>
                      </div>
                    </div>
                    <div className="mb-5 flex gap-0.5 self-center opacity-0 transition-opacity duration-150 group-hover:opacity-100">
                      {REACTIONS.map((r) => (
                        <button
                          key={r.key}
                          onClick={() => toggleReaction(m, r.key)}
                          title={r.key}
                          className="rounded px-1 text-xs transition-transform duration-150 hover:scale-[1.35]"
                        >
                          {r.glyph}
                        </button>
                      ))}
                    </div>
                  </div>
                );
              })}

              {typers.length > 0 && (
                <div className="flex items-center gap-2">
                  <Avatar name={typers[0]} />
                  <div className="flex items-center gap-1 rounded-2xl rounded-bl-md bg-surface-2 px-3.5 py-2.5">
                    <span className="chat-typing-dot h-1.5 w-1.5 rounded-full bg-text-faint" style={{ animationDelay: "0ms" }} />
                    <span className="chat-typing-dot h-1.5 w-1.5 rounded-full bg-text-faint" style={{ animationDelay: "150ms" }} />
                    <span className="chat-typing-dot h-1.5 w-1.5 rounded-full bg-text-faint" style={{ animationDelay: "300ms" }} />
                  </div>
                </div>
              )}
            </div>

            <div className="h-4 px-4 text-[11px]">
              {error ? (
                <span className="text-danger">{error}</span>
              ) : uploading ? (
                <span className="text-text-faint">uploading…</span>
              ) : (
                <span className="text-text-faint">
                  {typers.length > 0 &&
                    `${typers.slice(0, 3).join(", ")} ${typers.length === 1 ? "is" : "are"} typing…`}
                </span>
              )}
            </div>

            <div className="flex items-center gap-2 border-t border-border-soft px-3 py-3">
              <input
                ref={fileRef}
                type="file"
                accept="image/*,application/pdf"
                className="hidden"
                onChange={(e) => {
                  onFilePicked(e.target.files?.[0]);
                  e.target.value = "";
                }}
              />
              <button
                type="button"
                aria-label="Attach a file"
                title="Attach a file"
                disabled={uploading}
                onClick={() => fileRef.current?.click()}
                className="grid h-8 w-8 shrink-0 place-items-center rounded-full text-text-muted hover:text-text disabled:opacity-50"
              >
                {uploading ? (
                  <Paperclip className="h-[18px] w-[18px] animate-pulse text-accent" />
                ) : (
                  <Plus className="h-[18px] w-[18px]" />
                )}
              </button>
              <div className="flex flex-1 items-center gap-2 rounded-full border border-border bg-bg px-3.5 py-2 focus-within:border-accent">
                <input
                  ref={draftRef}
                  value={draft}
                  onChange={(e) => onDraftChange(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && send()}
                  placeholder={`Message #${activeChannel.name}`}
                  className="flex-1 bg-transparent text-[13px] text-text outline-none placeholder:text-text-faint"
                />
                <div ref={emojiRef} className="relative shrink-0">
                  <button
                    type="button"
                    aria-label="Emoji"
                    onClick={() => setEmojiOpen((o) => !o)}
                    className={`grid place-items-center transition-colors ${emojiOpen ? "text-accent" : "text-text-faint hover:text-text"}`}
                  >
                    <Smile className="h-[17px] w-[17px]" />
                  </button>
                  {emojiOpen && (
                    <div className="absolute bottom-8 right-0 z-20 w-60 rounded-xl border border-border bg-surface p-2 shadow-2xl">
                      <div className="grid grid-cols-8 gap-0.5">
                        {EMOJI_PICKER.map((e) => (
                          <button
                            key={e}
                            type="button"
                            onClick={() => insertEmoji(e)}
                            className="rounded-md py-1 text-base transition-transform duration-150 hover:scale-125 hover:bg-surface-2"
                          >
                            {e}
                          </button>
                        ))}
                      </div>
                    </div>
                  )}
                </div>
              </div>
              <button
                onClick={send}
                disabled={!draft.trim()}
                aria-label="Send"
                className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-accent text-bg disabled:opacity-40"
              >
                <SendHorizontal className="h-[17px] w-[17px]" />
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
