// Thin react-query wrappers around lib/api.ts. Nothing here changes what
// data is fetched or what shape it comes back in — the point is caching:
// every console page used to run its own useEffect+useState fetch on
// mount, so navigating away and back (Apps -> an app -> Apps, or just
// switching tabs) always blanked the screen back to skeletons while it
// refetched data it already had a second ago. Routing the same fetches
// through useQuery with a shared cache (see components/query-provider.tsx)
// means a revisited screen renders instantly from cache and only quietly
// revalidates in the background once past staleTime.
//
// Query keys are centralized in `queryKeys` below so a mutation can
// invalidate (or directly patch, via setQueryData) exactly the queries a
// change actually affects, without every caller needing to know the exact
// key shape.
import { useMutation, useQuery, useQueryClient, type UseQueryOptions } from "@tanstack/react-query";
import {
  addChannelMember,
  createApp,
  createBillingCheckout,
  createCredential,
  createDashboardChannel,
  createEndUser,
  createInvite,
  getAppMessagesUsage,
  getAppsMessagesDaily,
  getOrgMessagesUsage,
  getRegionUsage,
  getUsage,
  listApps,
  listAppDashboardPolls,
  listChannelMembers,
  listCredentials,
  listDashboardBlocks,
  listDashboardChannels,
  listEndUsers,
  listInvites,
  listTeam,
  removeChannelMember,
  removeTeamMember,
  revokeCredential,
  updateApp,
} from "./api";
import type { AppSummary, UpdateAppRequest } from "./types";

export const queryKeys = {
  usage: () => ["usage"] as const,
  orgMessagesUsage: (orgId: number) => ["org-messages-usage", orgId] as const,
  regionUsage: () => ["region-usage"] as const,
  apps: (orgId: number) => ["apps", orgId] as const,
  appsMessagesDaily: () => ["apps-messages-daily"] as const,
  appMessagesUsage: (appId: number) => ["app-messages-usage", appId] as const,
  endUsers: (appId: number) => ["end-users", appId] as const,
  dashboardChannels: (appId: number) => ["dashboard-channels", appId] as const,
  channelMembers: (channelId: string) => ["channel-members", channelId] as const,
  dashboardBlocks: (appId: number) => ["dashboard-blocks", appId] as const,
  appPolls: (appId: number) => ["app-polls", appId] as const,
  credentials: (appId: number) => ["credentials", appId] as const,
  team: () => ["team"] as const,
  invites: () => ["invites"] as const,
};

// ---- queries ----

export function useUsageQuery(token: string, enabled = true) {
  return useQuery({ queryKey: queryKeys.usage(), queryFn: () => getUsage(token), enabled });
}

export function useOrgMessagesUsageQuery(token: string, orgId: number) {
  return useQuery({ queryKey: queryKeys.orgMessagesUsage(orgId), queryFn: () => getOrgMessagesUsage(token, orgId) });
}

export function useRegionUsageQuery(token: string) {
  return useQuery({ queryKey: queryKeys.regionUsage(), queryFn: () => getRegionUsage(token) });
}

export function useAppsQuery(token: string, orgId: number, enabled = true) {
  return useQuery({ queryKey: queryKeys.apps(orgId), queryFn: () => listApps(token, orgId), enabled });
}

/** One app out of the same cached list the Apps grid and global search use
 * — landing on an app's page after clicking it from either place is a
 * cache hit, not a fresh loading state. */
export function useAppQuery(token: string, orgId: number, appId: number) {
  const query = useAppsQuery(token, orgId);
  const app = query.data?.find((a) => a.app_id === appId) ?? null;
  return { ...query, data: app };
}

export function useAppsMessagesDailyQuery(token: string) {
  return useQuery({ queryKey: queryKeys.appsMessagesDaily(), queryFn: () => getAppsMessagesDaily(token) });
}

export function useAppMessagesUsageQuery(token: string, appId: number) {
  return useQuery({ queryKey: queryKeys.appMessagesUsage(appId), queryFn: () => getAppMessagesUsage(token, appId) });
}

export function useEndUsersQuery(token: string, appId: number) {
  return useQuery({ queryKey: queryKeys.endUsers(appId), queryFn: () => listEndUsers(token, appId) });
}

export function useDashboardChannelsQuery(token: string, appId: number) {
  return useQuery({ queryKey: queryKeys.dashboardChannels(appId), queryFn: () => listDashboardChannels(token, appId) });
}

export function useChannelMembersQuery(
  token: string,
  channelId: string | undefined,
  options?: Pick<UseQueryOptions, "enabled">
) {
  return useQuery({
    queryKey: queryKeys.channelMembers(channelId ?? ""),
    queryFn: () => listChannelMembers(token, channelId as string),
    enabled: (options?.enabled ?? true) && !!channelId,
  });
}

export function useDashboardBlocksQuery(token: string, appId: number) {
  return useQuery({ queryKey: queryKeys.dashboardBlocks(appId), queryFn: () => listDashboardBlocks(token, appId) });
}

/** Total blocked pairs across every app in the org — the Overview page's
 * "Blocked" stat tile. Depends on the same apps list query, so it only
 * does real work once per org per staleTime window. */
