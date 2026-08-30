import type { Bookmark, BookmarkFolder, Channel, ChannelSummary, Member, Message, Poll, PollVoteState } from "./types";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

type AppCredentials = { key: string; secret: string };

async function request<T>(
  apiBase: string,
  path: string,
  opts: RequestInit & { token?: string; appCredentials?: AppCredentials } = {}
): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (opts.token) headers["Authorization"] = `Bearer ${opts.token}`;
  if (opts.appCredentials) {
    headers["Authorization"] = `Basic ${btoa(`${opts.appCredentials.key}:${opts.appCredentials.secret}`)}`;
  }

  const res = await fetch(`${apiBase}${path}`, { ...opts, headers });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = await res.json();
      message = body.error ?? message;
    } catch {
      // ignore non-JSON error bodies
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// exchangeAppToken mints a short-lived app Bearer token from an app's
// key+secret (POST /apps/token, Basic auth) — POST /users only accepts that
// Bearer token, never the raw key+secret directly (cmd/api's
// requireAppJWT), so createUser below always does this exchange first. The
// SDK's createServerClient caches this per-instance; the demo doesn't
// bother — user creation here is a one-off per sign-in, not a hot path.
async function exchangeAppToken(apiBase: string, appCredentials: AppCredentials): Promise<string> {
  const { token } = await request<{ token: string; app_id: number; expires_at: string }>(apiBase, "/apps/token", {
    method: "POST",
    appCredentials,
  });
  return token;
}

// createUser is called with the App credentials of the org whose tier the
// caller picked — tier is never sent in the request body. It's resolved
// live from the App's owning Organization on the server, matching the
// B2B model: this is the endpoint a business's own backend calls, and the
// demo just plays that role using the credentials deploy/seed.sh mints.
export async function createUser(apiBase: string, displayName: string, region: string, appCredentials: AppCredentials) {
  const appToken = await exchangeAppToken(apiBase, appCredentials);
  return request<{ user_id: string; display_name: string; region: string; tier: string; token: string }>(
    apiBase,
    "/users",
    { method: "POST", body: JSON.stringify({ display_name: displayName, region }), token: appToken }
  );
}

export function createChannel(apiBase: string, token: string, name: string) {
  return request<Channel>(apiBase, "/channels", { method: "POST", token, body: JSON.stringify({ name }) });
}

export function addMember(apiBase: string, token: string, channelId: string, userId: string) {
  return request<{ channel_id: string; user_id: string }>(apiBase, `/channels/${channelId}/members`, {
    method: "POST",
    token,
    body: JSON.stringify({ user_id: userId }),
  });
}

export function listMyChannels(apiBase: string, token: string) {
  return request<ChannelSummary[]>(apiBase, "/users/me/channels", { token });
}

export function listMembers(apiBase: string, token: string, channelId: string) {
  return request<Member[]>(apiBase, `/channels/${channelId}/members`, { token });
}

// parentId, when set, sends this as a reply to another message in the same
// channel instead of a top-level message — rejected server-side (400) if it
// would nest deeper than the app's configured max_thread_depth, or (404) if
// parentId doesn't name a message in this channel. pollId, when set,
// attaches an already-created poll (createPoll) — 404 if it doesn't name a
// poll in this channel.
export function sendMessage(
  apiBase: string,
  token: string,
  channelId: string,
  clientMessageId: string,
  body: string,
  parentId?: string,
  pollId?: string
) {
  return request<Message>(apiBase, `/channels/${channelId}/messages`, {
    method: "POST",
    token,
    body: JSON.stringify({ client_message_id: clientMessageId, body, parent_id: parentId, poll_id: pollId }),
  });
}

export function listMessages(apiBase: string, token: string, channelId: string, before?: number, limit = 50) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (before) params.set("before", String(before));
  return request<Message[]>(apiBase, `/channels/${channelId}/messages?${params.toString()}`, { token });
}

// Only the message's own sender may edit it (403 otherwise), and only if
// the app has message_edit_enabled on (403 if it's off — there's no
// client-facing way to check this ahead of time, so the demo just always
// shows the Edit affordance and surfaces the error if the API rejects it).
export function editMessage(apiBase: string, token: string, channelId: string, messageId: string, body: string) {
  return request<Message>(apiBase, `/channels/${channelId}/messages/${messageId}`, {
    method: "PATCH",
    token,
    body: JSON.stringify({ body }),
  });
}

interface ReactionState {
  reaction_counts: Record<string, number>;
  latest_reactions: import("./types").ReactionSummary[];
}

export function addReaction(apiBase: string, token: string, channelId: string, messageId: string, reaction: string) {
  return request<ReactionState>(apiBase, `/channels/${channelId}/messages/${messageId}/reactions`, {
    method: "POST",
    token,
    body: JSON.stringify({ reaction }),
  });
}

export function removeReaction(apiBase: string, token: string, channelId: string, messageId: string, reaction: string) {
  return request<ReactionState>(apiBase, `/channels/${channelId}/messages/${messageId}/reactions/${encodeURIComponent(reaction)}`, {
    method: "DELETE",
    token,
  });
}

// Idempotent — pinning an already-pinned message leaves it unchanged. Any
// channel member may pin; there's no channel-owner role in this API.
export function pinMessage(apiBase: string, token: string, channelId: string, messageId: string) {
  return request<Message>(apiBase, `/channels/${channelId}/messages/${messageId}/pin`, {
    method: "POST",
    token,
  });
}

