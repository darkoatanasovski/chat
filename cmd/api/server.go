package api

import (
	"context"
	"net/http"

	"github.com/darkoatanasovski/chat/internal/platform/health"
)

// dataRoutes is the per-cell DATA plane (docs/adr/0006-cell-based-tenant-routing.md):
// the apikey/user-token-scoped surface a business's own end-users exercise.
// The edge router sends each of these to the one cell the app is pinned to,
// so every request here is local to this instance's cell.
func (a *App) dataRoutes() http.Handler {
	mux := http.NewServeMux()
	a.registerDataRoutes(mux)
	mux.Handle("GET /healthz", health.Handler(map[string]health.Checker{
		"config": func(ctx context.Context) error { return a.configPool.Ping(ctx) },
		"cell":   func(ctx context.Context) error { return a.cellPool.Ping(ctx) },
	}))
	return corsMiddleware(a.cfg.CORSAllowedOrigins, mux)
}

// controlRoutes is the global CONTROL plane: the org/dashboard/billing surface
// (config DB, plus cross-cell reads/writes for dashboard admin). It runs as
// its own service (RunControl); the router forwards control-plane paths here.
func (a *App) controlRoutes() http.Handler {
	mux := http.NewServeMux()
	a.registerControlRoutes(mux)
	checks := map[string]health.Checker{
		"config": func(ctx context.Context) error { return a.configPool.Ping(ctx) },
	}
	for key, pool := range a.cellPools {
		p := pool
		checks["cell:"+key] = func(ctx context.Context) error { return p.Ping(ctx) }
	}
	mux.Handle("GET /healthz", health.Handler(checks))
	return corsMiddleware(a.cfg.CORSAllowedOrigins, mux)
}

// testRoutes mounts BOTH planes on one mux — used only by the in-process test
// harness (handlers_test.go), which exercises control and data endpoints
// against a single *App. In production the two planes run as separate
// services (RunAPI / RunControl) behind the router.
func (a *App) testRoutes() http.Handler {
	mux := http.NewServeMux()
	a.registerDataRoutes(mux)
	a.registerControlRoutes(mux)
	return corsMiddleware(a.cfg.CORSAllowedOrigins, mux)
}

func (a *App) registerDataRoutes(mux *http.ServeMux) {
	// POST /users runs on the short-lived app JWT minted by /apps/token
	// (a control-plane route); the router forwards it here by the JWT's
	// api_key claim.
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
	mux.HandleFunc("POST /channels/{id}/messages/{message_id}/translate", a.instrument("translate_message", a.requireAuth(a.handleTranslateMessage)))
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
}

func (a *App) registerControlRoutes(mux *http.ServeMux) {
	// Read-through placement lookup for the edge router (KV-miss fallback).
	mux.HandleFunc("GET /internal/placement", a.instrument("placement_lookup", a.handlePlacementLookup))

	mux.HandleFunc("POST /organizations", a.instrument("create_org", a.handleCreateOrg))
	mux.HandleFunc("POST /organizations/{org_id}/apps", a.instrument("create_app", a.requireOrgAuth(a.handleCreateApp)))
	mux.HandleFunc("GET /organizations/{org_id}/apps", a.instrument("list_apps", a.requireOrgAuth(a.handleListApps)))
	mux.HandleFunc("PATCH /apps/{app_id}", a.instrument("update_app", a.requireOrgAuth(a.handleUpdateApp)))
	mux.HandleFunc("POST /apps/{app_id}/credentials", a.instrument("create_app_credential", a.requireOrgAuth(a.handleCreateAppCredential)))
	mux.HandleFunc("GET /apps/{app_id}/credentials", a.instrument("list_app_credentials", a.requireOrgAuth(a.handleListAppCredentials)))
	mux.HandleFunc("DELETE /apps/{app_id}/credentials/{credential_id}", a.instrument("revoke_app_credential", a.requireOrgAuth(a.handleRevokeAppCredential)))
	mux.HandleFunc("GET /apps/{app_id}/credentials/{credential_id}/reveal", a.instrument("reveal_app_credential", a.requireOrgAuth(a.handleRevealAppCredential)))

	// A business's backend exchanges its app key+secret (Basic auth) for a
	// short-lived JWT here, then uses that JWT against the data plane
	// (POST /users, etc.). This is a config-DB operation, so it lives in the
	// control plane rather than any single cell.
	mux.HandleFunc("POST /apps/token", a.instrument("create_app_token", a.requireAppCredentials(a.handleCreateAppToken)))

	// Dashboard: real per-person accounts (internal/orgusers) on top of the
	// same organizations.
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

	mux.HandleFunc("GET /dashboard/apps/{app_id}/users", a.instrument("dashboard_list_end_users", a.requireOrgUser(a.handleDashboardListEndUsers)))
	mux.HandleFunc("POST /dashboard/apps/{app_id}/users", a.instrument("dashboard_create_end_user", a.requireOrgUser(a.handleDashboardCreateEndUser)))
	mux.HandleFunc("POST /dashboard/apps/{app_id}/users/{user_id}/token", a.instrument("dashboard_mint_end_user_token", a.requireOrgUser(a.handleDashboardMintEndUserToken)))
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
	mux.HandleFunc("GET /dashboard/apps/{app_id}/translations", a.instrument("dashboard_app_translations", a.requireOrgUser(a.handleDashboardAppTranslations)))

	mux.HandleFunc("POST /dashboard/billing/checkout", a.instrument("dashboard_billing_checkout", a.requireOrgUser(a.handleCreateBillingCheckout)))
	mux.HandleFunc("POST /dodo/webhook", a.instrument("dodo_webhook", a.handleDodoWebhook))
}
