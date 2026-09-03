// The Playground's feature catalog — every end-user-facing capability of
// the platform as a small form plus a RequestSpec builder. This is the
// single place a feature is described: the form (Field[]), the real
// request it makes (build), the SDK snippet (sdk) and which app-level
// capability toggle gates it (capability / requiresEdit). Adding a feature
// here is all it takes for it to show up in the sidebar, run, log, and
// render code in every snippet language.
//
// `capability` keys must match internal/apps.ChannelCapabilities' json
// tags (lib/types.ts's ChannelCapabilities) — the Playground reads the
// chosen app's current toggles and shows whether each feature is switched
// on, linking to the exact Settings row otherwise. A feature still runs
// when its capability is off: seeing the API's real 403 is part of the
// point.
import type { ChannelCapabilities } from "@/lib/types";
import type { RequestSpec } from "./client";
import { sdkPrelude, sdkRealtimePrelude } from "./snippets";

export type RecentKind = "message" | "poll" | "folder" | "bookmark";

/** Ids captured from recent responses (and from clicking a message in the
 * live channel view) so a follow-up feature's id field is pre-filled
 * instead of pasted — react to the message you just sent, vote on the poll
 * you just created. */
export type RecentIds = Partial<Record<RecentKind, string>>;

export type FieldType =
  | "text"
  | "textarea"
  | "number"
  | "select"
  | "checkbox"
  | "json"
  | "datetime"
  /** An end-user id — the form offers the other actors and channel members. */
  | "user"
  /** An id of one of RecentKind — pre-filled from RecentIds when left blank. */
  | "recent";

export interface FieldOption {
  value: string;
  label: string;
}

export interface Field {
  key: string;
  label: string;
  type: FieldType;
  recent?: RecentKind;
  required?: boolean;
  placeholder?: string;
  hint?: string;
  options?: FieldOption[];
  default?: string | number | boolean;
}

export type Values = Record<string, string | number | boolean | undefined>;

export interface FeatureContext {
  apiBase: string;
  wsBase: string;
  channelId: string;
  actor: { userId: string; displayName: string };
  recent: RecentIds;
}

export interface Feature {
  id: string;
  group: string;
  label: string;
  description: string;
  capability?: keyof ChannelCapabilities;
  /** Gated by the app's message_edit_enabled flag, not a channel capability. */
  requiresEdit?: boolean;
  /** Most features act inside the selected channel; the few that don't
   * (create channel, blocks, bookmarks) set this false so the Playground
   * doesn't insist on a channel first. */
  channelScoped?: boolean;
  fields: Field[];
  /** Absent for an "observe only" feature — nothing to run, the feature is
   * about what shows up in the event feed (connection events, unread
   * reminders). */
  build?: (values: Values, ctx: FeatureContext) => RequestSpec;
  /** Node.js SDK rendering of the same call, or absent when the SDK
   * doesn't cover this endpoint yet (the panel then says so and shows the
   * raw REST call instead). */
  sdk?: (values: Values, ctx: FeatureContext) => string;
  /** Extra guidance shown under the form — what to look for after running. */
  notes?: string;
}

export const FEATURE_GROUPS = [
  "Channels & members",
  "Messaging",
  "Reactions & pins",
  "Polls",
  "Realtime",
  "Read state",
  "Moderation",
  "Reminders",
  "Blocks",
  "Bookmarks",
] as const;

export const REACTION_KEYS = ["like", "dislike", "love", "laugh", "celebrate", "eyes", "rocket", "fire"] as const;

const LANGUAGES: FieldOption[] = [
  { value: "en", label: "English (en)" },
  { value: "de", label: "German (de)" },
  { value: "fr", label: "French (fr)" },
  { value: "es", label: "Spanish (es)" },
  { value: "it", label: "Italian (it)" },
  { value: "pt", label: "Portuguese (pt)" },
  { value: "nl", label: "Dutch (nl)" },
  { value: "mk", label: "Macedonian (mk)" },
  { value: "ja", label: "Japanese (ja)" },
  { value: "zh-Hans", label: "Chinese, simplified (zh-Hans)" },
];

// ---- value helpers ----

function str(v: Values, key: string): string {
  const x = v[key];
  if (x === undefined || x === null) return "";
  return typeof x === "string" ? x.trim() : String(x);
}

function num(v: Values, key: string): number | undefined {
  const raw = str(v, key);
  if (raw === "") return undefined;
  const n = Number(raw);
  return Number.isFinite(n) ? n : undefined;
}

function bool(v: Values, key: string): boolean {
  return v[key] === true;
}

function iso(v: Values, key: string): string | undefined {
  const raw = str(v, key);
  if (!raw) return undefined;
  const d = new Date(raw);
  return Number.isNaN(d.getTime()) ? raw : d.toISOString();
}

