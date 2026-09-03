"use client";

// The Playground's right-hand side: a live view of the selected channel as
// the acting actor sees it, the realtime event feed across every actor's
// socket, and the request log. All three update from the same state the
// hook keeps — the channel view additionally patches itself from events
// (new messages, edits, reactions, pins) so what you did on the left shows
// up here without a refresh.
import { useEffect, useRef, useState } from "react";
import { RefreshCw, Trash2 } from "lucide-react";
import { API_BASE, runHttp, type Actor, type RequestRecord } from "@/lib/playground/client";
import type { RecentIds } from "@/lib/playground/features";
import type { RealtimeFrame } from "@/lib/playground/realtime";
import { Badge, Panel, cx } from "@/components/ui";
import { JsonView, specLabel, statusTone } from "./feature-panel";
import type { EventRecord } from "./use-playground";

interface Message {
  message_id: string;
  channel_id: string;
  sequence: number;
  sender_id: string;
  body: string;
  parent_id?: string | null;
  reply_count?: number;
  poll_id?: string | null;
  quoted_message_id?: string | null;
  created_at: string;
  edited_at?: string | null;
  reaction_counts?: Record<string, number>;
  pinned_at?: string | null;
  attachments?: unknown[];
  location?: unknown;
  link_preview?: { title?: string; url?: string } | null;
}

type Tab = "channel" | "events" | "log";

const EVENT_TONES: Record<string, "accent" | "success" | "warning" | "default"> = {
  "message.created": "accent",
  "message.edited": "accent",
  "reaction.updated": "success",
  "typing.updated": "warning",
  "read.updated": "default",
  "custom.event": "success",
  "connection.updated": "warning",
  "poll.vote_updated": "success",
  "message.pin_updated": "accent",
  "message_reminder.due": "warning",
  "unread_reminder.due": "warning",
};

function summarize(frame: RealtimeFrame, name: (id: string) => string): string {
  const who = typeof frame.user_id === "string" ? name(frame.user_id) : typeof frame.sender_id === "string" ? name(frame.sender_id) : typeof frame.actor_id === "string" ? name(frame.actor_id) : "";
  switch (frame.type) {
    case "message.created":
      return `${who}: ${String(frame.body ?? "")}`;
    case "message.edited":
      return `${who} edited: ${String(frame.body ?? "")}`;
    case "reaction.updated":
      return `${who} ${String(frame.action)} ${String(frame.reaction)}`;
    case "typing.updated":
      return `${who} ${frame.typing ? "started" : "stopped"} typing`;
    case "read.updated":
      return `${who} read up to #${String(frame.last_read_sequence)}`;
    case "custom.event":
      return `${who} · ${String(frame.event_type)}`;
    case "connection.updated":
      return `${who} ${frame.connected ? "connected" : "disconnected"}`;
    case "poll.vote_updated":
      return `${who} voted · ${String(frame.total_voters)} voter(s)`;
    case "message.pin_updated":
      return `${who} ${String(frame.action)}`;
    case "message_reminder.due":
      return "reminder due";
    case "unread_reminder.due":
      return `unread: at #${String(frame.last_read_sequence)}, latest #${String(frame.latest_sequence)}`;
    default:
      return "";
  }
}

