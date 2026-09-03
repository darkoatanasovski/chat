import type { ChannelCapabilities } from "./types";

// CAPABILITY_GROUPS drives the Settings tab's "Channel Capabilities" panels
// (console/app/console/apps/[id]/page.tsx) *and* the global search index
// (lib/search-index.ts) — a single source of truth so a new capability only
// needs to be added here to show up in both places. Every key here must
// match internal/apps.ChannelCapabilities' json tags exactly (lib/types.ts's
// ChannelCapabilities interface mirrors that struct field for field).
// Grouped for readability only; the backend has no concept of these
// groupings, just one flat set of 20 booleans.
export const CAPABILITY_GROUPS: { title: string; items: { key: keyof ChannelCapabilities; label: string; hint?: string }[] }[] = [
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
      { key: "translations", label: "Translations", hint: "On-demand message translation via Azure Translator — billed per request on a cache miss." },
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
export const KNOWN_COMMANDS = ["giphy", "ban", "unban", "mute", "unmute"];
