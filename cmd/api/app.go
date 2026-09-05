// cmd/api is the stateless HTTP service implementing the REST surface in
// INSTRUCTIONS.md §36. It owns no long-lived per-client state (any instance
// can serve any request). It serves exactly the apps pinned to its own cell:
// the edge router (cmd/router) resolves apikey -> cell and sends each request
// to the right one, so this service never forwards cross-region or reaches
// another cell's data (docs/adr/0006-cell-based-tenant-routing.md).
package api

import (
	"fmt"

	"github.com/dodopayments/dodopayments-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/apps"
	"github.com/darkoatanasovski/chat/internal/blocks"
	"github.com/darkoatanasovski/chat/internal/bookmarks"
	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/membership"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/mutes"
	"github.com/darkoatanasovski/chat/internal/organizations"
	"github.com/darkoatanasovski/chat/internal/orgusers"
	"github.com/darkoatanasovski/chat/internal/platform/auth"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	"github.com/darkoatanasovski/chat/internal/polls"
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/reactions"
	"github.com/darkoatanasovski/chat/internal/readstate"
	"github.com/darkoatanasovski/chat/internal/realtime"
	"github.com/darkoatanasovski/chat/internal/reminders"
	"github.com/darkoatanasovski/chat/internal/routing"
	"github.com/darkoatanasovski/chat/internal/topology"
	"github.com/darkoatanasovski/chat/internal/translations"
	"github.com/darkoatanasovski/chat/internal/users"

	"log/slog"
)

// App holds every dependency an HTTP handler needs. Handlers are methods on
// *App so they share one set of connections/config without global state.
type App struct {
	cfg     config.Config
	log     *slog.Logger
	metrics *metrics.Metrics

	signer *auth.Signer
	// region resolves channel_id -> app_id for tenant-isolation checks.
	// Home-region write-forwarding is gone: the edge router already sent
	// this request to the one cell that owns the app (ADR 0006), so any
	// channel this instance sees is local and authoritative.
	region *routing.RegionResolver
	quota  *quota.Quota

	// configPool is the global config DB (orgs, apps, credentials, org
	// users/invites, translation usage). cellPool is THIS cell's own
	// database, holding every tenant-scoped table for the apps pinned here
	// (users, channels, membership, messages, …). There is no cross-cell
	// access and no virtual-shard indirection — a cell has one database.
	//
	// In the DATA-plane api (RunAPI) cellPool is the one cell this instance
	// serves. In the CONTROL-plane service (RunControl) cellPool is nil and
	// cellPools + topo are populated instead: the control plane is global and
	// reaches every cell's DB for admin/dashboard operations (cellPoolForApp
	// resolves an app's placement to its cell). topo is also used to place a
	// new app into a cell at creation.
	configPool *pgxpool.Pool
	cellPool   *pgxpool.Pool
	cellPools  map[string]*pgxpool.Pool // "region/shard" -> cell DB (control plane only)
	topo       *topology.Index

	orgsSvc  *organizations.Service
	orgsRepo *organizations.Repo

	orgUsersRepo   *orgusers.Repo
	orgInvitesRepo *orgusers.InviteRepo

	appsRepo       *apps.Repo
	appCredentials *apps.CredentialRepo
	appTiers       *apps.TierResolver

	usersSvc        *users.Service
	channelsSvc     *channels.Service
	channelsRepo    *channels.Repo
	membershipRepo  *membership.Repo
	messagesRepo    *messages.Repo
	reactionsRepo   *reactions.Repo
	pollsRepo       *polls.Repo
	readStateRepo   *readstate.Repo
	remindersRepo   *reminders.Repo
	blocksRepo      *blocks.Repo
	mutesRepo       *mutes.Repo
	bookmarksRepo   *bookmarks.Repo
	membershipCache *realtime.MembershipCache
	blocksCache     *realtime.BlocksCache

	// dodo is always non-nil, even when billing is unconfigured (empty
	// cfg.DodoAPIKey) — handlers_billing.go checks cfg.DodoAPIKey before
	// using it, so an unconfigured client is simply never called.
	dodo *dodopayments.Client

	// translationClient is always non-nil, even when unconfigured (empty
	// cfg.AzureTranslatorKey) — same "always construct it, let the client
	// itself report unconfigured" shape as dodo above (see
	// translations.Client.Configured).
	translationClient    *translations.Client
	translationsRepo     *translations.Repo
	translationUsageRepo *translations.UsageRepo
}

// shardPoolFor returns the database holding a channel's messages. In the cell
// model that is always this instance's own cell database — the app (and thus
// all its channels) is pinned to this cell, so there is no per-channel shard
// selection. The extra return values are retained for call-site compatibility
// (shardID names this cell; the virtual shard is gone, always 0).
func (a *App) shardPoolFor(string) (pool *pgxpool.Pool, physicalShardID string, virtualShard int, err error) {
	return a.cellPool, a.cfg.ShardID, 0, nil
}

// cellPoolForApp resolves an app's placement (region, shard) to that cell's
// database pool. Used only by the control plane (RunControl), whose dashboard
// and admin handlers read/write tenant data across the cells an org's apps
// live in. Returns an error if the app is unplaced or its cell isn't in this
// service's configured topology.
func (a *App) cellPoolForApp(app apps.App) (*pgxpool.Pool, error) {
	if app.Region == "" || app.Shard == "" {
		return nil, fmt.Errorf("app %d has no cell placement", app.AppID)
	}
	pool, ok := a.cellPools[app.Region+"/"+app.Shard]
	if !ok {
		return nil, fmt.Errorf("no cell pool for placement %s/%s (app %d)", app.Region, app.Shard, app.AppID)
	}
	return pool, nil
}
