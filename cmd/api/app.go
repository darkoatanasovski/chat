// cmd/api is the stateless HTTP service implementing the REST surface in
// INSTRUCTIONS.md §36. It owns no long-lived per-client state (any instance
// can serve any request) and forwards channel writes to the channel's home
// region when it isn't the instance currently handling the request
// (INSTRUCTIONS.md §5/§27).
package main

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/apps"
	"github.com/darkoatanasovski/chat/internal/blocks"
	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/membership"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/organizations"
	"github.com/darkoatanasovski/chat/internal/orgusers"
	"github.com/darkoatanasovski/chat/internal/platform/auth"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/reactions"
	"github.com/darkoatanasovski/chat/internal/readstate"
	"github.com/darkoatanasovski/chat/internal/realtime"
	"github.com/darkoatanasovski/chat/internal/routing"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
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
	router *routing.Router
	region *routing.RegionResolver
	quota  *quota.Quota

	controlPool *pgxpool.Pool
	shardPools  pgstorage.ShardPools

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
	readStateRepo   *readstate.Repo
	blocksRepo      *blocks.Repo
	membershipCache *realtime.MembershipCache
	blocksCache     *realtime.BlocksCache

	peerClient *http.Client
}

func (a *App) shardPoolFor(channelID string) (pool *pgxpool.Pool, physicalShardID string, virtualShard int, err error) {
	physicalShardID, virtualShard, err = a.router.Resolve(channelID)
	if err != nil {
		return nil, "", 0, err
	}
	pool, err = a.shardPools.Get(physicalShardID)
	return pool, physicalShardID, virtualShard, err
}

func newPeerClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}
