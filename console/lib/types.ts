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

export interface RevealedSecret {
  secret: string;
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

// Derived server-side from recency of last activity (connecting, sending a
// message, reacting, marking read) rather than a separately tracked
// "connected" flag — see the API's internal/users.OnlineWindow. last_active
// is undefined for a user with no tracked activity yet.
export interface UserStatus {
  last_active_at?: string;
  is_online: boolean;
}

export interface EndUser {
  user_id: string;
  display_name: string;
  region: string;
  created_at: string;
  status: UserStatus;
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
  status: UserStatus;
}

export interface DashboardBlock {
  blocker_user_id: string;
  blocked_user_id: string;
}

export interface RegionMessages {
  region: string;
  messages: number;
}

export interface AppDailyMessages {
  app_id: number;
  name: string;
  total: number;
  today: number;
  /** Message counts for the last 7 UTC calendar days, oldest first —
   * always 7 entries (zero-filled), aligned with MessagesDaily.days. */
  daily: number[];
}

export interface MessagesDaily {
  /** "YYYY-MM-DD", oldest first, same length/order as each app's `daily`. */
  days: string[];
  apps: AppDailyMessages[];
}

export interface MessagesUsage {
  total: number;
  by_region: RegionMessages[];
}

export interface BillingCheckout {
  checkout_url: string;
}

export interface DashboardPollOption {
  option_id: string;
  label: string;
  vote_count: number;
}

/** A poll is a standalone entity a message can attach via poll_id, not a
 * property of the message itself — this view is org-wide oversight across
 * every app/channel, read-only (polls are created by an app's own
 * end-users, not from the dashboard). */
export interface DashboardPoll {
  poll_id: string;
  channel_id: string;
  channel_name: string;
  app_id: number;
  app_name: string;
  question: string;
  multi_select: boolean;
  /** Absent for a poll that never closes on its own. */
  closes_at?: string;
  created_at: string;
  options: DashboardPollOption[];
  total_voters: number;
}
