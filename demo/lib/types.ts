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
  /** Absent for a top-level message; another message_id in the same
   * channel when this is a reply. There's no separate "thread" resource —
   * a thread is just the messages sharing a parent_id chain. The demo
   * keeps the list flat and chronological (sequence order, as delivered)
   * and shows a "replying to …" strip on each reply that jumps to its
   * parent, rather than regrouping messages into nested sub-threads. */
  parent_id?: string | null;
  /** How many messages reply to this one (parent_id = this message_id) —
   * denormalized server-side, always present, 0 for a message with no
   * replies yet (including a reply itself). */
  reply_count: number;
  /** Absent unless a poll is attached. Unlike parent_id, a poll is a
   * separate resource — its state is never inlined here, fetch it with
   * getPoll(). */
  poll_id?: string | null;
  created_at: string;
  /** Absent for a message that's never been edited; the edit's UTC
   * timestamp otherwise. No edit history is kept, just current state. */
  edited_at?: string | null;
  reaction_counts: Record<string, number>;
  latest_reactions: ReactionSummary[];
  /** Absent/null for a message that isn't currently pinned. Set together
   * with pinned_by — channel-shared, not per-viewer, unlike a bookmark:
   * any channel member sees the same pinned state. */
  pinned_at?: string | null;
  pinned_by?: string | null;
}

export interface PollOption {
  option_id: string;
  label: string;
  vote_count: number;
}

export interface Poll {
  poll_id: string;
  channel_id: string;
  creator_id: string;
  question: string;
  multi_select: boolean;
  /** Absent for a poll with no close time — otherwise voting is rejected
   * (400) once this passes. */
  closes_at?: string | null;
  created_at: string;
  options: PollOption[];
  total_voters: number;
  /** The CALLING user's own current selection, scoped per-viewer server
   * side — never who else voted for what. Absent/empty if the caller
   * hasn't voted. */
  voted_option_ids?: string[];
}

/** The shape returned by votePoll/clearPollVotes — just the tallies that
 * changed, same "the write returns only the state it touched" convention
 * as reaction_counts on addReaction/removeReaction. */
export interface PollVoteState {
  options: PollOption[];
  total_voters: number;
  voted_option_ids: string[];
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

export interface BookmarkFolder {
  folder_id: string;
  name: string;
  created_at: string;
}

/** Private to the calling user — never visible to anyone else, the
 * opposite of a pinned message (see Message.pinned_at). */
export interface Bookmark {
  bookmark_id: string;
  channel_id: string;
  message_id: string;
  /** Absent/null when not filed into a folder ("unfiled"). */
  folder_id?: string | null;
  created_at: string;
}
