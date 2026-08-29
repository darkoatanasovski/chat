export interface DashboardUser {
  user_id: string;
  email: string;
  role: "owner" | "member";
}

export interface DashboardOrg {
  org_id: number;
  name: string;
  tier: string;
}

export interface Session {
  token: string;
  user: DashboardUser;
  org: DashboardOrg;
}

export interface AppSummary {
  app_id: number;
  name: string;
  created_at: string;
}

export interface Credential {
  credential_id: string;
  key: string;
  secret?: string;
  created_at: string;
  revoked_at?: string;
}

export interface CreatedApp extends AppSummary {
  credential: Credential;
}

export interface TeamMember {
  user_id: string;
  email: string;
  role: string;
  created_at: string;
}

export interface Invite {
  invite_id: string;
  email: string;
  role: string;
  token?: string;
  expires_at: string;
  created_at: string;
}

export interface UsageCount {
  used: number;
  limit: number;
}

export interface AppUsage {
  app_id: number;
  name: string;
  users: number;
  channels: number;
}

export interface Usage {
  tier: string;
  apps: UsageCount;
  apps_detail: AppUsage[];
}

export interface RegionUsage {
  region: string;
  users: number;
}

export interface EndUser {
  user_id: string;
  display_name: string;
  region: string;
  created_at: string;
}

export interface DashboardChannel {
  channel_id: string;
  name: string;
  home_region: string;
  creator_name: string;
  member_count: number;
  created_at: string;
}

export interface ChannelMember {
  user_id: string;
  display_name: string;
}

export interface DashboardBlock {
  blocker_user_id: string;
  blocked_user_id: string;
}

export interface RegionMessages {
  region: string;
  messages: number;
}

export interface MessagesUsage {
  total: number;
  by_region: RegionMessages[];
}