function timeOf(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function LivePanel({
  actor,
  channelId,
  events,
  requests,
  recent,
  nameOf,
  onSelectMessage,
  onClear,
}: {
  actor: Actor | null;
  channelId: string | null;
  events: EventRecord[];
  requests: RequestRecord[];
  recent: RecentIds;
  nameOf: (userId: string) => string;
  onSelectMessage: (messageId: string) => void;
  onClear: () => void;
}) {
  const [tab, setTab] = useState<Tab>("channel");
  const unseenEvents = useRef(0);
  const [eventBadge, setEventBadge] = useState(0);

  // Count events that arrived while the feed wasn't visible, as a nudge.
  const lastEventId = useRef<number | null>(null);
  useEffect(() => {
    const newest = events[0]?.id ?? null;
    if (newest !== null && newest !== lastEventId.current) {
      lastEventId.current = newest;
      if (tab !== "events") {
        unseenEvents.current += 1;
        setEventBadge(unseenEvents.current);
      }
    }
  }, [events, tab]);
  useEffect(() => {
    if (tab === "events") {
      unseenEvents.current = 0;
      setEventBadge(0);
    }
  }, [tab]);

  return (
    <Panel animate={false} className="flex h-[calc(100vh-11rem)] min-h-[520px] flex-col p-0">
      <div className="flex shrink-0 items-center gap-1 border-b border-border-soft px-3 pt-2">
        {(
          [
            { id: "channel", label: "Channel" },
            { id: "events", label: "Events" },
            { id: "log", label: "Log" },
          ] as { id: Tab; label: string }[]
        ).map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={cx(
              "relative flex items-center gap-1.5 px-3 py-2.5 text-sm transition-colors duration-150",
              tab === t.id ? "font-medium text-accent" : "text-text-muted hover:text-text"
            )}
          >
            {t.label}
            {t.id === "events" && eventBadge > 0 && (
              <span className="rounded-full bg-accent px-1.5 font-mono text-[10px] font-semibold text-bg">{eventBadge}</span>
            )}
            {t.id === "log" && requests.length > 0 && <span className="font-mono text-[10px] text-text-faint">{requests.length}</span>}
            {tab === t.id && <span className="absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-accent" />}
          </button>
        ))}
        {tab !== "channel" && (
          <button onClick={onClear} className="ml-auto rounded-lg p-1.5 text-text-faint transition-colors duration-150 hover:text-danger" title="Clear events and log">
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {tab === "channel" && <ChannelView actor={actor} channelId={channelId} events={events} recent={recent} nameOf={nameOf} onSelectMessage={onSelectMessage} />}
        {tab === "events" && <EventsView events={events} nameOf={nameOf} />}
        {tab === "log" && <LogView requests={requests} />}
      </div>
    </Panel>
  );
}

function ChannelView({
  actor,
  channelId,
  events,
  recent,
  nameOf,
  onSelectMessage,
}: {
  actor: Actor | null;
  channelId: string | null;
  events: EventRecord[];
  recent: RecentIds;
  nameOf: (userId: string) => string;
  onSelectMessage: (messageId: string) => void;
}) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);

  async function load() {
    if (!actor || !channelId) {
      setMessages([]);
      setError(null);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const res = await runHttp(API_BASE, actor.token, { kind: "http", method: "GET", path: `/channels/${channelId}/messages`, query: { limit: 40 } });
      if (!res.ok) {
        const body = res.response as { error?: string } | null;
        setError(body?.error ?? `HTTP ${res.status}`);
        setMessages([]);
        return;
      }
      setMessages(((res.response as Message[]) ?? []).slice().reverse());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => void load(), [actor?.userId, actor?.token, channelId]);

  // Apply realtime frames as they arrive, newest-first list → process the
  // ones not yet seen. Any actor's socket counts (they all get the same
  // channel broadcasts), deduped by message_id.
  const seenEvent = useRef<number>(0);
  useEffect(() => {
    if (!channelId) return;
    const fresh = events.filter((e) => e.id > seenEvent.current).reverse();
    if (fresh.length === 0) return;
    seenEvent.current = events[0]?.id ?? seenEvent.current;
    setMessages((prev) => {
      let next = prev;
      for (const { frame } of fresh) {
        if (frame.channel_id !== channelId) continue;
        const messageId = String(frame.message_id ?? "");
        switch (frame.type) {
          case "message.created":
            if (!next.some((m) => m.message_id === messageId)) {
              next = [
                ...next,
                {
                  message_id: messageId,
                  channel_id: channelId,
                  sequence: Number(frame.sequence ?? 0),
                  sender_id: String(frame.sender_id ?? ""),
                  body: String(frame.body ?? ""),
                  parent_id: (frame.parent_id as string | undefined) ?? null,
                  poll_id: (frame.poll_id as string | undefined) ?? null,
                  created_at: String(frame.created_at ?? new Date().toISOString()),
                  reaction_counts: {},
                },
              ];
            }
            break;
          case "message.edited":
            next = next.map((m) => (m.message_id === messageId ? { ...m, body: String(frame.body ?? m.body), edited_at: String(frame.edited_at ?? "") } : m));
            break;
          case "reaction.updated":
            next = next.map((m) => (m.message_id === messageId ? { ...m, reaction_counts: (frame.reaction_counts as Record<string, number>) ?? {} } : m));
            break;
          case "message.pin_updated":
            next = next.map((m) => (m.message_id === messageId ? { ...m, pinned_at: (frame.pinned_at as string | null) ?? null } : m));
            break;
        }
      }
      return next;
    });
  }, [events, channelId]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [messages.length]);

  if (!channelId) return <Empty>Select or create a channel to see it here.</Empty>;
  if (!actor) return <Empty>Add an actor to view the channel as them.</Empty>;

  return (
    <div className="flex min-h-full flex-col">
      <div className="flex items-center justify-between px-4 pt-3 text-xs text-text-faint">
        <span>
          Viewing as <span className="text-text">{actor.displayName}</span> · click a message to target it
        </span>
        <button onClick={load} className="rounded-lg p-1 transition-colors duration-150 hover:text-text" title="Reload">
          <RefreshCw className={cx("h-3.5 w-3.5", loading && "animate-spin")} />
        </button>
      </div>
      {error && <p className="mx-4 mt-3 rounded-xl border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger">{error}</p>}
      {!error && messages.length === 0 && !loading && <Empty>No messages yet — run Send message.</Empty>}
      <div className="flex flex-col gap-1.5 px-3 py-3">
        {messages.map((m) => {
          const mine = m.sender_id === actor.userId;
          const selected = recent.message === m.message_id;
          const reactions = Object.entries(m.reaction_counts ?? {}).filter(([, n]) => n > 0);
          return (
            <button
              key={m.message_id}
              onClick={() => onSelectMessage(m.message_id)}
              className={cx(
                "flex flex-col gap-1 rounded-xl border px-3 py-2 text-left transition-colors duration-150",
                selected ? "border-accent/60 bg-accent-soft" : "border-transparent hover:border-border hover:bg-surface-2",
                mine && !selected && "bg-surface-2/60"
              )}
              title={m.message_id}
            >
              <div className="flex items-center gap-2 text-[11px] text-text-faint">
                <span className={cx("font-medium", mine ? "text-accent" : "text-text-muted")}>{nameOf(m.sender_id)}</span>
                <span className="font-mono">#{m.sequence}</span>
                <span>{new Date(m.created_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</span>
                {m.edited_at && <span>· edited</span>}
                {m.pinned_at && <span className="text-warning">· pinned</span>}
                {m.parent_id && <span>· reply</span>}
                {m.quoted_message_id && <span>· quote</span>}
                {m.poll_id && <span className="text-success">· poll</span>}
                {(m.attachments?.length ?? 0) > 0 && <span>· attachment</span>}
                {m.location ? <span>· location</span> : null}
              </div>
              <div className="whitespace-pre-wrap break-words text-sm text-text">{m.body || <span className="italic text-text-faint">(empty)</span>}</div>
              {m.link_preview?.title && <div className="truncate text-xs text-text-muted">↗ {m.link_preview.title}</div>}
              {reactions.length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {reactions.map(([r, n]) => (
                    <span key={r} className="rounded-full border border-border bg-surface px-2 py-0.5 font-mono text-[10px] text-text-muted">
                      {r} {n}
                    </span>
                  ))}
                </div>
              )}
            </button>
          );
        })}
        <div ref={bottomRef} />
      </div>
    </div>
  );
}

function EventsView({ events, nameOf }: { events: EventRecord[]; nameOf: (userId: string) => string }) {
  const [filter, setFilter] = useState("");
  const [open, setOpen] = useState<number | null>(null);
  const shown = filter ? events.filter((e) => e.frame.type.includes(filter.trim())) : events;

  return (
    <div>
      <div className="px-3 pt-3">
        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="filter by type, e.g. typing"
          className="w-full rounded-lg border border-border bg-surface-2 px-3 py-1.5 font-mono text-xs text-text placeholder:text-text-faint outline-none focus:border-accent/60"
        />
      </div>
      {shown.length === 0 && (
        <Empty>
          {events.length === 0
            ? "Frames received on any actor's realtime connection appear here. Send a message to see message.created."
            : "No events match that filter."}
        </Empty>
      )}
      <div className="flex flex-col gap-1 px-3 py-3">
        {shown.map((e) => (
          <div key={e.id} className="rounded-xl border border-border-soft">
            <button onClick={() => setOpen(open === e.id ? null : e.id)} className="flex w-full flex-col gap-1 px-3 py-2 text-left">
              <div className="flex items-center gap-2">
                <Badge tone={EVENT_TONES[e.frame.type] ?? "default"} className="px-2 py-0.5 text-[10px]">
                  {e.frame.type}
                </Badge>
                <span className="ml-auto text-[11px] text-text-faint">
                  → {e.actor.displayName} · {timeOf(e.at)}
                </span>
              </div>
              <div className="truncate text-xs text-text-muted">{summarize(e.frame, nameOf)}</div>
            </button>
            {open === e.id && <JsonView value={e.frame} className="mx-3 mb-3 max-h-64" />}
          </div>
        ))}
      </div>
    </div>
  );
}

function LogView({ requests }: { requests: RequestRecord[] }) {
  const [open, setOpen] = useState<number | null>(null);
  if (requests.length === 0) return <Empty>Every request the Playground makes as an actor is logged here.</Empty>;
  return (
    <div className="flex flex-col gap-1 px-3 py-3">
      {requests.map((r) => {
        const status = r.result?.status ?? -1;
        return (
          <div key={r.id} className="rounded-xl border border-border-soft">
            <button onClick={() => setOpen(open === r.id ? null : r.id)} className="flex w-full items-center gap-2 px-3 py-2 text-left">
              <Badge tone={statusTone(status)} className="px-2 py-0.5 text-[10px]">
                {status === 0 ? "sent" : status < 0 ? "err" : status}
              </Badge>
              <span className="min-w-0 flex-1 truncate font-mono text-xs text-text">{specLabel(r.spec)}</span>
              <span className="shrink-0 text-[11px] text-text-faint">
                {r.actor.displayName} · {timeOf(r.at)}
              </span>
            </button>
            {open === r.id && (
              <div className="flex flex-col gap-2 px-3 pb-3">
                {r.spec.kind === "http" && r.spec.body !== undefined && <JsonView value={r.spec.body} className="max-h-40" />}
                <JsonView value={r.result?.response ?? null} className="max-h-64" />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return <p className="px-6 py-10 text-center text-sm leading-relaxed text-text-faint">{children}</p>;
}
