"use client";

import { useCallback, useEffect, useRef, useState } from "react";

// Live chat demo for the landing page. A visitor picks a username, the server
// route (/api/demo/session) mints them an end-user in the ENTERPRISE demo app
// and drops them in the shared Lobby, and this widget then exercises the real
// platform over the Cloudflare edge: send, react, edit, and typing indicators.

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

type Reaction = { reaction: string; user_id: string };
type Message = {
  message_id: string;
  sender_id: string;
  body: string;
  created_at: string;
  sequence: number;
  edited_at?: string | null;
  reaction_counts: Record<string, number>;
  latest_reactions: Reaction[];
};
type Session = { token: string; userId: string; displayName: string; channelId: string };

const uuid = () => crypto.randomUUID();

export default function DemoChat() {
  const [session, setSession] = useState<Session | null>(null);
  const [username, setUsername] = useState("");
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [messages, setMessages] = useState<Message[]>([]);
  const [names, setNames] = useState<Record<string, string>>({});
  const [typing, setTyping] = useState<Record<string, boolean>>({});
  const [status, setStatus] = useState<"connecting" | "open" | "closed">("connecting");
  const [draft, setDraft] = useState("");
  const [editing, setEditing] = useState<{ id: string; body: string } | null>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);
  const typingSentAt = useRef(0);
  const typingStopTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const typingClear = useRef<Record<string, ReturnType<typeof setTimeout>>>({});

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

  const loadMembers = useCallback(
    async (s: Session) => {
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
    },
    [],
  );

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
      setSession(s);
      setNames((p) => ({ ...p, [s.userId]: s.displayName }));
      // recent history
      const hist = await fetch(`${API}/channels/${s.channelId}/messages?limit=40`, {
        headers: { authorization: `Bearer ${s.token}` },
      });
      if (hist.ok) setMessages(((await hist.json()) as Message[]).slice().reverse());
      await loadMembers(s);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not join");
    } finally {
      setJoining(false);
    }
  }

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
        };
        setMessages((prev) => (prev.some((x) => x.message_id === m.message_id) ? prev : [...prev, m]));
        if (!(f.sender_id as string in names)) loadMembers(session);
      } else if (type === "message.edited") {
        setMessages((prev) =>
          prev.map((m) => (m.message_id === f.message_id ? { ...m, body: f.body as string, edited_at: f.edited_at as string } : m)),
        );
      } else if (type === "reaction.updated") {
        setMessages((prev) =>
          prev.map((m) =>
            m.message_id === f.message_id
              ? { ...m, reaction_counts: f.reaction_counts as Record<string, number>, latest_reactions: (f.latest_reactions as Reaction[]) || [] }
              : m,
          ),
        );
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

  // autoscroll
  useEffect(() => {
    listRef.current?.scrollTo({ top: listRef.current.scrollHeight });
  }, [messages, typing]);

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

  async function send() {
    const body = draft.trim();
    if (!body || !session) return;
    setDraft("");
    sendTyping(false);
    await authed(`/channels/${session.channelId}/messages`, {
      method: "POST",
      body: JSON.stringify({ client_message_id: uuid(), body }),
    });
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

  function myReacted(m: Message, key: string) {
    return m.latest_reactions?.some((r) => r.reaction === key && r.user_id === session?.userId);
  }
  async function toggleReaction(m: Message, key: string) {
    if (!session) return;
    const on = myReacted(m, key);
    await authed(`/channels/${session.channelId}/messages/${m.message_id}/reactions${on ? "/" + key : ""}`, {
      method: on ? "DELETE" : "POST",
      body: on ? undefined : JSON.stringify({ reaction: key }),
    });
  }

  const typers = Object.keys(typing)
    .filter((u) => typing[u])
    .map((u) => names[u] || "Someone");

  // ---- join screen ----
  if (!session) {
    return (
      <div className="mx-auto w-full max-w-md rounded-2xl border border-white/10 bg-neutral-900 p-6 text-neutral-100 shadow-2xl">
        <h3 className="text-lg font-semibold">Try the live chat</h3>
        <p className="mt-1 text-sm text-neutral-400">
          Pick a name and join the shared Lobby. Send messages, react, edit, and watch typing
          indicators in real time — running on the actual platform.
        </p>
        <div className="mt-4 flex gap-2">
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && join()}
            placeholder="your name"
            maxLength={40}
            className="flex-1 rounded-lg border border-white/10 bg-neutral-800 px-3 py-2 text-sm outline-none focus:border-white/30"
          />
          <button
            onClick={join}
            disabled={joining || !username.trim()}
            className="rounded-lg bg-white px-4 py-2 text-sm font-medium text-neutral-900 disabled:opacity-50"
          >
            {joining ? "Joining…" : "Join"}
          </button>
        </div>
        {error && <p className="mt-2 text-sm text-red-400">{error}</p>}
      </div>
    );
  }

  // ---- chat screen ----
  return (
    <div className="mx-auto flex h-[32rem] w-full max-w-md flex-col overflow-hidden rounded-2xl border border-white/10 bg-neutral-900 text-neutral-100 shadow-2xl">
      <div className="flex items-center justify-between border-b border-white/10 px-4 py-3">
        <div>
          <div className="text-sm font-semibold">Lobby</div>
          <div className="text-xs text-neutral-400">
            you are <span className="text-neutral-200">{session.displayName}</span> ·{" "}
            <span className={status === "open" ? "text-emerald-400" : "text-neutral-500"}>
              {status === "open" ? "live" : status}
            </span>
          </div>
        </div>
        <button onClick={() => setSession(null)} className="text-xs text-neutral-400 hover:text-neutral-200">
          leave
        </button>
      </div>

      <div ref={listRef} className="flex-1 space-y-3 overflow-y-auto px-4 py-3">
        {messages.length === 0 && <p className="text-sm text-neutral-500">No messages yet — say hi 👋</p>}
        {messages.map((m) => {
          const mine = m.sender_id === session.userId;
          return (
            <div key={m.message_id} className="group">
              <div className="flex items-baseline gap-2">
                <span className="text-xs font-medium text-neutral-300">{names[m.sender_id] || "Someone"}</span>
                {m.edited_at && <span className="text-[10px] text-neutral-500">(edited)</span>}
              </div>
              {editing?.id === m.message_id ? (
                <div className="mt-1 flex gap-2">
                  <input
                    value={editing.body}
                    onChange={(e) => setEditing({ id: m.message_id, body: e.target.value })}
                    onKeyDown={(e) => e.key === "Enter" && saveEdit()}
                    className="flex-1 rounded border border-white/10 bg-neutral-800 px-2 py-1 text-sm outline-none"
                    autoFocus
                  />
                  <button onClick={saveEdit} className="text-xs text-emerald-400">save</button>
                  <button onClick={() => setEditing(null)} className="text-xs text-neutral-500">cancel</button>
                </div>
              ) : (
                <div className="text-sm text-neutral-100">{m.body}</div>
              )}

              <div className="mt-1 flex flex-wrap items-center gap-1">
                {Object.entries(m.reaction_counts || {})
                  .filter(([, c]) => c > 0)
                  .map(([k, c]) => (
                    <button
                      key={k}
                      onClick={() => toggleReaction(m, k)}
                      className={`rounded-full border px-1.5 py-0.5 text-xs ${
                        myReacted(m, k) ? "border-emerald-500/50 bg-emerald-500/10" : "border-white/10 bg-neutral-800"
                      }`}
                    >
                      {glyph(k)} {c}
                    </button>
                  ))}
                <div className="ml-1 hidden gap-0.5 group-hover:flex">
                  {REACTIONS.map((r) => (
                    <button
                      key={r.key}
                      onClick={() => toggleReaction(m, r.key)}
                      title={r.key}
                      className="rounded px-1 text-xs opacity-70 hover:opacity-100"
                    >
                      {r.glyph}
                    </button>
                  ))}
                  {mine && editing?.id !== m.message_id && (
                    <button
                      onClick={() => setEditing({ id: m.message_id, body: m.body })}
                      className="ml-1 rounded px-1 text-[11px] text-neutral-400 hover:text-neutral-200"
                    >
                      edit
                    </button>
                  )}
                </div>
              </div>
            </div>
          );
        })}
      </div>

      <div className="h-5 px-4 text-xs text-neutral-500">
        {typers.length > 0 && `${typers.slice(0, 3).join(", ")} ${typers.length === 1 ? "is" : "are"} typing…`}
      </div>

      <div className="flex gap-2 border-t border-white/10 p-3">
        <input
          value={draft}
          onChange={(e) => onDraftChange(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && send()}
          placeholder="Message the Lobby…"
          className="flex-1 rounded-lg border border-white/10 bg-neutral-800 px-3 py-2 text-sm outline-none focus:border-white/30"
        />
        <button onClick={send} disabled={!draft.trim()} className="rounded-lg bg-white px-4 py-2 text-sm font-medium text-neutral-900 disabled:opacity-50">
          Send
        </button>
      </div>
    </div>
  );
}
