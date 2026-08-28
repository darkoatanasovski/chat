export interface Profile {
  userId: string;
  displayName: string;
  region: import("./regions").Region;
  tier: string;
  token: string;
}

export interface ChannelSummary {
  channel_id: string;
  name: string;
  home_region: string;
  last_message_sequence: number;
  last_message_at?: string;
}

export interface Channel {
  channel_id: string;
  name: string;
  home_region: string;
  virtual_shard: number;
}

/** reaction is a canonical string key (e.g. "like", "rocket") — never a raw
 * emoji glyph. The UI maps keys to display glyphs (see lib/reactions.ts). */
export interface ReactionSummary {
  reaction: string;
  user_id: string;
  created_at: string;
}

export interface Message {
  message_id: string;
  channel_id: string;
  sequence: number;
  sender_id: string;
  client_message_id: string;
  body: string;
  created_at: string;
  reaction_counts: Record<string, number>;
  latest_reactions: ReactionSummary[];
}

export interface Member {
  user_id: string;
  display_name: string;
}

/** A user_id known to this browser but not necessarily the active identity —
 * either one of your own other test users, or one added by pasting an id. */
export interface Contact {
  userId: string;
  displayName: string;
}