function json(v: Values, key: string): unknown {
  const raw = str(v, key);
  if (!raw) return undefined;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

function recentId(v: Values, key: string, kind: RecentKind, ctx: FeatureContext): string {
  return str(v, key) || ctx.recent[kind] || `${kind.toUpperCase()}_ID`;
}

function channel(ctx: FeatureContext): string {
  return ctx.channelId || "CHANNEL_ID";
}

function userId(v: Values, key: string): string {
  return str(v, key) || "USER_ID";
}

/** A fresh client_message_id per send — the API dedupes retries on it. */
function clientMessageId(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`;
}

function inTwoMinutes(): string {
  const d = new Date(Date.now() + 2 * 60_000);
  // datetime-local wants local time without zone, to the minute.
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const q = (s: string) => JSON.stringify(s);

// ---- shared fields ----

const messageField = (label = "Message", hint?: string): Field => ({
  key: "message_id",
  label,
  type: "recent",
  recent: "message",
  hint: hint ?? "Blank uses the most recent message (last sent, or clicked in the channel view).",
});

const bodyField: Field = { key: "body", label: "Body", type: "textarea", required: true, placeholder: "Hello from the Playground!" };

const limitField: Field = { key: "limit", label: "Limit", type: "number", default: 20 };

// ---- catalog ----

export const FEATURES: Feature[] = [
  // ---- Channels & members ----
  {
    id: "channel.create",
    group: "Channels & members",
    label: "Create channel",
    description: "Creates a channel homed in the caller's region. The creator is automatically its first member.",
    channelScoped: false,
    fields: [{ key: "name", label: "Name", type: "text", required: true, placeholder: "general" }],
    build: (v) => ({ kind: "http", method: "POST", path: "/channels", body: { name: str(v, "name") } }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}const channel = await chat.channels.create({ name: ${q(str(v, "name") || "general")} });`,
    notes: "The new channel is selected automatically once created, so the rest of the catalog can act inside it.",
  },
  {
    id: "channel.listMine",
    group: "Channels & members",
    label: "List my channels",
    description: "Every channel the acting end-user is a member of, with the latest message sequence per channel.",
    channelScoped: false,
    fields: [],
    build: () => ({ kind: "http", method: "GET", path: "/users/me/channels" }),
    sdk: (_v, ctx) => `${sdkPrelude(ctx.apiBase)}const channels = await chat.channels.listMine();`,
  },
  {
    id: "channel.addMember",
    group: "Channels & members",
    label: "Add member",
    description: "Adds another end-user of the same app to the channel. Only an existing member may add someone.",
    fields: [{ key: "user_id", label: "User", type: "user", required: true }],
    build: (v, ctx) => ({ kind: "http", method: "POST", path: `/channels/${channel(ctx)}/members`, body: { user_id: userId(v, "user_id") } }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.channels.addMember(${q(channel(ctx))}, ${q(userId(v, "user_id"))});`,
  },
  {
    id: "channel.listMembers",
    group: "Channels & members",
    label: "List members",
    description: "The channel's current members with display names.",
    fields: [],
    build: (_v, ctx) => ({ kind: "http", method: "GET", path: `/channels/${channel(ctx)}/members` }),
    sdk: (_v, ctx) => `${sdkPrelude(ctx.apiBase)}const members = await chat.channels.listMembers(${q(channel(ctx))});`,
  },

  // ---- Messaging ----
  {
    id: "message.send",
    group: "Messaging",
    label: "Send message",
    description: "Sends a top-level message. client_message_id is a fresh UUID per send — retrying the same one returns the original message instead of a duplicate.",
    fields: [bodyField],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages`,
      body: { client_message_id: clientMessageId(), body: str(v, "body") },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const message = await chat.messages.send(${q(channel(ctx))}, crypto.randomUUID(), ${q(str(v, "body") || "Hello!")});`,
    notes: "Watch the event feed: every connected actor in the channel receives a message.created frame.",
  },
  {
    id: "message.list",
    group: "Messaging",
    label: "List messages",
    description: "Newest-first, cursor-paginated. Pass the oldest sequence you've seen as `before` to page further back.",
    fields: [limitField, { key: "before", label: "Before (sequence)", type: "number", hint: "Optional cursor." }],
    build: (v, ctx) => ({
      kind: "http",
      method: "GET",
      path: `/channels/${channel(ctx)}/messages`,
      query: { limit: num(v, "limit"), before: num(v, "before") },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const messages = await chat.messages.list(${q(channel(ctx))}, { limit: ${num(v, "limit") ?? 20}${num(v, "before") !== undefined ? `, before: ${num(v, "before")}` : ""} });`,
  },
  {
    id: "message.edit",
    group: "Messaging",
    label: "Edit message",
    description: "Overwrites a message's body and sets edited_at. Only the sender may edit, and only while the app's Message Editing setting is on.",
    requiresEdit: true,
    fields: [messageField(), { ...bodyField, placeholder: "Edited body" }],
    build: (v, ctx) => ({
      kind: "http",
      method: "PATCH",
      path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}`,
      body: { body: str(v, "body") },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const updated = await chat.messages.edit(${q(channel(ctx))}, ${q(recentId(v, "message_id", "message", ctx))}, ${q(str(v, "body") || "Edited body")});`,
    notes: "Other members receive a message.edited frame carrying the new body.",
  },
  {
    id: "message.reply",
    group: "Messaging",
    label: "Reply in thread",
    description: "Sends a message with parent_id set. Nesting deeper than the app's Maximum Thread Depth is rejected with 400.",
    capability: "threads_and_replies",
    fields: [messageField("Parent message"), bodyField],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages`,
      body: { client_message_id: clientMessageId(), body: str(v, "body"), parent_id: recentId(v, "message_id", "message", ctx) },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const reply = await chat.messages.send(${q(channel(ctx))}, crypto.randomUUID(), ${q(str(v, "body") || "Replying!")}, ${q(recentId(v, "message_id", "message", ctx))});`,
    notes: "The parent's reply_count is denormalized — list messages again to see it tick up.",
  },
  {
    id: "message.quote",
    group: "Messaging",
    label: "Quote a message",
    description: "Sends a message that quotes another one in the same channel (quoted_message_id).",
    capability: "quotes",
    fields: [messageField("Quoted message"), bodyField],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages`,
      body: { client_message_id: clientMessageId(), body: str(v, "body"), quoted_message_id: recentId(v, "message_id", "message", ctx) },
    }),
  },
  {
    id: "message.attachment",
    group: "Messaging",
    label: "Send with attachment",
    description: "Attaches a client-supplied file reference. The platform never hosts files — it stores the URL and metadata you pass.",
    capability: "uploads",
    fields: [
      { ...bodyField, required: false, placeholder: "Here's the file" },
      { key: "url", label: "Attachment URL", type: "text", required: true, placeholder: "https://example.com/report.pdf" },
      { key: "type", label: "MIME type", type: "text", placeholder: "application/pdf" },
      { key: "filename", label: "Filename", type: "text", placeholder: "report.pdf" },
      { key: "size_bytes", label: "Size (bytes)", type: "number" },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages`,
      body: {
        client_message_id: clientMessageId(),
        body: str(v, "body"),
        attachments: [
          {
            url: str(v, "url"),
            type: str(v, "type") || undefined,
            filename: str(v, "filename") || undefined,
            size_bytes: num(v, "size_bytes"),
          },
        ],
      },
    }),
  },
  {
    id: "message.location",
    group: "Messaging",
    label: "Share location",
    description: "Sends a message carrying a lat/lng point.",
    capability: "location_sharing",
    fields: [
      { ...bodyField, required: false, placeholder: "I'm here" },
      { key: "lat", label: "Latitude", type: "number", required: true, default: 41.9973 },
      { key: "lng", label: "Longitude", type: "number", required: true, default: 21.428 },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages`,
      body: { client_message_id: clientMessageId(), body: str(v, "body"), location: { lat: num(v, "lat") ?? 0, lng: num(v, "lng") ?? 0 } },
    }),
  },
  {
    id: "message.link",
    group: "Messaging",
    label: "URL enrichment",
    description: "Sends a message containing a URL. A best-effort link preview is fetched after the send and appears on the message as link_preview.",
    capability: "url_enrichment",
    fields: [{ ...bodyField, default: "Worth a read: https://go.dev/blog/" }],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages`,
      body: { client_message_id: clientMessageId(), body: str(v, "body") },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}await chat.messages.send(${q(channel(ctx))}, crypto.randomUUID(), ${q(str(v, "body") || "https://go.dev/blog/")});\n// link_preview is filled in asynchronously — re-list a moment later:\nconst [latest] = await chat.messages.list(${q(channel(ctx))}, { limit: 1 });\nconsole.log(latest.link_preview);`,
    notes: "The preview is asynchronous: run List messages a second or two later and look for link_preview on the message.",
  },
  {
    id: "message.search",
    group: "Messaging",
    label: "Search messages",
    description: "Simple substring search over message bodies in this channel.",
    capability: "search",
    fields: [{ key: "q", label: "Query", type: "text", required: true, placeholder: "hello" }, limitField],
    build: (v, ctx) => ({
      kind: "http",
      method: "GET",
      path: `/channels/${channel(ctx)}/messages/search`,
      query: { q: str(v, "q"), limit: num(v, "limit") },
    }),
  },
  {
    id: "message.translate",
    group: "Messaging",
    label: "Translate message",
    description: "On-demand translation via Azure Translator. Results are cached per message and language — the response says whether this call hit the cache.",
    capability: "translations",
    fields: [
      messageField(),
      { key: "target_lang", label: "Target language", type: "select", options: LANGUAGES, default: "de", required: true },
      { key: "source_lang", label: "Source language", type: "text", placeholder: "auto-detect", hint: "Optional — detected when blank." },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/translate`,
      body: { target_lang: str(v, "target_lang"), source_lang: str(v, "source_lang") || undefined },
    }),
    notes: "Returns 503 when the deployment has no Azure Translator key configured.",
  },

  // ---- Reactions & pins ----
  {
    id: "reaction.add",
    group: "Reactions & pins",
    label: "Add reaction",
    description: "Reacts with one of the canonical reaction keys. The response carries the message's fresh counts.",
    capability: "reactions",
    fields: [
      messageField(),
      { key: "reaction", label: "Reaction", type: "select", options: REACTION_KEYS.map((r) => ({ value: r, label: r })), default: "like", required: true },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/reactions`,
      body: { reaction: str(v, "reaction") || "like" },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const state = await chat.reactions.add(${q(channel(ctx))}, ${q(recentId(v, "message_id", "message", ctx))}, ${q(str(v, "reaction") || "like")});`,
    notes: "Other members receive a reaction.updated frame with the new reaction_counts.",
  },
  {
    id: "reaction.remove",
    group: "Reactions & pins",
    label: "Remove reaction",
    description: "Removes the caller's own reaction of that kind.",
    capability: "reactions",
    fields: [
      messageField(),
      { key: "reaction", label: "Reaction", type: "select", options: REACTION_KEYS.map((r) => ({ value: r, label: r })), default: "like", required: true },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "DELETE",
      path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/reactions/${str(v, "reaction") || "like"}`,
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const state = await chat.reactions.remove(${q(channel(ctx))}, ${q(recentId(v, "message_id", "message", ctx))}, ${q(str(v, "reaction") || "like")});`,
  },
  {
    id: "pin.pin",
    group: "Reactions & pins",
    label: "Pin message",
    description: "Channel-shared pin — any member may pin, and pinning an already-pinned message is a no-op.",
    fields: [messageField()],
    build: (v, ctx) => ({ kind: "http", method: "POST", path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/pin` }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}const pinned = await chat.pins.pin(${q(channel(ctx))}, ${q(recentId(v, "message_id", "message", ctx))});`,
  },
  {
    id: "pin.unpin",
    group: "Reactions & pins",
    label: "Unpin message",
    description: "Any member may unpin, regardless of who pinned.",
    fields: [messageField()],
    build: (v, ctx) => ({ kind: "http", method: "DELETE", path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/pin` }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.pins.unpin(${q(channel(ctx))}, ${q(recentId(v, "message_id", "message", ctx))});`,
  },
  {
    id: "pin.list",
    group: "Reactions & pins",
    label: "List pinned",
    description: "The channel's pinned messages, most recently pinned first.",
    fields: [limitField],
    build: (v, ctx) => ({ kind: "http", method: "GET", path: `/channels/${channel(ctx)}/pinned-messages`, query: { limit: num(v, "limit") } }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}const pinned = await chat.pins.list(${q(channel(ctx))}, ${num(v, "limit") ?? 20});`,
  },

  // ---- Polls ----
  {
    id: "poll.create",
    group: "Polls",
    label: "Create poll",
    description: "Creates a standalone poll. It isn't visible to other members until a message attaches it (see Attach poll to message).",
    capability: "polls",
    fields: [
      { key: "question", label: "Question", type: "text", required: true, placeholder: "Tabs or spaces?" },
      { key: "options", label: "Options (one per line)", type: "textarea", required: true, default: "Tabs\nSpaces", hint: "2–10 unique options." },
      { key: "multi_select", label: "Allow multiple choices", type: "checkbox" },
      { key: "closes_at", label: "Closes at", type: "datetime", hint: "Optional — blank never closes." },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/polls`,
      body: {
        question: str(v, "question"),
        options: str(v, "options").split("\n").map((s) => s.trim()).filter(Boolean),
        multi_select: bool(v, "multi_select"),
        closes_at: iso(v, "closes_at"),
      },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const poll = await chat.polls.create(${q(channel(ctx))}, ${q(str(v, "question") || "Tabs or spaces?")}, ${JSON.stringify(str(v, "options").split("\n").map((s) => s.trim()).filter(Boolean))}${bool(v, "multi_select") ? ", { multiSelect: true }" : ""});`,
  },
  {
    id: "poll.attach",
    group: "Polls",
    label: "Attach poll to message",
    description: "Sends a message with poll_id set, which is what makes the poll visible in the channel.",
    capability: "polls",
    fields: [{ key: "poll_id", label: "Poll", type: "recent", recent: "poll", hint: "Blank uses the most recently created poll." }, { ...bodyField, default: "Vote now!" }],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages`,
      body: { client_message_id: clientMessageId(), body: str(v, "body"), poll_id: recentId(v, "poll_id", "poll", ctx) },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}await chat.messages.send(${q(channel(ctx))}, crypto.randomUUID(), ${q(str(v, "body") || "Vote now!")}, undefined, ${q(recentId(v, "poll_id", "poll", ctx))});`,
  },
  {
    id: "poll.get",
    group: "Polls",
    label: "Get poll",
    description: "Current tallies plus the caller's own votes (voted_option_ids) — never anyone else's.",
    capability: "polls",
    fields: [{ key: "poll_id", label: "Poll", type: "recent", recent: "poll" }],
    build: (v, ctx) => ({ kind: "http", method: "GET", path: `/channels/${channel(ctx)}/polls/${recentId(v, "poll_id", "poll", ctx)}` }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}const poll = await chat.polls.get(${q(channel(ctx))}, ${q(recentId(v, "poll_id", "poll", ctx))});`,
  },
  {
    id: "poll.vote",
    group: "Polls",
    label: "Vote",
    description: "Replaces the caller's entire vote with the given option ids — one for a single-select poll, the full selection for multi-select.",
    capability: "polls",
    fields: [
      { key: "poll_id", label: "Poll", type: "recent", recent: "poll" },
      { key: "option_ids", label: "Option ids", type: "text", required: true, placeholder: "option UUID, another UUID", hint: "Comma-separated. Run Get poll to see the ids." },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/polls/${recentId(v, "poll_id", "poll", ctx)}/votes`,
      body: { option_ids: str(v, "option_ids").split(",").map((s) => s.trim()).filter(Boolean) },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const tallies = await chat.polls.vote(${q(channel(ctx))}, ${q(recentId(v, "poll_id", "poll", ctx))}, ${JSON.stringify(str(v, "option_ids").split(",").map((s) => s.trim()).filter(Boolean))});`,
    notes: "Other members receive a poll.vote_updated frame with fresh tallies.",
  },
  {
    id: "poll.clear",
    group: "Polls",
    label: "Retract vote",
    description: "Removes the caller's vote(s). Idempotent.",
    capability: "polls",
    fields: [{ key: "poll_id", label: "Poll", type: "recent", recent: "poll" }],
    build: (v, ctx) => ({ kind: "http", method: "DELETE", path: `/channels/${channel(ctx)}/polls/${recentId(v, "poll_id", "poll", ctx)}/votes` }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.polls.clearVotes(${q(channel(ctx))}, ${q(recentId(v, "poll_id", "poll", ctx))});`,
  },

  // ---- Realtime ----
  {
    id: "typing.start",
    group: "Realtime",
    label: "Typing: start",
    description: "Pushes a typing.start frame up the acting actor's WebSocket. Ephemeral — nothing is stored; other members get a typing.updated frame.",
    capability: "typing_events",
    fields: [],
    build: (_v, ctx) => ({ kind: "ws", frame: { type: "typing.start", channel_id: channel(ctx) } }),
    sdk: (_v, ctx) => `${sdkRealtimePrelude(ctx.wsBase)}realtime.setTyping(${q(channel(ctx))}, true);`,
    notes: "Needs at least two actors connected: the sender never receives its own typing frame.",
  },
  {
    id: "typing.stop",
    group: "Realtime",
    label: "Typing: stop",
    description: "Pushes a typing.stop frame.",
    capability: "typing_events",
    fields: [],
    build: (_v, ctx) => ({ kind: "ws", frame: { type: "typing.stop", channel_id: channel(ctx) } }),
    sdk: (_v, ctx) => `${sdkRealtimePrelude(ctx.wsBase)}realtime.setTyping(${q(channel(ctx))}, false);`,
  },
  {
    id: "event.custom",
    group: "Realtime",
    label: "Custom event",
    description: "Broadcasts an arbitrary event of your own to the channel's members. Never stored — it exists only long enough to be delivered.",
    capability: "custom_events",
    fields: [
      { key: "event_type", label: "Event type", type: "text", required: true, default: "game.move" },
      { key: "data", label: "Data (JSON)", type: "json", default: '{ "square": "e4" }' },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/events`,
      body: { event_type: str(v, "event_type"), data: json(v, "data") },
    }),
    notes: "Returns 202 — delivery happens asynchronously. Look for a custom.event frame in the feed.",
  },
  {
    id: "connection.observe",
    group: "Realtime",
    label: "Connection events",
    description: "With this capability on, every channel a user belongs to is told when that user connects or disconnects (connection.updated frames).",
    capability: "connection_events",
    fields: [],
    sdk: (_v, ctx) =>
      `${sdkRealtimePrelude(ctx.wsBase)}// onEvent above receives { type: "connection.updated", user_id, connected }\n// whenever a fellow channel member connects or disconnects.`,
    notes: "Nothing to run: toggle another actor's realtime connection off and on in the setup bar and watch the feed.",
  },

  // ---- Read state ----
  {
    id: "read.mark",
    group: "Read state",
    label: "Mark read",
    description: "Advances the caller's read watermark. Blank sequence marks up to the latest message; the watermark never regresses.",
    capability: "read_events",
    fields: [{ key: "sequence", label: "Sequence", type: "number", hint: "Optional." }],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/read`,
      body: num(v, "sequence") !== undefined ? { sequence: num(v, "sequence") } : {},
    }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.readState.mark(${q(channel(ctx))}${num(v, "sequence") !== undefined ? `, ${num(v, "sequence")}` : ""});`,
    notes: "Other members receive a read.updated frame — the basis for 'seen by' indicators.",
  },
  {
    id: "read.list",
    group: "Read state",
    label: "List read state",
    description: "Every member's last-read sequence for the channel.",
    fields: [],
    build: (_v, ctx) => ({ kind: "http", method: "GET", path: `/channels/${channel(ctx)}/read-state` }),
    sdk: (_v, ctx) => `${sdkPrelude(ctx.apiBase)}const readState = await chat.readState.list(${q(channel(ctx))});`,
  },

  // ---- Moderation ----
  {
    id: "pending.send",
    group: "Moderation",
    label: "Send pending message",
    description: "Sends a message with pending: true — visible only to its sender until approved.",
    capability: "pending_messages",
    fields: [{ ...bodyField, default: "Please approve me" }],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages`,
      body: { client_message_id: clientMessageId(), body: str(v, "body"), pending: true },
    }),
    notes: "Switch to another actor and run List messages: the pending message isn't there until it's approved.",
  },
  {
    id: "pending.list",
    group: "Moderation",
    label: "List pending",
    description: "The channel's moderation queue. Any member can see it — there's no separate moderator role.",
    capability: "pending_messages",
    fields: [limitField],
    build: (v, ctx) => ({ kind: "http", method: "GET", path: `/channels/${channel(ctx)}/messages/pending`, query: { limit: num(v, "limit") } }),
  },
  {
    id: "pending.approve",
    group: "Moderation",
    label: "Approve message",
    description: "Publishes a pending message to the channel.",
    capability: "pending_messages",
    fields: [messageField("Pending message")],
    build: (v, ctx) => ({ kind: "http", method: "POST", path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/approve` }),
    notes: "Approval is what triggers the message.created frame for everyone else.",
  },
  {
    id: "pending.reject",
    group: "Moderation",
    label: "Reject message",
    description: "Drops a pending message.",
    capability: "pending_messages",
    fields: [messageField("Pending message")],
    build: (v, ctx) => ({ kind: "http", method: "POST", path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/reject` }),
  },
  {
    id: "mute.add",
    group: "Moderation",
    label: "Mute user",
    description: "One-directional, per-channel mute. Advisory only — the API records it, your client decides how to hide the muted user's messages.",
    capability: "mutes",
    fields: [{ key: "user_id", label: "User", type: "user", required: true }],
    build: (v, ctx) => ({ kind: "http", method: "POST", path: `/channels/${channel(ctx)}/mutes`, body: { user_id: userId(v, "user_id") } }),
  },
  {
    id: "mute.remove",
    group: "Moderation",
    label: "Unmute user",
    description: "Removes a mute the caller created.",
    capability: "mutes",
    fields: [{ key: "user_id", label: "User", type: "user", required: true }],
    build: (v, ctx) => ({ kind: "http", method: "DELETE", path: `/channels/${channel(ctx)}/mutes/${userId(v, "user_id")}` }),
  },
  {
    id: "mute.list",
    group: "Moderation",
    label: "List mutes",
    description: "Users the caller has muted in this channel.",
    capability: "mutes",
    fields: [],
    build: (_v, ctx) => ({ kind: "http", method: "GET", path: `/channels/${channel(ctx)}/mutes` }),
  },

  // ---- Reminders ----
  {
    id: "reminder.create",
    group: "Reminders",
    label: "Remind me about a message",
    description: "Schedules a one-off reminder. When it's due, the worker delivers a message_reminder.due frame to the caller's socket only.",
    capability: "message_reminders",
    fields: [messageField(), { key: "remind_at", label: "Remind at", type: "datetime", required: true, default: inTwoMinutes() }],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/reminders`,
      body: { remind_at: iso(v, "remind_at") },
    }),
    notes: "Keep the acting actor's realtime connection on and wait for the frame in the event feed.",
  },
  {
    id: "reminder.cancel",
    group: "Reminders",
    label: "Cancel reminder",
    description: "Cancels the caller's pending reminder for a message.",
    capability: "message_reminders",
    fields: [messageField()],
    build: (v, ctx) => ({ kind: "http", method: "DELETE", path: `/channels/${channel(ctx)}/messages/${recentId(v, "message_id", "message", ctx)}/reminders` }),
  },
  {
    id: "unread.observe",
    group: "Reminders",
    label: "Unread reminders",
    description: "A background sweep nudges members who are behind on a channel with an unread_reminder.due frame, respecting a per-member cooldown.",
    capability: "unread_reminders",
    fields: [],
    sdk: (_v, ctx) =>
      `${sdkRealtimePrelude(ctx.wsBase)}// onEvent above receives { type: "unread_reminder.due", channel_id, last_read_sequence, latest_sequence }`,
    notes: "Nothing to run: send messages as one actor, leave another actor connected without marking read, and wait for the sweep.",
  },

  // ---- Blocks ----
  {
    id: "block.add",
    group: "Blocks",
    label: "Block user",
    description: "App-wide block. Enforced in both directions at delivery time — neither user receives the other's realtime frames.",
    channelScoped: false,
    fields: [{ key: "user_id", label: "User", type: "user", required: true }],
    build: (v) => ({ kind: "http", method: "POST", path: "/blocks", body: { user_id: userId(v, "user_id") } }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.blocks.block(${q(userId(v, "user_id"))});`,
    notes: "Blocks also show up on the app's Blocks tab in this console.",
  },
  {
    id: "block.remove",
    group: "Blocks",
    label: "Unblock user",
    description: "Removes a block the caller created.",
    channelScoped: false,
    fields: [{ key: "user_id", label: "User", type: "user", required: true }],
    build: (v) => ({ kind: "http", method: "DELETE", path: `/blocks/${userId(v, "user_id")}` }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.blocks.unblock(${q(userId(v, "user_id"))});`,
  },
  {
    id: "block.list",
    group: "Blocks",
    label: "List blocks",
    description: "Users the caller has blocked.",
    channelScoped: false,
    fields: [],
    build: () => ({ kind: "http", method: "GET", path: "/blocks" }),
    sdk: (_v, ctx) => `${sdkPrelude(ctx.apiBase)}const blocked = await chat.blocks.list();`,
  },

  // ---- Bookmarks ----
  {
    id: "bookmark.create",
    group: "Bookmarks",
    label: "Bookmark message",
    description: "Private to the caller — the opposite of a pin. Optionally filed into one of the caller's folders.",
    fields: [messageField(), { key: "folder_id", label: "Folder", type: "recent", recent: "folder", hint: "Optional — blank leaves it unfiled." }],
    build: (v, ctx) => ({
      kind: "http",
      method: "POST",
      path: "/bookmarks",
      body: { channel_id: channel(ctx), message_id: recentId(v, "message_id", "message", ctx), folder_id: str(v, "folder_id") || undefined },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}const bookmark = await chat.bookmarks.create(${q(channel(ctx))}, ${q(recentId(v, "message_id", "message", ctx))}${str(v, "folder_id") ? `, ${q(str(v, "folder_id"))}` : ""});`,
  },
  {
    id: "bookmark.list",
    group: "Bookmarks",
    label: "List bookmarks",
    description: "All of the caller's bookmarks, or one folder's. Pass \"none\" to list only unfiled ones.",
    channelScoped: false,
    fields: [{ key: "folder_id", label: "Folder", type: "text", placeholder: "blank = all, none = unfiled, or a folder id" }],
    build: (v) => ({ kind: "http", method: "GET", path: "/bookmarks", query: { folder_id: str(v, "folder_id") || undefined } }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}const bookmarks = await chat.bookmarks.list(${str(v, "folder_id") ? q(str(v, "folder_id")) : ""});`,
  },
  {
    id: "bookmark.move",
    group: "Bookmarks",
    label: "Move bookmark",
    description: "Files a bookmark into a folder, or unfiles it when the folder is blank.",
    channelScoped: false,
    fields: [
      { key: "bookmark_id", label: "Bookmark", type: "recent", recent: "bookmark" },
      { key: "folder_id", label: "Folder", type: "recent", recent: "folder", hint: "Blank unfiles it." },
    ],
    build: (v, ctx) => ({
      kind: "http",
      method: "PATCH",
      path: `/bookmarks/${recentId(v, "bookmark_id", "bookmark", ctx)}`,
      body: { folder_id: str(v, "folder_id") || null },
    }),
    sdk: (v, ctx) =>
      `${sdkPrelude(ctx.apiBase)}await chat.bookmarks.move(${q(recentId(v, "bookmark_id", "bookmark", ctx))}${str(v, "folder_id") ? `, ${q(str(v, "folder_id"))}` : ""});`,
  },
  {
    id: "bookmark.delete",
    group: "Bookmarks",
    label: "Remove bookmark",
    description: "Deletes one of the caller's bookmarks.",
    channelScoped: false,
    fields: [{ key: "bookmark_id", label: "Bookmark", type: "recent", recent: "bookmark" }],
    build: (v, ctx) => ({ kind: "http", method: "DELETE", path: `/bookmarks/${recentId(v, "bookmark_id", "bookmark", ctx)}` }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.bookmarks.delete(${q(recentId(v, "bookmark_id", "bookmark", ctx))});`,
  },
  {
    id: "bookmark.folder.create",
    group: "Bookmarks",
    label: "Create folder",
    description: "A private folder to file bookmarks into.",
    channelScoped: false,
    fields: [{ key: "name", label: "Name", type: "text", required: true, placeholder: "Read later" }],
    build: (v) => ({ kind: "http", method: "POST", path: "/bookmarks/folders", body: { name: str(v, "name") } }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}const folder = await chat.bookmarks.folders.create(${q(str(v, "name") || "Read later")});`,
  },
  {
    id: "bookmark.folder.list",
    group: "Bookmarks",
    label: "List folders",
    description: "The caller's folders.",
    channelScoped: false,
    fields: [],
    build: () => ({ kind: "http", method: "GET", path: "/bookmarks/folders" }),
    sdk: (_v, ctx) => `${sdkPrelude(ctx.apiBase)}const folders = await chat.bookmarks.folders.list();`,
  },
  {
    id: "bookmark.folder.rename",
    group: "Bookmarks",
    label: "Rename folder",
    description: "Renames one of the caller's folders.",
    channelScoped: false,
    fields: [{ key: "folder_id", label: "Folder", type: "recent", recent: "folder" }, { key: "name", label: "New name", type: "text", required: true }],
    build: (v, ctx) => ({ kind: "http", method: "PATCH", path: `/bookmarks/folders/${recentId(v, "folder_id", "folder", ctx)}`, body: { name: str(v, "name") } }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.bookmarks.folders.rename(${q(recentId(v, "folder_id", "folder", ctx))}, ${q(str(v, "name") || "Archive")});`,
  },
  {
    id: "bookmark.folder.delete",
    group: "Bookmarks",
    label: "Delete folder",
    description: "Deletes a folder. Bookmarks filed in it become unfiled — they aren't deleted.",
    channelScoped: false,
    fields: [{ key: "folder_id", label: "Folder", type: "recent", recent: "folder" }],
    build: (v, ctx) => ({ kind: "http", method: "DELETE", path: `/bookmarks/folders/${recentId(v, "folder_id", "folder", ctx)}` }),
    sdk: (v, ctx) => `${sdkPrelude(ctx.apiBase)}await chat.bookmarks.folders.delete(${q(recentId(v, "folder_id", "folder", ctx))});`,
  },
];

export function featureById(id: string): Feature | undefined {
  return FEATURES.find((f) => f.id === id);
}

/** Initial form values for a feature — its declared defaults. */
export function defaultValues(feature: Feature): Values {
  const out: Values = {};
  for (const f of feature.fields) {
    if (f.default !== undefined) out[f.key] = f.default;
  }
  return out;
}

/** Pulls any ids worth remembering out of a response — a created message,
 * poll, folder or bookmark — so the next feature's id field is pre-filled.
 * Only looks at a top-level object (a list response says nothing about
 * what the user just did). */
export function captureRecent(response: unknown): RecentIds {
  if (!response || typeof response !== "object" || Array.isArray(response)) return {};
  const r = response as Record<string, unknown>;
  const out: RecentIds = {};
  if (typeof r.message_id === "string") out.message = r.message_id;
  if (typeof r.poll_id === "string") out.poll = r.poll_id;
  if (typeof r.bookmark_id === "string") out.bookmark = r.bookmark_id;
  // A bookmark's folder_id is where it's filed, not a folder the user just
  // made — only a folder response (no bookmark_id) should update "folder".
  if (typeof r.folder_id === "string" && typeof r.bookmark_id !== "string") out.folder = r.folder_id;
  return out;
}
