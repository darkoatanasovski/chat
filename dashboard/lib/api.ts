import type {
  AppSummary,
  AppUsage,
  ChannelMember,
  Credential,
  CreatedApp,
  DashboardChannel,
  DashboardOrg,
  DashboardUser,
  EndUser,
  Invite,
  RegionUsage,
  Session,
  TeamMember,
  Usage,
} from "./types";

// All api-* regional instances share the same control-plane Postgres, so
// which region's instance the dashboard talks to for org/app/credential/
// dashboard endpoints doesn't matter — this is unlike the demo app, which
// deliberately lets you pick a region to exercise cross-region behavior.
const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8081";

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

export function signup(orgName: string, tier: string, email: string, password: string) {
  return request<Session>("/dashboard/signup", {
    method: "POST",
    body: JSON.stringify({ org_name: orgName, tier, email, password }),
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

export function listCredentials(token: string, appId: number) {
  return request<Credential[]>(`/apps/${appId}/credentials`, { token });
}

export function createCredential(token: string, appId: number) {
  return request<Credential>(`/apps/${appId}/credentials`, { method: "POST", token });
}

export function revokeCredential(token: string, appId: number, credentialId: string) {
  return request<void>(`/apps/${appId}/credentials/${credentialId}`, { method: "DELETE", token });
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

export type { AppSummary, AppUsage, ChannelMember, Credential, CreatedApp, DashboardChannel, EndUser, Invite, RegionUsage, TeamMember, Usage };
