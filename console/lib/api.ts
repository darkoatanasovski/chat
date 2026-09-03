import type {
  AppDailyMessages,
  AppSummary,
  AppUsage,
  BillingCheckout,
  ChannelMember,
  Credential,
  CreatedApp,
  DashboardBlock,
  DashboardChannel,
  DashboardOrg,
  DashboardPoll,
  DashboardUser,
  EndUser,
  EndUserToken,
  Invite,
  MessagesDaily,
  MessagesUsage,
  RegionMessages,
  RegionUsage,
  RevealedSecret,
  Session,
  TeamMember,
  UpdateAppRequest,
  Usage,
} from "./types";

// All api-* regional instances share the same control-plane Postgres, so
// which region's instance the dashboard talks to for org/app/credential/
// dashboard endpoints doesn't matter — this is unlike the demo app, which
// deliberately lets you pick a region to exercise cross-region behavior.
export const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8081";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, opts: RequestInit & { token?: string } = {}): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (opts.token) headers["Authorization"] = `Bearer ${opts.token}`;

  const res = await fetch(`${API_BASE}${path}`, { ...opts, headers });
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

export function signup(orgName: string, email: string, password: string) {
  return request<Session>("/dashboard/signup", {
    method: "POST",
    body: JSON.stringify({ org_name: orgName, email, password }),
  });
}

export function login(email: string, password: string) {
  return request<Session>("/dashboard/login", { method: "POST", body: JSON.stringify({ email, password }) });
}

export function me(token: string) {
  return request<{ user: DashboardUser; org: DashboardOrg }>("/dashboard/me", { token });
}

export function acceptInvite(inviteToken: string, password: string) {
  return request<Session>(`/dashboard/invites/${inviteToken}/accept`, {
    method: "POST",
    body: JSON.stringify({ password }),
  });
}

export function listApps(token: string, orgId: number) {
  return request<AppSummary[]>(`/organizations/${orgId}/apps`, { token });
}

export function createApp(token: string, orgId: number, name: string) {
  return request<CreatedApp>(`/organizations/${orgId}/apps`, {
    method: "POST",
    token,
    body: JSON.stringify({ name }),
  });
}

export function updateApp(token: string, appId: number, patch: UpdateAppRequest) {
  return request<AppSummary>(`/apps/${appId}`, {
    method: "PATCH",
    token,
    body: JSON.stringify(patch),
  });
}

export function listCredentials(token: string, appId: number) {
  return request<Credential[]>(`/apps/${appId}/credentials`, { token });
}

export function createCredential(token: string, appId: number) {
  return request<Credential>(`/apps/${appId}/credentials`, { method: "POST", token });
}

export function revokeCredential(token: string, appId: number, credentialId: string) {
  return request<void>(`/apps/${appId}/credentials/${credentialId}`, { method: "DELETE", token });
}

// Decrypts and returns a credential's secret on demand — works any time
// after creation, not just in the one-time "shown once" response, and
// works for revoked credentials too (revoking stops the secret from being
// usable, not from being visible to the org that owns it).
export function revealCredential(token: string, appId: number, credentialId: string) {
  return request<RevealedSecret>(`/apps/${appId}/credentials/${credentialId}/reveal`, { token });
}

export function listTeam(token: string) {
  return request<TeamMember[]>("/dashboard/team", { token });
}

export function createInvite(token: string, email: string, role: string) {
  return request<Invite>("/dashboard/team/invites", {
    method: "POST",
    token,
    body: JSON.stringify({ email, role }),
  });
}

export function listInvites(token: string) {
  return request<Invite[]>("/dashboard/team/invites", { token });
}

export function removeTeamMember(token: string, userId: string) {
  return request<void>(`/dashboard/team/${userId}`, { method: "DELETE", token });
}

export function getUsage(token: string) {
  return request<Usage>("/dashboard/usage", { token });
}

export function getRegionUsage(token: string) {
  return request<RegionUsage[]>("/dashboard/regions", { token });
}

export function getAppMessagesUsage(token: string, appId: number) {
  return request<MessagesUsage>(`/dashboard/apps/${appId}/messages`, { token });
}

