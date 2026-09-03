"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  BarChart3,
  Bookmark as BookmarkIcon,
  Check,
  ChevronDown,
  FolderPlus,
  Hash,
  History,
  Languages,
  Pencil,
  Pin,
  PinOff,
  PlugZap,
  Plus,
  Reply,
  RotateCcw,
  Send,
  SmilePlus,
  Terminal,
  Trash2,
  Unplug,
  Wifi,
  WifiOff,
  X,
  Zap,
} from "lucide-react";
import {
  addReaction,
  clearPollVotes,
  createBookmark,
  createBookmarkFolder,
  createPoll,
  deleteBookmark,
  deleteBookmarkFolder,
  editMessage,
  getPoll,
  listBookmarkFolders,
  listBookmarks,
  listMembers,
  listMessages,
  listPinnedMessages,
  listReadState,
  markRead,
  moveBookmark,
  pinMessage,
  removeReaction,
  renameBookmarkFolder,
  sendMessage,
  translateMessage,
  unpinMessage,
  votePoll,
  ApiError,
} from "@/lib/api";
import { QUICK_REACT_KEYS, reactionGlyph } from "@/lib/reactions";
import { REGION_ENDPOINTS } from "@/lib/regions";
import type { Bookmark, BookmarkFolder, ChannelSummary, Message, Poll, Profile, ReactionSummary } from "@/lib/types";
import { Avatar, Badge, Button, Input, Modal, cx } from "@/components/ui";

type WsStatus = "connecting" | "open" | "closed";

interface DeliveryFrame {
  type: string;
  channel_id: string;
  message_id: string;
  sequence: number;
  sender_id: string;
  body: string;
  created_at: string;
  parent_id?: string | null;
  /** The parent message's fresh reply_count, present only when this
   * delivered message is itself a reply (parent_id set) — lets the local
   * copy of the parent bubble update in place without a re-fetch. */
  parent_reply_count?: number | null;
  poll_id?: string | null;
}

interface PollVoteDeliveryFrame {
  type: string;
  channel_id: string;
  poll_id: string;
  actor_id: string;
  options: { option_id: string; vote_count: number }[];
  total_voters: number;
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

interface MessageEditDeliveryFrame {
  type: string;
  channel_id: string;
  message_id: string;
  sender_id: string;
  body: string;
  edited_at: string;
}

interface MessagePinDeliveryFrame {
  type: string;
  channel_id: string;
  message_id: string;
  actor_id: string;
  action: "pinned" | "unpinned";
  pinned_at: string | null;
  pinned_by: string | null;
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
  const [replyTo, setReplyTo] = useState<Message | null>(null);
  // Which message (if any) is currently in inline-edit mode, and its
  // in-progress draft text — only one message can be edited at a time.
  const [editingMessageId, setEditingMessageId] = useState<string | null>(null);
  const [editText, setEditText] = useState("");
  // Polls are a separate resource, never inlined on a message (see
  // lib/types.ts's Poll) — keyed by poll_id, lazily fetched (see the
  // "fetch any poll_id we haven't seen yet" effect below) and kept current
  // by poll.vote_updated frames.
  const [polls, setPolls] = useState<Record<string, Poll>>({});
  const [pollBuilderOpen, setPollBuilderOpen] = useState(false);
  const [pollQuestion, setPollQuestion] = useState("");
  const [pollOptionInputs, setPollOptionInputs] = useState<string[]>(["", ""]);
  const [pollMultiSelect, setPollMultiSelect] = useState(false);
  const [pollClosesInMinutes, setPollClosesInMinutes] = useState("");
  // Pinned messages are channel-shared (see lib/types.ts's Message.pinned_at)
  // — this is a dedicated fetch (not just "messages currently loaded that
  // happen to have pinned_at set") because a pinned message can be far
  // enough back in history that it isn't loaded, the same reason polls are
  // fetched separately rather than expected inline.
  const [pinnedMessages, setPinnedMessages] = useState<Message[]>([]);
  const [pinnedPanelOpen, setPinnedPanelOpen] = useState(false);
  // Bookmarks are private to the CURRENT user and never broadcast over the
  // websocket (see internal/bookmarks' package doc on the Go side) — unlike
  // pinnedMessages above, there's no live frame to keep this in sync;
  // every mutation below (toggleBookmark, folder CRUD, move) updates this
  // state directly from its own API response. Loaded once per profile
  // (see the effect below), not per channel — a bookmark can reference a
  // message in any channel this user belongs to.
  const [bookmarks, setBookmarks] = useState<Bookmark[]>([]);
  const [bookmarkFolders, setBookmarkFolders] = useState<BookmarkFolder[]>([]);
  const [bookmarksPanelOpen, setBookmarksPanelOpen] = useState(false);
  const [newFolderName, setNewFolderName] = useState("");
  // Per-message translation result, keyed by message_id — set once a
  // translate request resolves (see translateMessageAction) and shown
  // inline under the original body with a "Hide translation" toggle;
  // absent for a message that's never been translated. Private to this
  // viewer only, same as bookmarks: the server never broadcasts a
  // translation to other channel members.
  const [messageTranslations, setMessageTranslations] = useState<Record<string, { text: string; sourceLang: string; targetLang: string; cached: boolean }>>({});
  const [translatingMessageId, setTranslatingMessageId] = useState<string | null>(null);
  const [lastSent, setLastSent] = useState<{ clientMessageId: string; body: string; parentId?: string } | null>(null);
  const [highlightedMessageId, setHighlightedMessageId] = useState<string | null>(null);
  const [log, setLog] = useState<string[]>([]);
  const [devToolsOpen, setDevToolsOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const messagesEndRef = useRef<HTMLDivElement | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const messageRefs = useRef<Record<string, HTMLDivElement | null>>({});
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
            setMessages((prev) => {
              const merged = mergeMessages(prev, [
                {
                  message_id: frame.message_id,
                  channel_id: frame.channel_id,
                  sequence: frame.sequence,
                  sender_id: frame.sender_id,
                  client_message_id: "",
                  body: frame.body,
                  created_at: frame.created_at,
                  parent_id: frame.parent_id ?? null,
                  reply_count: 0,
                  poll_id: frame.poll_id ?? null,
                  reaction_counts: {},
                  latest_reactions: [],
                },
              ]);
              // frame is a reply: patch its parent's denormalized
              // reply_count in place rather than waiting for a re-fetch —
              // mirrors how reaction.updated below patches reaction state.
              if (frame.parent_id != null && frame.parent_reply_count != null) {
                const parentId = frame.parent_id;
                const freshCount = frame.parent_reply_count;
                return merged.map((m) => (m.message_id === parentId ? { ...m, reply_count: freshCount } : m));
              }
              return merged;
            });
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
            return;
          }

