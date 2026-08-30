package main

import (
	"context"
	"net/http"

	"github.com/darkoatanasovski/chat/internal/platform/health"
)

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /organizations", a.instrument("create_org", a.handleCreateOrg))
	mux.HandleFunc("POST /organizations/{org_id}/apps", a.instrument("create_app", a.requireOrgAuth(a.handleCreateApp)))
	mux.HandleFunc("GET /organizations/{org_id}/apps", a.instrument("list_apps", a.requireOrgAuth(a.handleListApps)))
	mux.HandleFunc("PATCH /apps/{app_id}", a.instrument("update_app", a.requireOrgAuth(a.handleUpdateApp)))
	mux.HandleFunc("POST /apps/{app_id}/credentials", a.instrument("create_app_credential", a.requireOrgAuth(a.handleCreateAppCredential)))
	mux.HandleFunc("GET /apps/{app_id}/credentials", a.instrument("list_app_credentials", a.requireOrgAuth(a.handleListAppCredentials)))
	mux.HandleFunc("DELETE /apps/{app_id}/credentials/{credential_id}", a.instrument("revoke_app_credential", a.requireOrgAuth(a.handleRevokeAppCredential)))
	mux.HandleFunc("GET /apps/{app_id}/credentials/{credential_id}/reveal", a.instrument("reveal_app_credential", a.requireOrgAuth(a.handleRevealAppCredential)))

	mux.HandleFunc("POST /apps/token", a.instrument("create_app_token", a.requireAppCredentials(a.handleCreateAppToken)))
	mux.HandleFunc("POST /users", a.instrument("create_user", a.requireAppJWT(a.handleCreateUser)))

	mux.HandleFunc("POST /channels", a.instrument("create_channel", a.requireAuth(a.handleCreateChannel)))
	mux.HandleFunc("POST /channels/{id}/members", a.instrument("add_member", a.requireAuth(a.handleAddMember)))
	mux.HandleFunc("GET /channels/{id}/members", a.instrument("list_members", a.requireAuth(a.handleListMembers)))
	mux.HandleFunc("POST /channels/{id}/messages", a.instrument("send_message", a.requireAuth(a.handleSendMessage)))
	mux.HandleFunc("GET /channels/{id}/messages", a.instrument("list_messages", a.requireAuth(a.handleListMessages)))
	mux.HandleFunc("PATCH /channels/{id}/messages/{message_id}", a.instrument("edit_message", a.requireAuth(a.handleEditMessage)))
	mux.HandleFunc("POST /channels/{id}/messages/{message_id}/reactions", a.instrument("add_reaction", a.requireAuth(a.handleAddReaction)))
	mux.HandleFunc("DELETE /channels/{id}/messages/{message_id}/reactions/{reaction}", a.instrument("remove_reaction", a.requireAuth(a.handleRemoveReaction)))
	mux.HandleFunc("POST /channels/{id}/messages/{message_id}/pin", a.instrument("pin_message", a.requireAuth(a.handlePinMessage)))
	mux.HandleFunc("DELETE /channels/{id}/messages/{message_id}/pin", a.instrument("unpin_message", a.requireAuth(a.handleUnpinMessage)))
	mux.HandleFunc("GET /channels/{id}/pinned-messages", a.instrument("list_pinned_messages", a.requireAuth(a.handleListPinnedMessages)))
	mux.HandleFunc("POST /channels/{id}/polls", a.instrument("create_poll", a.requireAuth(a.handleCreatePoll)))
	mux.HandleFunc("GET /channels/{id}/polls/{poll_id}", a.instrument("get_poll", a.requireAuth(a.handleGetPoll)))
	mux.HandleFunc("POST /channels/{id}/polls/{poll_id}/votes", a.instrument("vote_poll", a.requireAuth(a.handleVotePoll)))
	mux.HandleFunc("DELETE /channels/{id}/polls/{poll_id}/votes", a.instrument("clear_poll_votes", a.requireAuth(a.handleClearPollVotes)))
	mux.HandleFunc("POST /channels/{id}/read", a.instrument("mark_read", a.requireAuth(a.handleMarkRead)))
	mux.HandleFunc("GET /channels/{id}/read-state", a.instrument("list_read_state", a.requireAuth(a.handleListReadState)))
	mux.HandleFunc("GET /channels/{id}/messages/search", a.instrument("search_messages", a.requireAuth(a.handleSearchMessages)))
	mux.HandleFunc("POST /channels/{id}/events", a.instrument("send_custom_event", a.requireAuth(a.handleSendCustomEvent)))
	mux.HandleFunc("POST /channels/{id}/mutes", a.instrument("mute_user", a.requireAuth(a.handleMuteUser)))
	mux.HandleFunc("DELETE /channels/{id}/mutes/{user_id}", a.instrument("unmute_user", a.requireAuth(a.handleUnmuteUser)))
	mux.HandleFunc("GET /channels/{id}/mutes", a.instrument("list_mutes", a.requireAuth(a.handleListMutes)))
	mux.HandleFunc("POST /channels/{id}/messages/{message_id}/reminders", a.instrument("create_message_reminder", a.requireAuth(a.handleCreateMessageReminder)))
	mux.HandleFunc("DELETE /channels/{id}/messages/{message_id}/reminders", a.instrument("cancel_message_reminder", a.requireAuth(a.handleCancelMessageReminder)))
	mux.HandleFunc("GET /channels/{id}/messages/pending", a.instrument("list_pending_messages", a.requireAuth(a.handleListPendingMessages)))
	mux.HandleFunc("POST /channels/{id}/messages/{message_id}/approve", a.instrument("approve_message", a.requireAuth(a.handleApproveMessage)))
	mux.HandleFunc("POST /channels/{id}/messages/{message_id}/reject", a.instrument("reject_message", a.requireAuth(a.handleRejectMessage)))

	mux.HandleFunc("GET /users/me/channels", a.instrument("list_my_channels", a.requireAuth(a.handleListMyChannels)))

	mux.HandleFunc("POST /blocks", a.instrument("block_user", a.requireAuth(a.handleBlockUser)))
	mux.HandleFunc("DELETE /blocks/{user_id}", a.instrument("unblock_user", a.requireAuth(a.handleUnblockUser)))
	mux.HandleFunc("GET /blocks", a.instrument("list_blocks", a.requireAuth(a.handleListBlocks)))

	mux.HandleFunc("POST /bookmarks/folders", a.instrument("create_bookmark_folder", a.requireAuth(a.handleCreateBookmarkFolder)))
	mux.HandleFunc("GET /bookmarks/folders", a.instrument("list_bookmark_folders", a.requireAuth(a.handleListBookmarkFolders)))
	mux.HandleFunc("PATCH /bookmarks/folders/{folder_id}", a.instrument("rename_bookmark_folder", a.requireAuth(a.handleRenameBookmarkFolder)))
	mux.HandleFunc("DELETE /bookmarks/folders/{folder_id}", a.instrument("delete_bookmark_folder", a.requireAuth(a.handleDeleteBookmarkFolder)))
	mux.HandleFunc("POST /bookmarks", a.instrument("create_bookmark", a.requireAuth(a.handleCreateBookmark)))
	mux.HandleFunc("GET /bookmarks", a.instrument("list_bookmarks", a.requireAuth(a.handleListBookmarks)))
	mux.HandleFunc("PATCH /bookmarks/{bookmark_id}", a.instrument("move_bookmark", a.requireAuth(a.handleMoveBookmark)))
	mux.HandleFunc("DELETE /bookmarks/{bookmark_id}", a.instrument("delete_bookmark", a.requireAuth(a.handleDeleteBookmark)))

	// Dashboard: real per-person accounts (internal/orgusers) on top of the
	// same organizations. Signup/login/accept-invite are public, matching
	// this V1's existing trust model; everything else needs a dashboard
	// session, team management needs the owner role specifically.
	mux.HandleFunc("POST /dashboard/signup", a.instrument("dashboard_signup", a.handleDashboardSignup))
	mux.HandleFunc("POST /dashboard/login", a.instrument("dashboard_login", a.handleDashboardLogin))
	mux.HandleFunc("POST /dashboard/invites/{token}/accept", a.instrument("dashboard_accept_invite", a.handleAcceptInvite))
	mux.HandleFunc("GET /dashboard/me", a.instrument("dashboard_me", a.requireOrgUser(a.handleDashboardMe)))
	mux.HandleFunc("GET /dashboard/usage", a.instrument("dashboard_usage", a.requireOrgUser(a.handleDashboardUsage)))
	mux.HandleFunc("GET /dashboard/regions", a.instrument("dashboard_regions", a.requireOrgUser(a.handleDashboardRegions)))
	mux.HandleFunc("GET /dashboard/apps/messages/daily", a.instrument("dashboard_apps_messages_daily", a.requireOrgUser(a.handleDashboardAppsMessagesDaily)))
	mux.HandleFunc("GET /dashboard/team", a.instrument("dashboard_list_team", a.requireOrgUser(a.handleListTeam)))
	mux.HandleFunc("POST /dashboard/team/invites", a.instrument("dashboard_create_invite", a.requireOwnerRole(a.handleCreateInvite)))
	mux.HandleFunc("GET /dashboard/team/invites", a.instrument("dashboard_list_invites", a.requireOwnerRole(a.handleListInvites)))
	mux.HandleFunc("DELETE /dashboard/team/{user_id}", a.instrument("dashboard_remove_member", a.requireOwnerRole(a.handleRemoveTeamMember)))

	// End-user and channel administration, scoped to one app at a time —
	// lets an org operator create end-users/channels and manage channel
	// membership directly from the dashboard, alongside the same
	// app-credentialed /users and /channels routes a business's own
	// backend would use programmatically.
	mux.HandleFunc("GET /dashboard/apps/{app_id}/users", a.instrument("dashboard_list_end_users", a.requireOrgUser(a.handleDashboardListEndUsers)))
	mux.HandleFunc("POST /dashboard/apps/{app_id}/users", a.instrument("dashboard_create_end_user", a.requireOrgUser(a.handleDashboardCreateEndUser)))
	mux.HandleFunc("GET /dashboard/apps/{app_id}/channels", a.instrument("dashboard_list_channels", a.requireOrgUser(a.handleDashboardListChannels)))
	mux.HandleFunc("POST /dashboard/apps/{app_id}/channels", a.instrument("dashboard_create_channel", a.requireOrgUser(a.handleDashboardCreateChannel)))
	mux.HandleFunc("GET /dashboard/channels/{channel_id}/members", a.instrument("dashboard_list_channel_members", a.requireOrgUser(a.handleDashboardListChannelMembers)))
	mux.HandleFunc("POST /dashboard/channels/{channel_id}/members", a.instrument("dashboard_add_channel_member", a.requireOrgUser(a.handleDashboardAddChannelMember)))
	mux.HandleFunc("DELETE /dashboard/channels/{channel_id}/members/{user_id}", a.instrument("dashboard_remove_channel_member", a.requireOrgUser(a.handleDashboardRemoveChannelMember)))
	mux.HandleFunc("GET /dashboard/apps/{app_id}/blocks", a.instrument("dashboard_list_blocks", a.requireOrgUser(a.handleDashboardListBlocks)))
	mux.HandleFunc("POST /dashboard/apps/{app_id}/blocks", a.instrument("dashboard_block_user", a.requireOrgUser(a.handleDashboardBlockUser)))
	mux.HandleFunc("DELETE /dashboard/apps/{app_id}/blocks/{blocker_id}/{blocked_id}", a.instrument("dashboard_unblock_user", a.requireOrgUser(a.handleDashboardUnblockUser)))
	mux.HandleFunc("GET /dashboard/apps/{app_id}/messages", a.instrument("dashboard_app_messages", a.requireOrgUser(a.handleDashboardAppMessages)))
	mux.HandleFunc("GET /dashboard/apps/{app_id}/polls", a.instrument("dashboard_app_polls", a.requireOrgUser(a.handleDashboardAppPolls)))

	// Billing: self-serve plan upgrades via Dodo Payments hosted checkout
	// (see cmd/api/handlers_billing.go). The webhook route is deliberately
	// NOT under /dashboard/* and carries no bearer auth — it's Dodo's
	// servers calling us, authenticated by webhook signature instead.
	mux.HandleFunc("POST /dashboard/billing/checkout", a.instrument("dashboard_billing_checkout", a.requireOrgUser(a.handleCreateBillingCheckout)))
	mux.HandleFunc("POST /dodo/webhook", a.instrument("dodo_webhook", a.handleDodoWebhook))

	// Apps/credentials management: the dashboard calls these SAME routes
	// directly with its org-user session token — requireOrgAuth already
	// accepts either an org-admin token or a dashboard session and resolves
	// the same OrgIdentity either way, so no separate /dashboard/* routes
	// are needed here.
	checks := map[string]health.Checker{
		"control": func(ctx context.Context) error { return a.controlPool.Ping(ctx) },
	}
	for _, ps := range a.router.PhysicalShards() {
		pool, err := a.shardPools.Get(ps.ID)
		if err != nil {
			continue
		}
		checks[ps.ID] = pool.Ping
	}
	mux.Handle("GET /healthz", health.Handler(checks))

	return corsMiddleware(a.cfg.CORSAllowedOrigins, mux)
}