// Org-wide messages total + by-region breakdown, fanned out client-side
// across every app in the org (there's no org-wide /dashboard/messages
// endpoint anymore — messages usage is a per-app dashboard concept now,
// see handleDashboardAppMessages) and summed here for the Overview page.
export async function getOrgMessagesUsage(token: string, orgId: number): Promise<MessagesUsage> {
  const apps = await listApps(token, orgId);
  const perApp = await Promise.all(apps.map((a) => getAppMessagesUsage(token, a.app_id)));
  const total = perApp.reduce((sum, u) => sum + u.total, 0);
  const byRegion = new Map<string, number>();
  for (const u of perApp) {
    for (const r of u.by_region) {
      byRegion.set(r.region, (byRegion.get(r.region) ?? 0) + r.messages);
    }
  }
  return { total, by_region: Array.from(byRegion, ([region, messages]) => ({ region, messages })) };
}

// Backs the Apps grid's per-app card: an all-time total plus a 7-day
// daily breakdown for each app, so a card can show "today" and a mini
// sparkline alongside the total (not just the total) — see
// handleDashboardAppsMessagesDaily.
export function getAppsMessagesDaily(token: string) {
  return request<MessagesDaily>("/dashboard/apps/messages/daily", { token });
}

export function listEndUsers(token: string, appId: number) {
  return request<EndUser[]>(`/dashboard/apps/${appId}/users`, { token });
}

export function createEndUser(token: string, appId: number, displayName: string, region: string) {
  return request<EndUser>(`/dashboard/apps/${appId}/users`, {
    method: "POST",
    token,
    body: JSON.stringify({ display_name: displayName, region }),
  });
}

/** Mints a short-lived client token for an existing end-user so the
 * Playground (app/console/playground) can drive the end-user API as them.
 * Backed by handleDashboardMintEndUserToken; 404 for a user that isn't in
 * this app. */
export function mintEndUserToken(token: string, appId: number, userId: string) {
  return request<EndUserToken>(`/dashboard/apps/${appId}/users/${userId}/token`, { method: "POST", token });
}

export function listDashboardChannels(token: string, appId: number) {
  return request<DashboardChannel[]>(`/dashboard/apps/${appId}/channels`, { token });
}

export function createDashboardChannel(token: string, appId: number, name: string, creatorUserId: string) {
  return request<DashboardChannel>(`/dashboard/apps/${appId}/channels`, {
    method: "POST",
    token,
    body: JSON.stringify({ name, creator_user_id: creatorUserId }),
  });
}

export function listChannelMembers(token: string, channelId: string) {
  return request<ChannelMember[]>(`/dashboard/channels/${channelId}/members`, { token });
}

export function addChannelMember(token: string, channelId: string, userId: string) {
  return request<ChannelMember>(`/dashboard/channels/${channelId}/members`, {
    method: "POST",
    token,
    body: JSON.stringify({ user_id: userId }),
  });
}

export function removeChannelMember(token: string, channelId: string, userId: string) {
  return request<void>(`/dashboard/channels/${channelId}/members/${userId}`, { method: "DELETE", token });
}

export function listDashboardBlocks(token: string, appId: number) {
  return request<DashboardBlock[]>(`/dashboard/apps/${appId}/blocks`, { token });
}

/** Scoped to one app, newest-first, capped server-side (dashboardPollsLimit) —
 * an oversight view, not a paginated feed. */
export function listAppDashboardPolls(token: string, appId: number) {
  return request<DashboardPoll[]>(`/dashboard/apps/${appId}/polls`, { token });
}

export function createBillingCheckout(token: string, tier: string) {
  return request<BillingCheckout>("/dashboard/billing/checkout", {
    method: "POST",
    token,
    body: JSON.stringify({ tier }),
  });
}

export type {
  AppDailyMessages,
  AppSummary,
  AppUsage,
  BillingCheckout,
  ChannelMember,
  Credential,
  CreatedApp,
  DashboardBlock,
  DashboardChannel,
  EndUser,
  Invite,
  MessagesDaily,
  MessagesUsage,
  RegionMessages,
  RegionUsage,
  TeamMember,
  Usage,
};