          if (raw.type === "message.edited") {
            const frame = raw as unknown as MessageEditDeliveryFrame;
            appendLog(`message edited: ${frame.message_id.slice(0, 8)}…`);
            setMessages((prev) =>
              prev.map((m) => (m.message_id === frame.message_id ? { ...m, body: frame.body, edited_at: frame.edited_at } : m))
            );
            return;
          }

          if (raw.type === "message.pin_updated") {
            const frame = raw as unknown as MessagePinDeliveryFrame;
            appendLog(`message ${frame.action}: ${frame.message_id.slice(0, 8)}…`);
            setMessages((prev) =>
              prev.map((m) => (m.message_id === frame.message_id ? { ...m, pinned_at: frame.pinned_at, pinned_by: frame.pinned_by } : m))
            );
            // Re-fetch rather than patch pinnedMessages in place: the
            // pinned list can gain an entry for a message that was never
            // loaded into `messages` (far back in history), so there's no
            // reliable local copy to splice in. Uses `p` (the Profile this
            // socket was opened for) rather than the outer profile/apiBase
            // so this stays correct even though connectWs's memoization
            // doesn't list them as dependencies.
            listPinnedMessages(REGION_ENDPOINTS[p.region].apiBase, p.token, frame.channel_id)
              .then(setPinnedMessages)
              .catch(() => {
                // Best-effort: the pinned panel just lags until reopened.
              });
            return;
          }

          if (raw.type === "poll.vote_updated") {
            const frame = raw as unknown as PollVoteDeliveryFrame;
            appendLog(`poll vote updated: ${frame.poll_id.slice(0, 8)}…`);
            // Only tallies/total_voters are broadcast here — never who voted
            // for what (see lib/types.ts's Poll.voted_option_ids), so the
            // viewer's own selection is left untouched; it only ever changes
            // from that viewer's own vote/retract call.
            setPolls((prev) => {
              const existing = prev[frame.poll_id];
              if (!existing) return prev;
              const tallyByOption = new Map(frame.options.map((o) => [o.option_id, o.vote_count]));
              return {
                ...prev,
                [frame.poll_id]: {
                  ...existing,
                  total_voters: frame.total_voters,
                  options: existing.options.map((o) => ({ ...o, vote_count: tallyByOption.get(o.option_id) ?? o.vote_count })),
                },
              };
            });
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
    setMessageTranslations({});
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
    setPinnedMessages([]);
    listPinnedMessages(apiBase, profile.token, channelId)
      .then(setPinnedMessages)
      .catch((err) => appendLog(`pinned messages load failed: ${err}`));
    connectWs(profile);
    return () => {
      wsRef.current?.close();
      for (const timer of Object.values(typingClearTimersRef.current)) clearTimeout(timer);
      typingClearTimersRef.current = {};
      if (typingStopTimerRef.current) clearTimeout(typingStopTimerRef.current);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelId]);

  // Bookmarks are private to profile.userId and can reference a message in
  // ANY channel they belong to — loaded once per signed-in profile, not
  // re-fetched on every channel switch the way pinnedMessages above is.
  const refreshBookmarks = useCallback(() => {
    listBookmarks(apiBase, profile.token)
      .then(setBookmarks)
      .catch((err) => appendLog(`bookmarks load failed: ${err}`));
  }, [apiBase, profile.token, appendLog]);

  const refreshBookmarkFolders = useCallback(() => {
    listBookmarkFolders(apiBase, profile.token)
      .then(setBookmarkFolders)
      .catch((err) => appendLog(`bookmark folders load failed: ${err}`));
  }, [apiBase, profile.token, appendLog]);

  useEffect(() => {
    refreshBookmarks();
    refreshBookmarkFolders();
  }, [refreshBookmarks, refreshBookmarkFolders]);

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

  // translateMessageAction requires the app's "translations" capability
  // (403 otherwise, same as any other gated capability here — this UI has
  // no client-side awareness of which capabilities are on, it just shows
  // the button and surfaces whatever the server says). A 403/503/502 all
  // get logged the same way a failed reaction does rather than shown as a
  // dedicated error UI, since this is a demo harness, not the product.
  async function translateMessageAction(messageId: string, targetLang: string) {
    setTranslatingMessageId(messageId);
    try {
      const result = await translateMessage(apiBase, profile.token, channelId, messageId, targetLang);
      setMessageTranslations((prev) => ({
        ...prev,
        [messageId]: { text: result.translated_text, sourceLang: result.source_lang, targetLang: result.target_lang, cached: result.cached },
      }));
      appendLog(`translated message ${messageId.slice(0, 8)}… to ${targetLang}${result.cached ? " (cached)" : ""}`);
    } catch (err) {
      appendLog(err instanceof ApiError ? `translate failed: ${err.status} ${err.message}` : `translate failed: ${err}`);
    } finally {
      setTranslatingMessageId(null);
    }
  }

  function startEdit(m: Message) {
    setEditingMessageId(m.message_id);
    setEditText(m.body);
  }

  function cancelEdit() {
    setEditingMessageId(null);
    setEditText("");
  }

  // There's no client-facing way to know ahead of time whether this app has
  // message_edit_enabled on, or whether the API will still consider you the
  // sender by the time this lands — the Edit button is always shown on your
  // own messages, and a 403 from either check surfaces the same way any
  // other API rejection does here (appendLog), rather than being predicted
  // client-side.
  async function saveEdit(messageId: string) {
    const body = editText.trim();
    if (!body) return;
    try {
      const updated = await editMessage(apiBase, profile.token, channelId, messageId, body);
      setMessages((prev) => prev.map((m) => (m.message_id === messageId ? { ...m, body: updated.body, edited_at: updated.edited_at } : m)));
      cancelEdit();
    } catch (err) {
      appendLog(err instanceof ApiError ? `edit failed: ${err.status} ${err.message}` : `edit failed: ${err}`);
    }
  }

