"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  ChevronDown,
  Hash,
  History,
  PlugZap,
  RotateCcw,
  Send,
  SmilePlus,
  Terminal,
  Unplug,
  Wifi,
  WifiOff,
  Zap,
} from "lucide-react";
import { addReaction, listMembers, listMessages, listReadState, markRead, removeReaction, sendMessage, ApiError } from "@/lib/api";
import { QUICK_REACT_KEYS, reactionGlyph } from "@/lib/reactions";
import { REGION_ENDPOINTS } from "@/lib/regions";
import type { ChannelSummary, Message, Profile, ReactionSummary } from "@/lib/types";
import { Avatar, Badge, Button, Input, cx } from "@/components/ui";

type WsStatus = "connecting" | "open" | "closed";

interface DeliveryFrame {
  type: string;
  channel_id: string;
  message_id: string;
  sequence: number;
  sender_id: string;
  body: string;
  created_at: string;
}

interface ReactionDeliveryFrame {
  type: string;
  channel_id: string;
  message_id: string;
  actor_id: string;
  reaction: string;
  action: "added" | "removed";
  reaction_counts: Record<string, number>;
  latest_reactions: ReactionSummary[];
}

interface TypingDeliveryFrame {
  type: string;
  channel_id: string;
  user_id: string;
  typing: boolean;
}

interface ReadDeliveryFrame {
  type: string;
  channel_id: string;
  user_id: string;
  last_read_sequence: number;
}

// How long a typing indicator stays visible after the last typing.start
// with no follow-up — covers a client that never sends typing.stop (closed
// tab, crash, dropped connection).
const TYPING_INDICATOR_TTL_MS = 6000;
// How often typing.start is re-sent while the user keeps typing without
// pausing — keeps the socket quiet during a long burst of keystrokes.
const TYPING_RESEND_INTERVAL_MS = 2500;
// How long without a keystroke before typing.stop is sent automatically.
const TYPING_STOP_DELAY_MS = 4000;

function mergeMessages(existing: Message[], incoming: Message[]): Message[] {
  const byId = new Map(existing.map((m) => [m.message_id, m]));
  for (const m of incoming) byId.set(m.message_id, m);
  return Array.from(byId.values()).sort((a, b) => a.sequence - b.sequence);
}

const wsStatusMeta: Record<
  WsStatus,
  { tone: "success" | "warning" | "danger"; icon: typeof Wifi; label: string; pulse: boolean }
> = {
  open: { tone: "success", icon: Wifi, label: "live", pulse: true },
  connecting: { tone: "warning", icon: Wifi, label: "connecting", pulse: true },
  closed: { tone: "danger", icon: WifiOff, label: "disconnected", pulse: false },
};

