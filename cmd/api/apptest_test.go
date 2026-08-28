package main

import (
	"context"
	"io"
	"log/slog"
	"sync"

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
	redisstorage "github.com/darkoatanasovski/chat/internal/storage/redis"
	"github.com/darkoatanasovski/chat/internal/users"
)

// buildTestApp wires an *App exactly like cmd/api/main.go does, but against
// the docker-compose dev stack's host-exposed ports and with a fixed test
// AuthSecret, so handler tests exercise the real routing/quota/auth/storage
// stack in-process rather than mocking it. promauto registers metrics
// against the global Prometheus registry, so this must run at most once per
// test binary (see testApp in handlers_test.go).
func buildTestApp() (*App, error) {
	ctx := context.Background()
	cfg := config.Config{
		Region:             "eu",
		AuthSecret:         "test-secret-do-not-use-in-prod",
		ShardsConfigPath:   "../../deploy/shards.yaml",
		TiersConfigPath:    "../../deploy/tiers.yaml",
		CORSAllowedOrigins: []string{"http://localhost:3000"},
	}

	controlPool, err := pgstorage.Connect(ctx, "postgres://chat:chat@localhost:5433/chat?sslmode=disable")
	if err != nil {
		return nil, err
	}
	shardA, err := pgstorage.Connect(ctx, "postgres://chat:chat@localhost:5434/chat?sslmode=disable")
	if err != nil {
		return nil, err
	}
	shardB, err := pgstorage.Connect(ctx, "postgres://chat:chat@localhost:5435/chat?sslmode=disable")
	if err != nil {
		return nil, err
	}
	shardPools := pgstorage.ShardPools{"shard-a": shardA, "shard-b": shardB}

	redisClient, err := redisstorage.Connect(ctx, "localhost:6379")
	if err != nil {
		return nil, err
	}

	shardsCfg, err := routing.LoadShardsConfig(cfg.ShardsConfigPath)
	if err != nil {
		return nil, err
	}
	router := routing.NewRouter(shardsCfg)

	tiers, err := quota.LoadTiers(cfg.TiersConfigPath)
	if err != nil {
		return nil, err
	}
	rateLimiter := quota.NewRateLimiter(redisClient)
	q := quota.New(tiers, rateLimiter)

	channelsRepo := channels.NewRepo(controlPool)
	region := routing.NewRegionResolver(redisClient, channelsRepo.RouteSource)

	orgsRepo := organizations.NewRepo(controlPool)
	orgUsersRepo := orgusers.NewRepo(controlPool)
	orgInvitesRepo := orgusers.NewInviteRepo(controlPool)
	appsRepo := apps.NewRepo(controlPool)
	appCredentials := apps.NewCredentialRepo(controlPool)
	appTiers := apps.NewTierResolver(redisClient, appsRepo.TierSource)

	m := metrics.New("chat_api_test")
	return &App{
		cfg:             cfg,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics:         m,
		signer:          auth.NewSigner(cfg.AuthSecret),
		router:          router,
		region:          region,
		quota:           q,
		controlPool:     controlPool,
		shardPools:      shardPools,
		orgsSvc:         organizations.NewService(orgsRepo),
		orgsRepo:        orgsRepo,
		orgUsersRepo:    orgUsersRepo,
		orgInvitesRepo:  orgInvitesRepo,
		appsRepo:        appsRepo,
		appCredentials:  appCredentials,
		appTiers:        appTiers,
		usersSvc:        users.NewService(users.NewRepo(controlPool)),
		channelsSvc:     channels.NewService(channelsRepo, router),
		channelsRepo:    channelsRepo,
		membershipRepo:  membership.NewRepo(controlPool),
		messagesRepo:    messages.NewRepo(),
		reactionsRepo:   reactions.NewRepo(),
		readStateRepo:   readstate.NewRepo(),
		blocksRepo:      blocks.NewRepo(controlPool),
		membershipCache: realtime.NewMembershipCache(redisClient, m),
		blocksCache:     realtime.NewBlocksCache(redisClient, m),
		peerClient:      newPeerClient(),
	}, nil
}

var (
	testAppOnce sync.Once
	sharedApp   *App
	testAppErr  error
)