  // Idempotent either direction (see internal/messages.Repo.Pin/Unpin on
  // the Go side) — any channel member may pin or unpin, not just the
  // sender, so this toggles off m.pinned_at rather than checking isYou.
  async function togglePin(m: Message) {
    try {
      const updated = m.pinned_at
        ? await unpinMessage(apiBase, profile.token, channelId, m.message_id)
        : await pinMessage(apiBase, profile.token, channelId, m.message_id);
      setMessages((prev) =>
        prev.map((x) => (x.message_id === m.message_id ? { ...x, pinned_at: updated.pinned_at, pinned_by: updated.pinned_by } : x))
      );
      setPinnedMessages((prev) =>
        updated.pinned_at ? [updated, ...prev.filter((x) => x.message_id !== m.message_id)] : prev.filter((x) => x.message_id !== m.message_id)
      );
    } catch (err) {
      appendLog(err instanceof ApiError ? `pin failed: ${err.status} ${err.message}` : `pin failed: ${err}`);
    }
  }

  // Bookmarks are private and never broadcast over the websocket, so —
  // unlike togglePin above — this is the only place bookmark state ever
  // changes; there's no delivery frame to reconcile against.
  function bookmarkFor(messageId: string): Bookmark | undefined {
    return bookmarks.find((b) => b.message_id === messageId);
  }

  async function toggleBookmark(m: Message) {
    const existing = bookmarkFor(m.message_id);
    try {
      if (existing) {
        await deleteBookmark(apiBase, profile.token, existing.bookmark_id);
        setBookmarks((prev) => prev.filter((b) => b.bookmark_id !== existing.bookmark_id));
      } else {
        const created = await createBookmark(apiBase, profile.token, m.channel_id, m.message_id);
        setBookmarks((prev) => [created, ...prev.filter((b) => b.bookmark_id !== created.bookmark_id)]);
      }
    } catch (err) {
      appendLog(err instanceof ApiError ? `bookmark failed: ${err.status} ${err.message}` : `bookmark failed: ${err}`);
    }
  }

  async function createFolder() {
    const name = newFolderName.trim();
    if (!name) return;
    try {
      const folder = await createBookmarkFolder(apiBase, profile.token, name);
      setBookmarkFolders((prev) => [folder, ...prev]);
      setNewFolderName("");
    } catch (err) {
      appendLog(err instanceof ApiError ? `create folder failed: ${err.status} ${err.message}` : `create folder failed: ${err}`);
    }
  }

  async function renameFolder(folderId: string, name: string) {
    try {
      const folder = await renameBookmarkFolder(apiBase, profile.token, folderId, name);
      setBookmarkFolders((prev) => prev.map((f) => (f.folder_id === folderId ? folder : f)));
    } catch (err) {
      appendLog(err instanceof ApiError ? `rename folder failed: ${err.status} ${err.message}` : `rename folder failed: ${err}`);
    }
  }

  // Bookmarks that were filed in this folder are left in place, un-filed
  // back to "unfiled" server-side — refreshBookmarks() below picks that up
  // rather than this trying to patch folder_id locally for each of them.
  async function removeFolder(folderId: string) {
    try {
      await deleteBookmarkFolder(apiBase, profile.token, folderId);
      setBookmarkFolders((prev) => prev.filter((f) => f.folder_id !== folderId));
      refreshBookmarks();
    } catch (err) {
      appendLog(err instanceof ApiError ? `delete folder failed: ${err.status} ${err.message}` : `delete folder failed: ${err}`);
    }
  }

  async function moveBookmarkToFolder(bookmarkId: string, folderId: string | undefined) {
    try {
      const moved = await moveBookmark(apiBase, profile.token, bookmarkId, folderId);
      setBookmarks((prev) => prev.map((b) => (b.bookmark_id === bookmarkId ? moved : b)));
    } catch (err) {
      appendLog(err instanceof ApiError ? `move bookmark failed: ${err.status} ${err.message}` : `move bookmark failed: ${err}`);
    }
  }