export function useOrgBlockedCountQuery(token: string, orgId: number) {
  const appsQuery = useAppsQuery(token, orgId);
  const appIds = appsQuery.data?.map((a) => a.app_id) ?? [];
  return useQuery({
    queryKey: ["org-blocked-count", orgId, appIds.join(",")],
    queryFn: async () => {
      const perApp = await Promise.all(appIds.map((id) => listDashboardBlocks(token, id)));
      return perApp.reduce((sum, blocks) => sum + blocks.length, 0);
    },
    enabled: appsQuery.isSuccess,
  });
}

export function useAppDashboardPollsQuery(token: string, appId: number) {
  return useQuery({ queryKey: queryKeys.appPolls(appId), queryFn: () => listAppDashboardPolls(token, appId) });
}

export function useCredentialsQuery(token: string, appId: number) {
  return useQuery({ queryKey: queryKeys.credentials(appId), queryFn: () => listCredentials(token, appId) });
}

export function useTeamQuery(token: string) {
  return useQuery({ queryKey: queryKeys.team(), queryFn: () => listTeam(token) });
}

export function useInvitesQuery(token: string, enabled: boolean) {
  return useQuery({ queryKey: queryKeys.invites(), queryFn: () => listInvites(token), enabled });
}

// ---- mutations ----

/** Every list-mutating call below invalidates the query it affects rather
 * than trying to hand-patch the cache — these are all low-frequency,
 * dashboard-driven writes (create a channel, revoke a key), not a hot
 * path, so a refetch-on-invalidate is simpler and cheap enough. updateApp
 * is the one exception (see useUpdateAppMutation) since it already returns
 * the full updated resource and is called much more often, from the
 * Settings tab's per-toggle switches. */

// Deliberately does NOT invalidate ["usage"] here even though app count is
// part of that response — the Overview page uses usage.apps.used === 0 to
// decide whether to render the first-app wizard at all, and an
// auto-refetch firing while FirstAppStep is showing its "copy your key,
// it's shown once" success screen would unmount that screen out from
// under the user the instant the count ticks up. OverviewView instead
// refetches usage itself, once, when the user clicks past that screen.
export function useCreateAppMutation(token: string, orgId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => createApp(token, orgId, name),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.apps(orgId) });
      queryClient.invalidateQueries({ queryKey: queryKeys.appsMessagesDaily() });
    },
  });
}

/** Patches the app directly into the ["apps", orgId] cache instead of
 * invalidating it — that one list query backs the Apps grid, the Overview
 * page, global search, and this same app's own detail page (useAppQuery
 * derives from it), so a toggle flipped on the Settings tab shows up
 * everywhere else immediately rather than waiting on N separate refetches. */
export function useUpdateAppMutation(token: string, orgId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ appId, patch }: { appId: number; patch: UpdateAppRequest }) => updateApp(token, appId, patch),
    onSuccess: (updated) => {
      queryClient.setQueryData<AppSummary[]>(queryKeys.apps(orgId), (old) =>
        old ? old.map((a) => (a.app_id === updated.app_id ? updated : a)) : old
      );
    },
  });
}

export function useCreateCredentialMutation(token: string, appId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => createCredential(token, appId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.credentials(appId) }),
  });
}

export function useRevokeCredentialMutation(token: string, appId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (credentialId: string) => revokeCredential(token, appId, credentialId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.credentials(appId) }),
  });
}

export function useCreateEndUserMutation(token: string, appId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ displayName, region }: { displayName: string; region: string }) =>
      createEndUser(token, appId, displayName, region),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.endUsers(appId) }),
  });
}

export function useCreateDashboardChannelMutation(token: string, appId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ name, creatorUserId }: { name: string; creatorUserId: string }) =>
      createDashboardChannel(token, appId, name, creatorUserId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.dashboardChannels(appId) }),
  });
}

export function useAddChannelMemberMutation(token: string, appId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ channelId, userId }: { channelId: string; userId: string }) =>
      addChannelMember(token, channelId, userId),
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.channelMembers(vars.channelId) });
      // member_count on the channel list row needs the same refresh.
      queryClient.invalidateQueries({ queryKey: queryKeys.dashboardChannels(appId) });
    },
  });
}

export function useRemoveChannelMemberMutation(token: string, appId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ channelId, userId }: { channelId: string; userId: string }) =>
      removeChannelMember(token, channelId, userId),
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.channelMembers(vars.channelId) });
      queryClient.invalidateQueries({ queryKey: queryKeys.dashboardChannels(appId) });
    },
  });
}

export function useCreateInviteMutation(token: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ email, role }: { email: string; role: string }) => createInvite(token, email, role),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.invites() }),
  });
}

export function useRemoveTeamMemberMutation(token: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => removeTeamMember(token, userId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.team() }),
  });
}

export function useCreateBillingCheckoutMutation(token: string) {
  // No cache to invalidate — this redirects the browser to Dodo's hosted
  // checkout on success, it doesn't change anything the console has
  // cached (the tier only actually changes once billing/return's poll
  // sees it, which re-fetches session.org directly, not through
  // react-query).
  return useMutation({ mutationFn: (tier: string) => createBillingCheckout(token, tier) });
}
