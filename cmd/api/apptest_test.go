package api

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/apps"
	"github.com/darkoatanasovski/chat/internal/blocks"
	"github.com/darkoatanasovski/chat/internal/bookmarks"
	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/membership"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/organizations"
	"github.com/darkoatanasovski/chat/internal/orgusers"
	"github.com/darkoatanasovski/chat/internal/platform/auth"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	"github.com/darkoatanasovski/chat/internal/platform/secretbox"
	"github.com/darkoatanasovski/chat/internal/polls"
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/reactions"
	"github.com/darkoatanasovski/chat/internal/readstate"
	"github.com/darkoatanasovski/chat/internal/realtime"
	"github.com/darkoatanasovski/chat/internal/routing"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
	"github.com/darkoatanasovski/chat/internal/topology"
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
		Region:     "eu",
		ShardID:    "eu-a",
		AuthSecret: "test-secret-do-not-use-in-prod",
		// 32 bytes exactly (secretbox.KeySize) — any fixed test value
		// works, same spirit as AuthSecret above.
		AppSecretEncryptionKey: []byte("test-app-secret-encryption-key!!"),
		TiersConfigPath:        "../../deploy/tiers.yaml",
		CORSAllowedOrigins:     []string{"http://localhost:3000"},
	}

	// Cell model: one global config DB + this cell's own DB (ADR 0006). The
	// dev stack exposes the config DB on 5433 and the cell DB on 5434.
	configPool, err := pgstorage.Connect(ctx, "postgres://chat:chat@localhost:5433/chat?sslmode=disable")
	if err != nil {
		return nil, err
	}
	cellPool, err := pgstorage.Connect(ctx, "postgres://chat:chat@localhost:5434/chat?sslmode=disable")
	if err != nil {
		return nil, err
	}

	redisClient, err := redisstorage.Connect(ctx, "localhost:6379")
	if err != nil {
		return nil, err
	}

	// A one-region, one-cell topology matching cfg.Region/ShardID so
	// create-app placement resolves to this cell. cellPools lets the
	// control-plane routes (mounted alongside data routes by testRoutes)
	// resolve app→cell.
	var topoCell topology.Cell
	topoCell.ID = cfg.ShardID
	topo := topology.NewIndex(topology.Topology{
		Regions: []topology.Region{{ID: cfg.Region, Cells: []topology.Cell{topoCell}}},
	})
	cellPools := map[string]*pgxpool.Pool{cfg.Region + "/" + cfg.ShardID: cellPool}

	tiers, err := quota.LoadTiers(cfg.TiersConfigPath)
	if err != nil {
		return nil, err
	}
	rateLimiter := quota.NewRateLimiter(redisClient)
	q := quota.New(tiers, rateLimiter)

	channelsRepo := channels.NewRepo(cellPool)
	region := routing.NewRegionResolver(redisClient, channelsRepo.RouteSource)

	orgsRepo := organizations.NewRepo(configPool)
	orgUsersRepo := orgusers.NewRepo(configPool)
	orgInvitesRepo := orgusers.NewInviteRepo(configPool)
	appsRepo := apps.NewRepo(configPool)
	secretBox, err := secretbox.New(cfg.AppSecretEncryptionKey)
	if err != nil {
		return nil, err
	}
	appCredentials := apps.NewCredentialRepo(configPool, secretBox)
	appTiers := apps.NewTierResolver(redisClient, appsRepo.TierSource)

	m := metrics.New("chat_api_test")
	return &App{
		cfg:             cfg,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics:         m,
		signer:          auth.NewSigner(cfg.AuthSecret),
		region:          region,
		quota:           q,
		configPool:      configPool,
		cellPool:        cellPool,
		cellPools:       cellPools,
		topo:            topo,
		orgsSvc:         organizations.NewService(orgsRepo),
		orgsRepo:        orgsRepo,
		orgUsersRepo:    orgUsersRepo,
		orgInvitesRepo:  orgInvitesRepo,
		appsRepo:        appsRepo,
		appCredentials:  appCredentials,
		appTiers:        appTiers,
		usersSvc:        users.NewService(users.NewRepo(cellPool)),
		channelsSvc:     channels.NewService(channelsRepo),
		channelsRepo:    channelsRepo,
		membershipRepo:  membership.NewRepo(cellPool),
		messagesRepo:    messages.NewRepo(),
		reactionsRepo:   reactions.NewRepo(),
		pollsRepo:       polls.NewRepo(),
		readStateRepo:   readstate.NewRepo(),
		blocksRepo:      blocks.NewRepo(cellPool),
		bookmarksRepo:   bookmarks.NewRepo(cellPool),
		membershipCache: realtime.NewMembershipCache(redisClient, m),
		blocksCache:     realtime.NewBlocksCache(redisClient, m),
	}, nil
}

var (
	testAppOnce sync.Once
	sharedApp   *App
	testAppErr  error
)