// Idempotent. Any channel member may unpin, regardless of who pinned it.
export function unpinMessage(apiBase: string, token: string, channelId: string, messageId: string) {
  return request<Message>(apiBase, `/channels/${channelId}/messages/${messageId}/pin`, {
    method: "DELETE",
    token,
  });
}

export function listPinnedMessages(apiBase: string, token: string, channelId: string) {
  return request<Message[]>(apiBase, `/channels/${channelId}/pinned-messages`, { token });
}

interface ReadStateEntry {
  user_id: string;
  last_read_sequence: number;
}

// markRead defaults to "the channel's latest message" when sequence is
// omitted — the common case (opening a channel, or new messages arriving
// while already at the bottom). Monotonic server-side, so calling this
// repeatedly is always safe.
export function markRead(apiBase: string, token: string, channelId: string, sequence?: number) {
  return request<ReadStateEntry>(apiBase, `/channels/${channelId}/read`, {
    method: "POST",
    token,
    body: sequence ? JSON.stringify({ sequence }) : undefined,
  });
}

export function listReadState(apiBase: string, token: string, channelId: string) {
  return request<ReadStateEntry[]>(apiBase, `/channels/${channelId}/read-state`, { token });
}

// createPoll makes a poll a standalone entity in the channel — it isn't
// visible to other members until a message attaches it via poll_id
// (sendMessage). options must have 2-10 unique (case-insensitive) entries.
export function createPoll(
  apiBase: string,
  token: string,
  channelId: string,
  question: string,
  options: string[],
  multiSelect: boolean,
  closesAt?: string
) {
  return request<Poll>(apiBase, `/channels/${channelId}/polls`, {
    method: "POST",
    token,
    body: JSON.stringify({ question, options, multi_select: multiSelect, closes_at: closesAt }),
  });
}

export function getPoll(apiBase: string, token: string, channelId: string, pollId: string) {
  return request<Poll>(apiBase, `/channels/${channelId}/polls/${pollId}`, { token });
}

// Replaces the caller's ENTIRE vote for this poll — a single-select poll
// always sends exactly one id, a multi-select poll sends the full current
// selection. Use clearPollVotes to retract entirely rather than posting [].
export function votePoll(apiBase: string, token: string, channelId: string, pollId: string, optionIds: string[]) {
  return request<PollVoteState>(apiBase, `/channels/${channelId}/polls/${pollId}/votes`, {
    method: "POST",
    token,
    body: JSON.stringify({ option_ids: optionIds }),
  });
}

export function clearPollVotes(apiBase: string, token: string, channelId: string, pollId: string) {
  return request<PollVoteState>(apiBase, `/channels/${channelId}/polls/${pollId}/votes`, {
    method: "DELETE",
    token,
  });
}

// Bookmarks are private to the calling user (never visible to anyone
// else) and folder-organized — the opposite of a pin, which is
// channel-shared. See lib/types.ts's Bookmark/BookmarkFolder.

export function createBookmarkFolder(apiBase: string, token: string, name: string) {
  return request<BookmarkFolder>(apiBase, "/bookmarks/folders", {
    method: "POST",
    token,
    body: JSON.stringify({ name }),
  });
}

export function listBookmarkFolders(apiBase: string, token: string) {
  return request<BookmarkFolder[]>(apiBase, "/bookmarks/folders", { token });
}

export function renameBookmarkFolder(apiBase: string, token: string, folderId: string, name: string) {
  return request<BookmarkFolder>(apiBase, `/bookmarks/folders/${folderId}`, {
    method: "PATCH",
    token,
    body: JSON.stringify({ name }),
  });
}

// Bookmarks filed in the folder are un-filed back to "unfiled," not
// deleted.
export function deleteBookmarkFolder(apiBase: string, token: string, folderId: string) {
  return request<void>(apiBase, `/bookmarks/folders/${folderId}`, { method: "DELETE", token });
}

// Idempotent — bookmarking an already-bookmarked message returns the
// existing bookmark unchanged rather than moving it into a
// newly-requested folderId (call moveBookmark to reorganize it).
export function createBookmark(apiBase: string, token: string, channelId: string, messageId: string, folderId?: string) {
  return request<Bookmark>(apiBase, "/bookmarks", {
    method: "POST",
    token,
    body: JSON.stringify({ channel_id: channelId, message_id: messageId, folder_id: folderId }),
  });
}

// folderId omitted lists every bookmark; "none" scopes to unfiled only;
// any other value scopes to that folder specifically.
export function listBookmarks(apiBase: string, token: string, folderId?: string) {
  const path = folderId ? `/bookmarks?folder_id=${encodeURIComponent(folderId)}` : "/bookmarks";
  return request<Bookmark[]>(apiBase, path, { token });
}

// Pass folderId as undefined to unfile the bookmark back to "unfiled."
export function moveBookmark(apiBase: string, token: string, bookmarkId: string, folderId: string | undefined) {
  return request<Bookmark>(apiBase, `/bookmarks/${bookmarkId}`, {
    method: "PATCH",
    token,
    body: JSON.stringify({ folder_id: folderId ?? null }),
  });
}

export function deleteBookmark(apiBase: string, token: string, bookmarkId: string) {
  return request<void>(apiBase, `/bookmarks/${bookmarkId}`, { method: "DELETE", token });
}