  async function removeBookmark(bookmarkId: string) {
    try {
      await deleteBookmark(apiBase, profile.token, bookmarkId);
      setBookmarks((prev) => prev.filter((b) => b.bookmark_id !== bookmarkId));
    } catch (err) {
      appendLog(err instanceof ApiError ? `delete bookmark failed: ${err.status} ${err.message}` : `delete bookmark failed: ${err}`);
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
    const parentId = replyTo?.message_id;
    setBusy(true);
    sendTypingStop();
    try {
      const msg = await sendMessage(apiBase, profile.token, channelId, clientMessageId, body, parentId);
      setLastSent({ clientMessageId, body, parentId });
      setComposeText("");
      setReplyTo(null);
      appendLog(
        parentId
          ? `sent reply: seq ${msg.sequence}, parent ${parentId.slice(0, 8)}… (client_message_id ${clientMessageId.slice(0, 8)}…)`
          : `sent: seq ${msg.sequence} (client_message_id ${clientMessageId.slice(0, 8)}…)`
      );
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
      const msg = await sendMessage(apiBase, profile.token, channelId, lastSent.clientMessageId, lastSent.body, lastSent.parentId);
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

  const messagesById = useMemo(() => new Map(messages.map((m) => [m.message_id, m])), [messages]);

  // Lazily fetch any poll a visible message attaches that isn't loaded yet
  // — a poll is never inlined on the message itself (see lib/types.ts's
  // Message.poll_id), so this is what turns "here's a poll_id" into
  // rendered question/options/tallies.
  useEffect(() => {
    const missing = new Set<string>();
    for (const m of messages) {
      if (m.poll_id && !polls[m.poll_id]) missing.add(m.poll_id);
    }
    if (missing.size === 0) return;
    missing.forEach((pollId) => {
      getPoll(apiBase, profile.token, channelId, pollId)
        .then((poll) => setPolls((prev) => ({ ...prev, [pollId]: poll })))
        .catch((err) => appendLog(err instanceof ApiError ? `load poll failed: ${err.status} ${err.message}` : `load poll failed: ${err}`));
    });
  }, [messages, polls, apiBase, profile.token, channelId, appendLog]);

  async function votePollOption(poll: Poll, optionId: string) {
    try {
      let ids: string[];
      if (poll.multi_select) {
        const current = new Set(poll.voted_option_ids ?? []);
        if (current.has(optionId)) current.delete(optionId);
        else current.add(optionId);
        ids = Array.from(current);
      } else {
        ids = [optionId];
      }
      const state = ids.length === 0
        ? await clearPollVotes(apiBase, profile.token, channelId, poll.poll_id)
        : await votePoll(apiBase, profile.token, channelId, poll.poll_id, ids);
      setPolls((prev) => ({ ...prev, [poll.poll_id]: { ...prev[poll.poll_id], ...state } }));
    } catch (err) {
      appendLog(err instanceof ApiError ? `vote failed: ${err.status} ${err.message}` : `vote failed: ${err}`);
    }
  }

  async function retractPollVote(poll: Poll) {
    try {
      const state = await clearPollVotes(apiBase, profile.token, channelId, poll.poll_id);
      setPolls((prev) => ({ ...prev, [poll.poll_id]: { ...prev[poll.poll_id], ...state } }));
    } catch (err) {
      appendLog(err instanceof ApiError ? `retract vote failed: ${err.status} ${err.message}` : `retract vote failed: ${err}`);
    }
  }

  function updatePollOptionInput(index: number, value: string) {
    setPollOptionInputs((prev) => prev.map((v, i) => (i === index ? value : v)));
  }

  function addPollOptionInput() {
    setPollOptionInputs((prev) => (prev.length >= 10 ? prev : [...prev, ""]));
  }

  function removePollOptionInput(index: number) {
    setPollOptionInputs((prev) => (prev.length <= 2 ? prev : prev.filter((_, i) => i !== index)));
  }

  function closePollBuilder() {
    setPollBuilderOpen(false);
    setPollQuestion("");
    setPollOptionInputs(["", ""]);
    setPollMultiSelect(false);
    setPollClosesInMinutes("");
  }

  const pollOptionLabels = pollOptionInputs.map((o) => o.trim()).filter(Boolean);
  const pollBuilderValid =
    pollQuestion.trim().length > 0 &&
    pollOptionLabels.length >= 2 &&
    new Set(pollOptionLabels.map((o) => o.toLowerCase())).size === pollOptionLabels.length;

  // Creates the poll as a standalone entity, then immediately sends a
  // message attaching it (poll_id) — a poll only becomes visible to other
  // members once some message references it, same as the real API's
  // design (see internal/polls' package doc on the Go side).
  async function submitPoll(e: React.FormEvent) {
    e.preventDefault();
    if (!pollBuilderValid) return;
    setBusy(true);
    try {
      const closesAt = pollClosesInMinutes.trim()
        ? new Date(Date.now() + Number(pollClosesInMinutes) * 60_000).toISOString()
        : undefined;
      const poll = await createPoll(apiBase, profile.token, channelId, pollQuestion.trim(), pollOptionLabels, pollMultiSelect, closesAt);
      setPolls((prev) => ({ ...prev, [poll.poll_id]: poll }));
      const clientMessageId = crypto.randomUUID();
      await sendMessage(apiBase, profile.token, channelId, clientMessageId, poll.question, undefined, poll.poll_id);
      appendLog(`poll created and sent: ${poll.poll_id.slice(0, 8)}…`);
      closePollBuilder();
    } catch (err) {
      appendLog(err instanceof ApiError ? `create poll failed: ${err.status} ${err.message}` : `create poll failed: ${err}`);
    } finally {
      setBusy(false);
    }
  }

  function scrollToMessage(messageId: string) {
    const el = messageRefs.current[messageId];
    if (!el) return;
    el.scrollIntoView({ behavior: "smooth", block: "center" });
    setHighlightedMessageId(messageId);
    setTimeout(() => setHighlightedMessageId((cur) => (cur === messageId ? null : cur)), 1200);
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
          onClick={() => setPinnedPanelOpen(true)}
          className="relative grid h-8 w-8 place-items-center rounded-lg text-text-faint transition-colors duration-150 hover:bg-white/[0.05] hover:text-text"
          title="Pinned messages"
        >
          <Pin className="h-4 w-4" />
          {pinnedMessages.length > 0 && (
            <span className="absolute -right-0.5 -top-0.5 grid h-3.5 min-w-3.5 place-items-center rounded-full bg-accent px-0.5 text-[9px] font-semibold text-white">
              {pinnedMessages.length}
            </span>
          )}
        </button>
        <button
          onClick={() => setBookmarksPanelOpen(true)}
          className="grid h-8 w-8 place-items-center rounded-lg text-text-faint transition-colors duration-150 hover:bg-white/[0.05] hover:text-text"
          title="Bookmarks"
        >
          <BookmarkIcon className="h-4 w-4" />
        </button>
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
              const parent = m.parent_id ? messagesById.get(m.parent_id) : undefined;
              const parentSenderName = parent
                ? parent.sender_id === profile.userId
                  ? "you"
                  : memberNames[parent.sender_id] ?? parent.sender_id.slice(0, 8)
                : null;
              return (
                <div
                  key={m.message_id}
                  className={cx("group animate-message-in flex items-end gap-2", isYou ? "justify-end" : "justify-start")}
                  style={{ animationDelay: `${Math.min(i * 12, 240)}ms` }}
                >
                  {!isYou && <Avatar name={senderName} size="sm" className="mb-0.5" />}
                  <div className={cx("flex max-w-[70%] flex-col gap-1", isYou ? "items-end" : "items-start")}>
                    {m.parent_id && (
                      <button
                        type="button"
                        onClick={() => parent && scrollToMessage(parent.message_id)}
                        disabled={!parent}
                        className={cx(
                          "flex max-w-full items-center gap-1 truncate rounded-md px-1.5 py-0.5 text-[10.5px] text-text-faint",
                          parent && "cursor-pointer hover:bg-white/[0.05] hover:text-text-muted"
                        )}
                        title={parent ? "Jump to original message" : "Original message not loaded"}
                      >
                        <span aria-hidden>&#8618;</span>
                        <span className="truncate">
                          replying to {parentSenderName ?? "a message"}
                          {parent ? `: ${parent.body}` : ""}
                        </span>
                      </button>
                    )}
                    <div
                      ref={(el) => {
                        messageRefs.current[m.message_id] = el;
                      }}
                      className={cx(
                        "rounded-2xl px-4 py-2.5 shadow-sm transition-all duration-150",
                        isYou ? "rounded-br-md bg-accent text-white" : "rounded-bl-md bg-surface-2 text-text",
                        highlightedMessageId === m.message_id && "ring-2 ring-accent ring-offset-2 ring-offset-bg"
                      )}
                    >
                      <div className={cx("mb-0.5 font-mono text-[10.5px]", isYou ? "text-white/70" : "text-text-faint")}>
                        {senderName} &middot; seq {m.sequence} &middot; {new Date(m.created_at).toLocaleTimeString()}
                        {m.edited_at && <span title={`Edited ${new Date(m.edited_at).toLocaleTimeString()}`}> &middot; edited</span>}
                        {m.pinned_at && (
                          <span title={`Pinned ${new Date(m.pinned_at).toLocaleTimeString()}`}>
                            {" "}
                            &middot; <Pin className="inline h-2.5 w-2.5 -translate-y-px" /> pinned
                          </span>
                        )}
                      </div>
                      {editingMessageId === m.message_id ? (
                        <div className="flex flex-col gap-1.5">
                          <Input
                            autoFocus
                            value={editText}
                            onChange={(e) => setEditText(e.target.value)}
                            onKeyDown={(e) => {
                              if (e.key === "Enter" && !e.shiftKey) {
                                e.preventDefault();
                                saveEdit(m.message_id);
                              } else if (e.key === "Escape") {
                                cancelEdit();
                              }
                            }}
                            className="border-white/30 bg-white/10 text-sm text-white placeholder:text-white/60"
                          />
                          <div className="flex justify-end gap-1.5">
                            <button
                              onClick={cancelEdit}
                              className="grid h-6 w-6 place-items-center rounded-full bg-white/10 text-white/80 hover:bg-white/20"
                              title="Cancel edit"
                            >
                              <X className="h-3.5 w-3.5" />
                            </button>
                            <button
                              onClick={() => saveEdit(m.message_id)}
                              disabled={!editText.trim()}
                              className="grid h-6 w-6 place-items-center rounded-full bg-white/10 text-white/80 hover:bg-white/20 disabled:opacity-40"
                              title="Save edit"
                            >
                              <Check className="h-3.5 w-3.5" />
                            </button>
                          </div>
                        </div>
                      ) : m.poll_id ? (
                        <PollCard poll={polls[m.poll_id]} isYou={isYou} onVote={votePollOption} onRetract={retractPollVote} />
                      ) : (
                        <div className="text-sm leading-relaxed break-words">{m.body}</div>
                      )}
                      {!m.poll_id && messageTranslations[m.message_id] && (
                        <div className="mt-1.5 flex items-start gap-1.5 border-t border-white/10 pt-1.5 text-sm leading-relaxed break-words text-inherit/90">
                          <Languages className="mt-0.5 h-3.5 w-3.5 shrink-0 opacity-60" />
                          <div>
                            {messageTranslations[m.message_id].text}
                            <button
                              onClick={() =>
                                setMessageTranslations((prev) => {
                                  const next = { ...prev };
                                  delete next[m.message_id];
                                  return next;
                                })
                              }
                              className="ml-2 text-[10.5px] font-medium opacity-60 underline-offset-2 hover:underline"
                            >
                              Hide translation
                            </button>
                          </div>
                        </div>
                      )}
                    </div>
                    {editingMessageId !== m.message_id && (
                      <div className={cx("flex items-center gap-1", isYou && "flex-row-reverse")}>
                        {isYou && (
                          <button
                            onClick={() => startEdit(m)}
                            className="grid h-6 w-6 place-items-center rounded-full border border-border-soft text-text-faint opacity-0 transition-all duration-150 hover:border-accent/60 hover:bg-accent-soft hover:text-accent group-hover:opacity-100"
                            title="Edit"
                          >
                            <Pencil className="h-3.5 w-3.5" />
                          </button>
                        )}
                        <button
                          onClick={() => setReplyTo(m)}
                          className="grid h-6 w-6 place-items-center rounded-full border border-border-soft text-text-faint opacity-0 transition-all duration-150 hover:border-accent/60 hover:bg-accent-soft hover:text-accent group-hover:opacity-100"
                          title="Reply"
                        >
                          <Reply className="h-3.5 w-3.5" />
                        </button>
                        <MessageReactions
                          counts={m.reaction_counts}
                          mine={myReactions[m.message_id]}
                          mirrored={!isYou}
                          onToggle={(reaction) => toggleReaction(m.message_id, reaction)}
                        />
                        {!m.poll_id && (
                          <MessageTranslate
                            busy={translatingMessageId === m.message_id}
                            onSelect={(lang) => translateMessageAction(m.message_id, lang)}
                          />
                        )}
                        <button
                          onClick={() => togglePin(m)}
                          className={cx(
                            "grid h-6 w-6 place-items-center rounded-full border text-text-faint opacity-0 transition-all duration-150 hover:border-accent/60 hover:bg-accent-soft hover:text-accent group-hover:opacity-100",
                            m.pinned_at ? "border-accent/60 bg-accent-soft text-accent opacity-100" : "border-border-soft"
                          )}
                          title={m.pinned_at ? "Unpin" : "Pin"}
                        >
                          {m.pinned_at ? <PinOff className="h-3.5 w-3.5" /> : <Pin className="h-3.5 w-3.5" />}
                        </button>
                        <button
                          onClick={() => toggleBookmark(m)}
                          className={cx(
                            "grid h-6 w-6 place-items-center rounded-full border text-text-faint opacity-0 transition-all duration-150 hover:border-accent/60 hover:bg-accent-soft hover:text-accent group-hover:opacity-100",
                            bookmarkFor(m.message_id) ? "border-accent/60 bg-accent-soft text-accent opacity-100" : "border-border-soft"
                          )}
                          title={bookmarkFor(m.message_id) ? "Remove bookmark" : "Bookmark"}
                        >
                          <BookmarkIcon className={cx("h-3.5 w-3.5", bookmarkFor(m.message_id) && "fill-current")} />
                        </button>
                      </div>
                    )}
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
        {replyTo && (
          <div className="mb-2 flex items-center gap-2 rounded-lg border border-border-soft bg-surface-2/60 px-3 py-1.5 text-xs text-text-muted">
            <Reply className="h-3.5 w-3.5 shrink-0 text-text-faint" />
            <span className="min-w-0 flex-1 truncate">
              Replying to{" "}
              {replyTo.sender_id === profile.userId ? "yourself" : memberNames[replyTo.sender_id] ?? replyTo.sender_id.slice(0, 8)}:{" "}
              {replyTo.body}
            </span>
            <button
              type="button"
              onClick={() => setReplyTo(null)}
              className="grid h-5 w-5 shrink-0 place-items-center rounded-full text-text-faint transition-colors duration-150 hover:bg-white/[0.06] hover:text-text"
              title="Cancel reply"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        )}
        <div className="mb-1.5 h-4 px-1 text-xs text-text-faint italic">{typingLabel}</div>
        <form className="flex items-center gap-2.5" onSubmit={handleSend}>
          <button
            type="button"
            onClick={() => setPollBuilderOpen(true)}
            className="grid h-10 w-10 shrink-0 place-items-center rounded-full border border-border-soft text-text-faint transition-colors duration-150 hover:border-accent/60 hover:bg-accent-soft hover:text-accent"
            title="Create a poll"
          >
            <BarChart3 className="h-4 w-4" />
          </button>
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

      <Modal open={pollBuilderOpen} onClose={closePollBuilder} title="Create a poll" icon={<BarChart3 className="h-4 w-4 text-accent" />}>
        <form className="flex flex-col gap-4" onSubmit={submitPoll}>
          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium text-text-muted">Question</label>
            <Input
              autoFocus
              value={pollQuestion}
              onChange={(e) => setPollQuestion(e.target.value)}
              placeholder="What should we ship next?"
              maxLength={500}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium text-text-muted">Options (2&ndash;10, unique)</label>
            {pollOptionInputs.map((value, i) => (
              <div key={i} className="flex items-center gap-1.5">
                <Input
                  value={value}
                  onChange={(e) => updatePollOptionInput(i, e.target.value)}
                  placeholder={`Option ${i + 1}`}
                  maxLength={200}
                  className="flex-1"
                />
                <button
                  type="button"
                  onClick={() => removePollOptionInput(i)}
                  disabled={pollOptionInputs.length <= 2}
                  className="grid h-8 w-8 shrink-0 place-items-center rounded-lg text-text-faint transition-colors duration-150 hover:bg-white/[0.06] hover:text-text disabled:cursor-not-allowed disabled:opacity-30"
                  title="Remove option"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
            <Button
              type="button"
              variant="secondary"
              className="self-start text-xs"
              icon={<Plus className="h-3.5 w-3.5" />}
              onClick={addPollOptionInput}
              disabled={pollOptionInputs.length >= 10}
            >
              Add option
            </Button>
          </div>
          <label className="flex items-center gap-2 text-sm text-text-muted">
            <input
              type="checkbox"
              checked={pollMultiSelect}
              onChange={(e) => setPollMultiSelect(e.target.checked)}
              className="h-4 w-4 rounded border-border-soft accent-accent"
            />
            Allow selecting more than one option
          </label>
          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium text-text-muted">Closes in (minutes, optional)</label>
            <Input
              type="number"
              min={1}
              value={pollClosesInMinutes}
              onChange={(e) => setPollClosesInMinutes(e.target.value)}
              placeholder="Leave blank to never close"
              className="max-w-[10rem]"
            />
          </div>
          <div className="flex justify-end gap-2 border-t border-border-soft pt-4">
            <Button type="button" variant="secondary" onClick={closePollBuilder}>
              Cancel
            </Button>
            <Button type="submit" disabled={!pollBuilderValid || busy} loading={busy}>
              Create &amp; send
            </Button>
          </div>
        </form>
      </Modal>

      <Modal open={pinnedPanelOpen} onClose={() => setPinnedPanelOpen(false)} title="Pinned messages" icon={<Pin className="h-4 w-4 text-accent" />}>
        <div className="flex flex-col gap-2">
          {pinnedMessages.length === 0 && <p className="py-6 text-center text-sm text-text-muted">No pinned messages in this channel yet.</p>}
          {pinnedMessages.map((m) => {
            const senderName = m.sender_id === profile.userId ? "you" : memberNames[m.sender_id] ?? m.sender_id.slice(0, 8);
            const loaded = messagesById.has(m.message_id);
            return (
              <div key={m.message_id} className="flex items-start gap-2 rounded-lg border border-border-soft bg-surface-2/60 px-3 py-2">
                <div className="min-w-0 flex-1">
                  <div className="font-mono text-[10.5px] text-text-faint">{senderName}</div>
                  <div className="truncate text-sm text-text">{m.body}</div>
                </div>
                <button
                  type="button"
                  onClick={() => {
                    setPinnedPanelOpen(false);
                    scrollToMessage(m.message_id);
                  }}
                  disabled={!loaded}
                  className="grid h-7 w-7 shrink-0 place-items-center rounded-full text-text-faint transition-colors duration-150 hover:bg-white/[0.06] hover:text-text disabled:cursor-not-allowed disabled:opacity-30"
                  title={loaded ? "Jump to message" : "Not loaded — try Load older in dev tools"}
                >
                  <Reply className="h-3.5 w-3.5 rotate-180" />
                </button>
                <button
                  type="button"
                  onClick={() => togglePin(m)}
                  className="grid h-7 w-7 shrink-0 place-items-center rounded-full text-text-faint transition-colors duration-150 hover:bg-white/[0.06] hover:text-text"
                  title="Unpin"
                >
                  <PinOff className="h-3.5 w-3.5" />
                </button>
              </div>
            );
          })}
        </div>
      </Modal>

      <Modal open={bookmarksPanelOpen} onClose={() => setBookmarksPanelOpen(false)} title="Bookmarks" icon={<BookmarkIcon className="h-4 w-4 text-accent" />}>
        <div className="flex flex-col gap-4">
          <div className="flex items-center gap-1.5">
            <Input
              value={newFolderName}
              onChange={(e) => setNewFolderName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  createFolder();
                }
              }}
              placeholder="New folder name"
              maxLength={200}
              className="flex-1"
            />
            <Button type="button" variant="secondary" className="text-xs" icon={<FolderPlus className="h-3.5 w-3.5" />} onClick={createFolder} disabled={!newFolderName.trim()}>
              Add
            </Button>
          </div>

          <BookmarkGroup
            title="Unfiled"
            bookmarks={bookmarks.filter((b) => !b.folder_id)}
            folders={bookmarkFolders}
            messagesById={messagesById}
            onMove={moveBookmarkToFolder}
            onRemove={removeBookmark}
          />
          {bookmarkFolders.map((folder) => (
            <BookmarkGroup
              key={folder.folder_id}
              title={folder.name}
              folderId={folder.folder_id}
              bookmarks={bookmarks.filter((b) => b.folder_id === folder.folder_id)}
              folders={bookmarkFolders}
              messagesById={messagesById}
              onMove={moveBookmarkToFolder}
              onRemove={removeBookmark}
              onRename={(name) => renameFolder(folder.folder_id, name)}
              onDeleteFolder={() => removeFolder(folder.folder_id)}
            />
          ))}
        </div>
      </Modal>
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

// TRANSLATE_LANGUAGES is a short, fixed picker list for the demo — the API
// itself accepts any BCP-47-ish code the configured provider supports
// (see cmd/api's langPattern), this is just a convenient subset to click.
const TRANSLATE_LANGUAGES: { code: string; label: string }[] = [
  { code: "es", label: "Spanish" },
  { code: "fr", label: "French" },
  { code: "de", label: "German" },
  { code: "pt", label: "Portuguese" },
  { code: "ja", label: "Japanese" },
  { code: "zh-Hans", label: "Chinese (Simplified)" },
  { code: "ar", label: "Arabic" },
  { code: "hi", label: "Hindi" },
];

// MessageTranslate mirrors MessageReactions' trigger+portal-popover shape:
// a Languages trigger reveals a short fixed language list instead of an
// emoji palette. Unlike reactions this has no persistent per-message state
// of its own (the parent's messageTranslations map owns that) — this
// component is purely "pick a language, call onSelect."
function MessageTranslate({ busy, onSelect }: { busy: boolean; onSelect: (lang: string) => void }) {
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerPos, setPickerPos] = useState<{ top: number; left: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const pickerRef = useRef<HTMLDivElement | null>(null);

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
    <div className="relative">
      <button
        ref={triggerRef}
        onClick={() => setPickerOpen((v) => !v)}
        disabled={busy}
        className={cx(
          "grid h-6 w-6 place-items-center rounded-full border text-text-faint opacity-0 transition-all duration-150 group-hover:opacity-100 disabled:cursor-wait",
          pickerOpen
            ? "opacity-100 border-accent bg-accent-soft text-accent"
            : "border-border-soft hover:border-accent/60 hover:bg-accent-soft hover:text-accent"
        )}
        title="Translate"
      >
        <Languages className="h-3.5 w-3.5" />
      </button>

      {pickerOpen &&
        pickerPos &&
        createPortal(
          <div
            ref={pickerRef}
            className="animate-fade-in-up fixed z-[100] flex -translate-y-full flex-col gap-0.5 rounded-lg border border-border bg-surface-2 p-1 shadow-xl"
            style={{ top: pickerPos.top, left: pickerPos.left }}
          >
            {TRANSLATE_LANGUAGES.map(({ code, label }) => (
              <button
                key={code}
                onClick={() => {
                  onSelect(code);
                  setPickerOpen(false);
                }}
                className="whitespace-nowrap rounded-md px-2.5 py-1 text-left text-xs text-text-muted transition-colors duration-150 hover:bg-white/[0.06] hover:text-text"
              >
                {label}
              </button>
            ))}
          </div>,
          document.body
        )}
    </div>
  );
}

// PollCard renders a poll attached to a message (Message.poll_id) — never
// inlined on the message itself, so `poll` is undefined until the
// "fetch any poll_id we haven't seen yet" effect above resolves it. Each
// option is a full-width button (radio-like for single-select, checkbox-like
// for multi-select — see votePollOption's toggle logic) with a percentage
// fill bar behind the label/count, matching the accent-vs-bubble coloring
// the rest of the message row already uses for isYou.
function PollCard({
  poll,
  isYou,
  onVote,
  onRetract,
}: {
  poll: Poll | undefined;
  isYou: boolean;
  onVote: (poll: Poll, optionId: string) => void;
  onRetract: (poll: Poll) => void;
}) {
  if (!poll) {
    return <div className={cx("text-xs italic", isYou ? "text-white/70" : "text-text-faint")}>Loading poll&hellip;</div>;
  }

  const closed = poll.closes_at != null && new Date(poll.closes_at).getTime() <= Date.now();
  const myVotes = new Set(poll.voted_option_ids ?? []);
  const totalVotes = poll.options.reduce((sum, o) => sum + o.vote_count, 0);

  return (
    <div className="flex flex-col gap-2">
      <div className={cx("text-sm font-medium leading-snug", isYou ? "text-white" : "text-text")}>{poll.question}</div>
      <div className="flex flex-col gap-1.5">
        {poll.options.map((opt) => {
          const pct = totalVotes > 0 ? Math.round((opt.vote_count / totalVotes) * 100) : 0;
          const mine = myVotes.has(opt.option_id);
          return (
            <button
              key={opt.option_id}
              type="button"
              disabled={closed}
              onClick={() => onVote(poll, opt.option_id)}
              className={cx(
                "relative overflow-hidden rounded-lg border px-2.5 py-1.5 text-left text-xs transition-colors duration-150",
                isYou
                  ? cx("border-white/25", mine ? "bg-white/25" : "bg-white/5 hover:bg-white/15")
                  : cx("border-border-soft", mine ? "border-accent/60 bg-accent-soft" : "bg-surface/60 hover:bg-white/[0.04]"),
                closed && "cursor-not-allowed opacity-70"
              )}
            >
              <div
                className={cx("absolute inset-y-0 left-0 transition-all duration-300", isYou ? "bg-white/15" : "bg-accent/10")}
                style={{ width: `${pct}%` }}
              />
              <div className="relative flex items-center justify-between gap-2">
                <span className="flex min-w-0 items-center gap-1.5 truncate">
                  {mine && <Check className="h-3 w-3 shrink-0" />}
                  <span className="truncate">{opt.label}</span>
                </span>
                <span className={cx("shrink-0 font-mono", isYou ? "text-white/70" : "text-text-faint")}>
                  {opt.vote_count} &middot; {pct}%
                </span>
              </div>
            </button>
          );
        })}
      </div>
      <div className={cx("flex items-center justify-between gap-2 text-[10.5px]", isYou ? "text-white/70" : "text-text-faint")}>
        <span>
          {poll.total_voters} voter{poll.total_voters === 1 ? "" : "s"} &middot; {poll.multi_select ? "multi-select" : "single-select"}
          {closed && <> &middot; closed</>}
        </span>
        {myVotes.size > 0 && !closed && (
          <button type="button" onClick={() => onRetract(poll)} className="shrink-0 underline decoration-dotted hover:no-underline">
            Clear my vote
          </button>
        )}
      </div>
    </div>
  );
}

// BookmarkGroup renders one folder's worth of bookmarks (or the "Unfiled"
// pseudo-group when folderId is undefined) — a header (with rename/delete
// for a real folder) followed by each bookmark's row. A bookmark can
// reference a message in any channel the user belongs to, not just the
// one currently open, so messagesById (this channel's loaded messages
// only) is a best-effort lookup: a hit shows the real body/sender, a miss
// falls back to showing the channel/message id prefixes.
function BookmarkGroup({
  title,
  folderId,
  bookmarks,
  folders,
  messagesById,
  onMove,
  onRemove,
  onRename,
  onDeleteFolder,
}: {
  title: string;
  folderId?: string;
  bookmarks: Bookmark[];
  folders: BookmarkFolder[];
  messagesById: Map<string, Message>;
  onMove: (bookmarkId: string, folderId: string | undefined) => void;
  onRemove: (bookmarkId: string) => void;
  onRename?: (name: string) => void;
  onDeleteFolder?: () => void;
}) {
  const [renaming, setRenaming] = useState(false);
  const [renameValue, setRenameValue] = useState(title);

  if (bookmarks.length === 0 && !folderId) return null;

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-1.5">
        {renaming ? (
          <>
            <Input
              autoFocus
              value={renameValue}
              onChange={(e) => setRenameValue(e.target.value)}
              className="h-7 flex-1 text-xs"
              onKeyDown={(e) => {
                if (e.key === "Enter" && renameValue.trim()) {
                  onRename?.(renameValue.trim());
                  setRenaming(false);
                } else if (e.key === "Escape") {
                  setRenaming(false);
                }
              }}
            />
            <button
              type="button"
              onClick={() => {
                if (renameValue.trim()) onRename?.(renameValue.trim());
                setRenaming(false);
              }}
              className="grid h-6 w-6 shrink-0 place-items-center rounded-full text-text-faint hover:bg-white/[0.06] hover:text-text"
              title="Save name"
            >
              <Check className="h-3.5 w-3.5" />
            </button>
          </>
        ) : (
          <>
            <span className="flex-1 text-xs font-semibold uppercase tracking-wide text-text-faint">
              {title} <span className="font-normal normal-case text-text-faint/70">({bookmarks.length})</span>
            </span>
            {folderId && (
              <>
                <button
                  type="button"
                  onClick={() => {
                    setRenameValue(title);
                    setRenaming(true);
                  }}
                  className="grid h-6 w-6 shrink-0 place-items-center rounded-full text-text-faint hover:bg-white/[0.06] hover:text-text"
                  title="Rename folder"
                >
                  <Pencil className="h-3 w-3" />
                </button>
                <button
                  type="button"
                  onClick={onDeleteFolder}
                  className="grid h-6 w-6 shrink-0 place-items-center rounded-full text-text-faint hover:bg-white/[0.06] hover:text-danger"
                  title="Delete folder (bookmarks are kept, un-filed)"
                >
                  <Trash2 className="h-3 w-3" />
                </button>
              </>
            )}
          </>
        )}
      </div>
      {bookmarks.length === 0 && <p className="text-xs text-text-faint">Nothing here yet.</p>}
      {bookmarks.map((b) => {
        const msg = messagesById.get(b.message_id);
        return (
          <div key={b.bookmark_id} className="flex items-center gap-2 rounded-lg border border-border-soft bg-surface-2/60 px-3 py-2">
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm text-text">{msg ? msg.body : `message ${b.message_id.slice(0, 8)}… in channel ${b.channel_id.slice(0, 8)}…`}</div>
            </div>
            <select
              value={b.folder_id ?? ""}
              onChange={(e) => onMove(b.bookmark_id, e.target.value || undefined)}
              className="shrink-0 rounded-md border border-border-soft bg-surface px-1.5 py-1 text-[11px] text-text-muted"
              title="Move to folder"
            >
              <option value="">Unfiled</option>
              {folders.map((f) => (
                <option key={f.folder_id} value={f.folder_id}>
                  {f.name}
                </option>
              ))}
            </select>
            <button
              type="button"
              onClick={() => onRemove(b.bookmark_id)}
              className="grid h-7 w-7 shrink-0 place-items-center rounded-full text-text-faint transition-colors duration-150 hover:bg-white/[0.06] hover:text-danger"
              title="Remove bookmark"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </button>
          </div>
        );
      })}
    </div>
  );
}
