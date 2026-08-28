import type { Channel, ChannelSummary, Member, Message } from "./types";

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

// createUser is called with the App credentials of the org whose tier the
// caller picked — tier is never sent in the request body. It's resolved
// live from the App's owning Organization on the server, matching the
// B2B model: this is the endpoint a business's own backend calls, and the
// demo just plays that role using the credentials deploy/seed.sh mints.
export function createUser(apiBase: string, displayName: string, region: string, appCredentials: AppCredentials) {
  return request<{ user_id: string; display_name: string; region: string; tier: string; token: string }>(
    apiBase,
    "/users",
    { method: "POST", body: JSON.stringify({ display_name: displayName, region }), appCredentials }
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

export function sendMessage(apiBase: string, token: string, channelId: string, clientMessageId: string, body: string) {
  return request<Message>(apiBase, `/channels/${channelId}/messages`, {
    method: "POST",
    token,
    body: JSON.stringify({ client_message_id: clientMessageId, body }),
  });
}

export function listMessages(apiBase: string, token: string, channelId: string, before?: number, limit = 50) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (before) params.set("before", String(before));
  return request<Message[]>(apiBase, `/channels/${channelId}/messages?${params.toString()}`, { token });
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