export function ChatPanel({
  profile,
  channel,
  onMessageDelivered,
}: {
  profile: Profile;
  channel: ChannelSummary;
  /** Fired for every live-delivered message, so the parent can refresh the
   * members panel if a sender it hasn't seen before shows up. */
  onMessageDelivered?: () => void;
}) {
  const channelId = channel.channel_id;
  const apiBase = REGION_ENDPOINTS[profile.region].apiBase;

  const [messages, setMessages] = useState<Message[]>([]);
  // Which of the CURRENT user's own reactions exist per message — derived
  // from actions the user themselves took (a request response or a
  // reaction.updated frame where actor_id is you), not from
  // latest_reactions, since that's capped at the 5 most recent and may not
  // include an older reaction of yours once enough newer ones arrive.
  const [myReactions, setMyReactions] = useState<Record<string, Set<string>>>({});
  const [memberNames, setMemberNames] = useState<Record<string, string>>({});
  // Per-member "read up to sequence N" watermark (internal/readstate) — used
  // to compute "Seen by …" under your own latest message.
  const [readState, setReadState] = useState<Record<string, number>>({});
  // user_id -> currently showing a typing indicator. Purely ephemeral,
  // never persisted (see internal/realtime.ConnectHandler.relayTyping).
  const [typingUsers, setTypingUsers] = useState<Record<string, true>>({});
  const [wsStatus, setWsStatus] = useState<WsStatus>("connecting");
  const [composeText, setComposeText] = useState("");
  const [lastSent, setLastSent] = useState<{ clientMessageId: string; body: string } | null>(null);
  const [log, setLog] = useState<string[]>([]);
  const [devToolsOpen, setDevToolsOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const messagesEndRef = useRef<HTMLDivElement | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const atBottomRef = useRef(true);
  const typingClearTimersRef = useRef<Record<string, ReturnType<typeof setTimeout>>>({});
  const typingStopTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastTypingSentAtRef = useRef(0);
  const lastMarkedReadSequenceRef = useRef(0);

  const appendLog = useCallback((line: string) => {
    setLog((prev) => [...prev.slice(-30), `${new Date().toLocaleTimeString()}  ${line}`]);
  }, []);

  // maybeMarkRead only calls the API when both true: the reader is actually
  // at the bottom (no point marking read what they haven't scrolled to) and
  // sequence is newer than what was already marked (avoids a redundant call
  // per message when several arrive in a burst — the latest sequence covers
  // all the earlier ones anyway).
  const maybeMarkRead = useCallback(
    (sequence: number) => {
      if (!atBottomRef.current) return;
      if (sequence <= lastMarkedReadSequenceRef.current) return;
      lastMarkedReadSequenceRef.current = sequence;
      markRead(apiBase, profile.token, channelId, sequence).catch(() => {
        // Best-effort: a failed mark-read just means "Seen by" lags for
        // this reader, not a data-loss concern.
      });
    },
    [apiBase, profile.token, channelId]
  );

  const connectWs = useCallback(
    (p: Profile) => {
      setWsStatus("connecting");
      const ws = new WebSocket(`${REGION_ENDPOINTS[p.region].wsBase}/connect?token=${p.token}`);
      ws.onopen = () => {
        setWsStatus("open");
        appendLog("websocket connected");
      };
      ws.onclose = () => {
        setWsStatus("closed");
        appendLog("websocket disconnected");
      };
      ws.onerror = () => appendLog("websocket error");
      ws.onmessage = (evt) => {
        try {
          const raw = JSON.parse(evt.data) as { type: string; channel_id: string };
          if (raw.channel_id !== channelId) return;

          if (raw.type === "message.created") {
            const frame = raw as unknown as DeliveryFrame;
            appendLog(`delivered live: seq ${frame.sequence}`);
            setMessages((prev) =>
              mergeMessages(prev, [
                {
                  message_id: frame.message_id,
                  channel_id: frame.channel_id,
                  sequence: frame.sequence,
                  sender_id: frame.sender_id,
                  client_message_id: "",
                  body: frame.body,
                  created_at: frame.created_at,
                  reaction_counts: {},
                  latest_reactions: [],
                },
              ])
            );
            onMessageDelivered?.();
            maybeMarkRead(frame.sequence);
            return;
          }

          if (raw.type === "typing.updated") {
            const frame = raw as unknown as TypingDeliveryFrame;
            clearTimeout(typingClearTimersRef.current[frame.user_id]);
            if (frame.typing) {
              setTypingUsers((prev) => ({ ...prev, [frame.user_id]: true }));
              typingClearTimersRef.current[frame.user_id] = setTimeout(() => {
                setTypingUsers((prev) => {
                  const next = { ...prev };
                  delete next[frame.user_id];
                  return next;
                });
              }, TYPING_INDICATOR_TTL_MS);
            } else {
              setTypingUsers((prev) => {
                const next = { ...prev };
                delete next[frame.user_id];
                return next;
              });
            }
            return;
          }

          if (raw.type === "read.updated") {
            const frame = raw as unknown as ReadDeliveryFrame;
            setReadState((prev) => ({ ...prev, [frame.user_id]: frame.last_read_sequence }));
            return;
          }

          if (raw.type === "reaction.updated") {
            const frame = raw as unknown as ReactionDeliveryFrame;
            appendLog(`reaction ${frame.action}: ${frame.reaction} on message ${frame.message_id.slice(0, 8)}…`);
            setMessages((prev) =>
              prev.map((m) =>
                m.message_id === frame.message_id
                  ? { ...m, reaction_counts: frame.reaction_counts, latest_reactions: frame.latest_reactions }
                  : m
              )
            );
            if (frame.actor_id === p.userId) {
              setMyReactions((prev) => {
                const next = { ...prev };
                const set = new Set(next[frame.message_id]);
                if (frame.action === "added") set.add(frame.reaction);
                else set.delete(frame.reaction);
                next[frame.message_id] = set;
                return next;
              });
            }
          }
        } catch {
          // ignore malformed frames
        }
      };
      wsRef.current = ws;
    },
    [channelId, appendLog, onMessageDelivered, maybeMarkRead]
  );

  useEffect(() => {
    setMessages([]);
    setMyReactions({});
    setReadState({});
    setTypingUsers({});
    setLog([]);
    atBottomRef.current = true;
    lastMarkedReadSequenceRef.current = 0;
    setShowJumpToLatest(false);
    listMessages(apiBase, profile.token, channelId)
      .then((initial) => {
        setMessages(mergeMessages([], initial));
        // Best-effort: latest_reactions only holds the 5 most recent, so an
        // older reaction of yours displaced off that list won't show as
        // "already reacted" until you act on it again or it's echoed live.
        setMyReactions((prev) => {
          const next = { ...prev };
          for (const m of initial) {
            const mine = m.latest_reactions.filter((r) => r.user_id === profile.userId).map((r) => r.reaction);
            if (mine.length > 0) next[m.message_id] = new Set([...(next[m.message_id] ?? []), ...mine]);
          }
          return next;
        });
        // Opening a channel counts as reading everything currently in it.
        const latest = initial.reduce((max, m) => Math.max(max, m.sequence), 0);
        if (latest > 0) maybeMarkRead(latest);
      })
      .catch((err) => appendLog(`history load failed: ${err}`));
    listMembers(apiBase, profile.token, channelId)
      .then((list) => setMemberNames(Object.fromEntries(list.map((m) => [m.user_id, m.display_name]))))
      .catch(() => {
        // Non-critical: falls back to showing a user_id prefix.
      });
    listReadState(apiBase, profile.token, channelId)
      .then((states) => setReadState(Object.fromEntries(states.map((s) => [s.user_id, s.last_read_sequence]))))
      .catch(() => {
        // Non-critical: "Seen by" just starts empty until a live update arrives.
      });
    connectWs(profile);
    return () => {
      wsRef.current?.close();
      for (const timer of Object.values(typingClearTimersRef.current)) clearTimeout(timer);
      typingClearTimersRef.current = {};
      if (typingStopTimerRef.current) clearTimeout(typingStopTimerRef.current);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelId]);

  useEffect(() => {
    // Only auto-follow if the reader was already near the bottom — someone
    // scrolled up to read history shouldn't get yanked back down by a new
    // message arriving elsewhere.
    if (atBottomRef.current) {
      messagesEndRef.current?.scrollIntoView({ block: "end" });
    } else {
      setShowJumpToLatest(true);
    }
  }, [messages.length]);

  function handleMessagesScroll() {
    const el = scrollRef.current;
    if (!el) return;
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    const nearBottom = distanceFromBottom < 80;
    const wasAtBottom = atBottomRef.current;
    atBottomRef.current = nearBottom;
    setShowJumpToLatest(!nearBottom && messages.length > 0);
    if (nearBottom && !wasAtBottom && messages.length > 0) {
      maybeMarkRead(messages[messages.length - 1].sequence);
    }
  }

  function jumpToLatest() {
    messagesEndRef.current?.scrollIntoView({ block: "end", behavior: "smooth" });
    atBottomRef.current = true;
    setShowJumpToLatest(false);
    if (messages.length > 0) maybeMarkRead(messages[messages.length - 1].sequence);
  }

  // sendTypingStart is throttled to at most once per TYPING_RESEND_INTERVAL_MS
  // while the user keeps typing without pausing, and arms a timer that sends
  // typing.stop automatically after TYPING_STOP_DELAY_MS of no keystrokes —
  // covers the common case of a user just stopping without deleting/sending.
  function sendTypingStart() {
    if (wsRef.current?.readyState !== WebSocket.OPEN) return;
    const now = Date.now();
    if (now - lastTypingSentAtRef.current > TYPING_RESEND_INTERVAL_MS) {
      wsRef.current.send(JSON.stringify({ type: "typing.start", channel_id: channelId }));
      lastTypingSentAtRef.current = now;
    }
    if (typingStopTimerRef.current) clearTimeout(typingStopTimerRef.current);
    typingStopTimerRef.current = setTimeout(sendTypingStop, TYPING_STOP_DELAY_MS);
  }

  function sendTypingStop() {
    if (typingStopTimerRef.current) {
      clearTimeout(typingStopTimerRef.current);
      typingStopTimerRef.current = null;
    }
    if (lastTypingSentAtRef.current === 0) return; // never actually sent a start
    lastTypingSentAtRef.current = 0;
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: "typing.stop", channel_id: channelId }));
    }
  }

  // toggleReaction applies the API response immediately (instant feedback
  // for the person clicking) — the websocket also delivers the same
  // reaction.updated frame to every member including yourself, which just
  // re-applies the identical state, harmlessly.
  async function toggleReaction(messageId: string, reaction: string) {
    const alreadyReacted = myReactions[messageId]?.has(reaction) ?? false;
    try {
      const state = alreadyReacted
        ? await removeReaction(apiBase, profile.token, channelId, messageId, reaction)
        : await addReaction(apiBase, profile.token, channelId, messageId, reaction);
      setMessages((prev) =>
        prev.map((m) => (m.message_id === messageId ? { ...m, reaction_counts: state.reaction_counts, latest_reactions: state.latest_reactions } : m))
      );
      setMyReactions((prev) => {
        const next = { ...prev };
        const set = new Set(next[messageId]);
        if (alreadyReacted) set.delete(reaction);
        else set.add(reaction);
        next[messageId] = set;
        return next;
      });
    } catch (err) {
      appendLog(err instanceof ApiError ? `reaction failed: ${err.status} ${err.message}` : `reaction failed: ${err}`);
    }
  }

  async function loadOlder() {
    if (messages.length === 0) return;
    const oldest = messages[0].sequence;
    try {
      const older = await listMessages(apiBase, profile.token, channelId, oldest);
      setMessages((prev) => mergeMessages(prev, older));
      appendLog(`loaded ${older.length} older message(s) before seq ${oldest}`);
    } catch (err) {
      appendLog(`load older failed: ${err}`);
    }
  }

  async function handleSend(e: React.FormEvent) {
    e.preventDefault();
    if (!composeText.trim()) return;
    const clientMessageId = crypto.randomUUID();
    const body = composeText.trim();
    setBusy(true);
    sendTypingStop();
    try {
      const msg = await sendMessage(apiBase, profile.token, channelId, clientMessageId, body);
      setLastSent({ clientMessageId, body });
      setComposeText("");
      appendLog(`sent: seq ${msg.sequence} (client_message_id ${clientMessageId.slice(0, 8)}…)`);
    } catch (err) {
      appendLog(err instanceof ApiError ? `send failed: ${err.status} ${err.message}` : `send failed: ${err}`);
    } finally {
      setBusy(false);
    }
  }

  async function retryLast() {
    if (!lastSent) return;
    setBusy(true);
    try {
      const msg = await sendMessage(apiBase, profile.token, channelId, lastSent.clientMessageId, lastSent.body);
      appendLog(
        `retry returned SAME message: seq ${msg.sequence}, message_id ${msg.message_id.slice(0, 8)}… (idempotent — no duplicate created)`
      );
    } catch (err) {
      appendLog(err instanceof ApiError ? `retry failed: ${err.status} ${err.message}` : `retry failed: ${err}`);
    } finally {
      setBusy(false);
    }
  }

  async function spamSend() {
    setBusy(true);
    let accepted = 0;
    let limited = 0;
    appendLog("spam send: firing 25 messages as fast as possible…");
    for (let i = 0; i < 25; i++) {
      try {
        await sendMessage(apiBase, profile.token, channelId, crypto.randomUUID(), `spam #${i + 1}`);
        accepted++;
      } catch (err) {
        if (err instanceof ApiError && err.status === 429) limited++;
        else appendLog(`spam send error: ${err}`);
      }
    }
    appendLog(`spam send done: ${accepted} accepted, ${limited} rate-limited (429) — tier limit is working`);
    setBusy(false);
  }

  function disconnect() {
    wsRef.current?.close();
  }

  function reconnect() {
    connectWs(profile);
    setTimeout(async () => {
      try {
        const recent = await listMessages(apiBase, profile.token, channelId);
        setMessages((prev) => mergeMessages(prev, recent));
        appendLog("reconnect recovery: re-fetched recent history to fill any gap from realtime delivery");
      } catch (err) {
        appendLog(`recovery fetch failed: ${err}`);
      }
    }, 500);
  }

  const status = wsStatusMeta[wsStatus];
  const StatusIcon = status.icon;

  const typingNames = Object.keys(typingUsers).map((id) => memberNames[id] ?? id.slice(0, 8));
  const typingLabel =
    typingNames.length === 0
      ? ""
      : typingNames.length === 1
        ? `${typingNames[0]} is typing…`
        : typingNames.length === 2
          ? `${typingNames[0]} and ${typingNames[1]} are typing…`
          : `${typingNames.length} people are typing…`;

  // "Seen by" only ever shows on the CURRENT user's own most recent message
  // (the WhatsApp/iMessage/Slack convention) — not every message, which
  // would be noisy and isn't how any real chat UI does it.
  const myLastMessageId = [...messages].reverse().find((m) => m.sender_id === profile.userId)?.message_id;
  function seenByFor(message: Message): string[] {
    return Object.entries(readState)
      .filter(([userId, seq]) => userId !== profile.userId && seq >= message.sequence)
      .map(([userId]) => memberNames[userId] ?? userId.slice(0, 8));
  }

  return (
    <div className="flex h-full min-w-0 flex-col">
      <div className="flex shrink-0 items-center gap-3 border-b border-border-soft px-5 py-3.5">
        <span className="grid h-9 w-9 place-items-center rounded-full bg-accent-soft text-accent">
          <Hash className="h-4 w-4" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-semibold text-text">{channel.name}</div>
          <div className="truncate font-mono text-[11px] text-text-faint">home {channel.home_region}</div>
        </div>
        <Badge tone={status.tone} pulse={status.pulse} icon={<StatusIcon className="h-3 w-3" />}>
          {status.label}
        </Badge>
        <button
          onClick={() => setDevToolsOpen((v) => !v)}
          className={cx(
            "grid h-8 w-8 place-items-center rounded-lg text-text-faint transition-colors duration-150 hover:bg-white/[0.05] hover:text-text",
            devToolsOpen && "bg-white/[0.05] text-text"
          )}
          title="Dev tools"
        >
          <Terminal className="h-4 w-4" />
        </button>
      </div>

      {devToolsOpen && (
        <div className="animate-fade-in-up shrink-0 border-b border-border-soft bg-bg/40 px-5 py-3">
          <div className="mb-2 flex flex-wrap gap-2">
            <Button variant="secondary" className="text-xs" icon={<History className="h-3.5 w-3.5" />} onClick={loadOlder} disabled={messages.length === 0}>
              Load older
            </Button>
            <Button variant="secondary" className="text-xs" icon={<Unplug className="h-3.5 w-3.5" />} onClick={disconnect} disabled={wsStatus !== "open"}>
              Disconnect
            </Button>
            <Button variant="secondary" className="text-xs" icon={<PlugZap className="h-3.5 w-3.5" />} onClick={reconnect} disabled={wsStatus === "open"}>
              Reconnect + recover
            </Button>
            <Button variant="secondary" className="text-xs" icon={<RotateCcw className="h-3.5 w-3.5" />} onClick={retryLast} disabled={busy || !lastSent}>
              Retry last (idempotency demo)
            </Button>
            <Button variant="secondary" className="text-xs" icon={<Zap className="h-3.5 w-3.5" />} onClick={spamSend} disabled={busy}>
              Spam send 25 (rate-limit demo)
            </Button>
          </div>
          <div className="max-h-28 overflow-y-auto rounded-lg border border-border-soft bg-bg/60 p-2.5 font-mono text-[11px] leading-relaxed text-text-muted">
            {log.length === 0 && <span className="text-text-faint">&mdash;</span>}
            {log.map((line, i) => (
              <div key={i} className="animate-fade-in">
                {line}
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="relative min-h-0 flex-1">
        <div ref={scrollRef} onScroll={handleMessagesScroll} className="h-full overflow-y-auto px-5 py-4">
          <div className="flex flex-col gap-3">
            {messages.length === 0 && (
              <div className="m-auto flex flex-col items-center gap-2 py-12 text-center">
                <span className="grid h-10 w-10 place-items-center rounded-full bg-surface-2 text-text-faint">
                  <Hash className="h-4 w-4" />
                </span>
                <p className="text-sm text-text-muted">No messages yet — say something below.</p>
              </div>
            )}
            {messages.map((m, i) => {
              const isYou = m.sender_id === profile.userId;
              const senderName = isYou ? "you" : memberNames[m.sender_id] ?? m.sender_id.slice(0, 8);
              return (
                <div
                  key={m.message_id}
                  className={cx("group animate-message-in flex items-end gap-2", isYou ? "justify-end" : "justify-start")}
                  style={{ animationDelay: `${Math.min(i * 12, 240)}ms` }}
                >
                  {!isYou && <Avatar name={senderName} size="sm" className="mb-0.5" />}
                  <div className={cx("flex max-w-[70%] flex-col gap-1", isYou ? "items-end" : "items-start")}>
                    <div
                      className={cx(
                        "rounded-2xl px-4 py-2.5 shadow-sm transition-transform duration-150",
                        isYou ? "rounded-br-md bg-accent text-white" : "rounded-bl-md bg-surface-2 text-text"
                      )}
                    >
                      <div className={cx("mb-0.5 font-mono text-[10.5px]", isYou ? "text-white/70" : "text-text-faint")}>
                        {senderName} &middot; seq {m.sequence} &middot; {new Date(m.created_at).toLocaleTimeString()}
                      </div>
                      <div className="text-sm leading-relaxed break-words">{m.body}</div>
                    </div>
                    <MessageReactions
                      counts={m.reaction_counts}
                      mine={myReactions[m.message_id]}
                      mirrored={!isYou}
                      onToggle={(reaction) => toggleReaction(m.message_id, reaction)}
                    />
                    {isYou && m.message_id === myLastMessageId && seenByFor(m).length > 0 && (
                      <div className="px-1 text-[10.5px] text-text-faint">Seen by {seenByFor(m).join(", ")}</div>
                    )}
                  </div>
                </div>
              );
            })}
            <div ref={messagesEndRef} />
          </div>
        </div>

        {showJumpToLatest && (
          <button
            onClick={jumpToLatest}
            className="animate-fade-in-up absolute bottom-3 left-1/2 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-border bg-surface-2/95 px-3.5 py-1.5 text-xs font-medium text-text shadow-lg backdrop-blur-sm transition-all duration-150 hover:border-accent/50 hover:text-accent active:scale-95"
          >
            <ChevronDown className="h-3.5 w-3.5" />
            Jump to latest
          </button>
        )}
      </div>

      <div className="shrink-0 border-t border-border-soft px-5 py-4">
        <div className="mb-1.5 h-4 px-1 text-xs text-text-faint italic">{typingLabel}</div>
        <form className="flex items-center gap-2.5" onSubmit={handleSend}>
          <Input
            className="flex-1 rounded-full px-4 py-2.5"
            placeholder="Type a message&hellip;"
            value={composeText}
            onChange={(e) => {
              const value = e.target.value;
              setComposeText(value);
              if (value.trim()) sendTypingStart();
              else sendTypingStop();
            }}
          />
          <button
            type="submit"
            disabled={busy || !composeText.trim()}
            className="grid h-10 w-10 shrink-0 place-items-center rounded-full bg-accent text-white transition-all duration-150 hover:bg-accent-strong active:scale-95 disabled:cursor-not-allowed disabled:opacity-40 disabled:active:scale-100"
          >
            <Send className="h-4 w-4" />
          </button>
        </form>
      </div>
    </div>
  );
}

// MessageReactions renders a SmilePlus trigger that reveals the fixed
// quick-react palette, followed by existing reaction pills (clickable to
// toggle your own). The trigger comes first so it has a stable position
// regardless of how many pills exist, and only appears on hover of the
// message row (the "group" class on the row above) so it doesn't clutter
// every message at rest.
function MessageReactions({
  counts,
  mine,
  mirrored,
  onToggle,
}: {
  counts: Record<string, number>;
  mine: Set<string> | undefined;
  /** True for messages aligned to the left (someone else's) — the trigger
   * sits nearest the bubble either way, so it flips to the right and pills
   * read right-to-left outward from it, mirroring the right-aligned case. */
  mirrored: boolean;
  onToggle: (reaction: string) => void;
}) {
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerPos, setPickerPos] = useState<{ top: number; left: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const pickerRef = useRef<HTMLDivElement | null>(null);
  const entries = Object.entries(counts).filter(([, count]) => count > 0);

  // Positioned via document.body portal at the trigger's own screen
  // coordinates instead of CSS `absolute` inside the message list — the
  // message list sits in a scroll container next to a sidebar panel, and an
  // in-flow absolute popover there can end up painted *behind* that
  // sibling panel depending on stacking context, not just z-index. A
  // portal escapes that entirely.
  useEffect(() => {
    if (!pickerOpen) return;
    function place() {
      const rect = triggerRef.current?.getBoundingClientRect();
      if (rect) setPickerPos({ top: rect.top - 8, left: rect.left });
    }
    place();
    function handlePointerDown(e: PointerEvent) {
      const target = e.target as Node;
      if (triggerRef.current?.contains(target) || pickerRef.current?.contains(target)) return;
      setPickerOpen(false);
    }
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    document.addEventListener("pointerdown", handlePointerDown);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
      document.removeEventListener("pointerdown", handlePointerDown);
    };
  }, [pickerOpen]);

  return (
    <div className={cx("flex flex-wrap items-center gap-1", mirrored && "flex-row-reverse")}>
      <button
        ref={triggerRef}
        onClick={() => setPickerOpen((v) => !v)}
        className={cx(
          "grid h-6 w-6 place-items-center rounded-full border text-text-faint opacity-0 transition-all duration-150 group-hover:opacity-100",
          pickerOpen
            ? "opacity-100 border-accent bg-accent-soft text-accent"
            : "border-border-soft hover:border-accent/60 hover:bg-accent-soft hover:text-accent"
        )}
        title="Add a reaction"
      >
        <SmilePlus className="h-3.5 w-3.5" />
      </button>

      {entries.map(([reaction, count]) => {
        const active = mine?.has(reaction) ?? false;
        return (
          <button
            key={reaction}
            onClick={() => onToggle(reaction)}
            className={cx(
              "flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs transition-colors duration-150",
              active
                ? "border-accent/50 bg-accent-soft text-accent"
                : "border-border-soft bg-surface-2/60 text-text-muted hover:border-text-faint"
            )}
            title={active ? "Remove your reaction" : "Add this reaction"}
          >
            <span>{reactionGlyph(reaction)}</span>
            <span className="font-mono text-[10.5px] tabular-nums">{count}</span>
          </button>
        );
      })}

      {pickerOpen &&
        pickerPos &&
        createPortal(
          <div
            ref={pickerRef}
            className="animate-fade-in-up fixed z-[100] flex -translate-y-full gap-0.5 rounded-full border border-border bg-surface-2 px-1.5 py-1 shadow-xl"
            style={{ top: pickerPos.top, left: pickerPos.left }}
          >
            {QUICK_REACT_KEYS.map((reaction) => (
              <button
                key={reaction}
                onClick={() => {
                  onToggle(reaction);
                  setPickerOpen(false);
                }}
                className="grid h-7 w-7 place-items-center rounded-full text-sm transition-transform duration-150 hover:scale-110 hover:bg-white/[0.06] active:scale-95"
                title={reaction}
              >
                {reactionGlyph(reaction)}
              </button>
            ))}
          </div>,
          document.body
        )}
    </div>
  );
}
