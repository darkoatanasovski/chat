package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dodopayments/dodopayments-go"
	"github.com/dodopayments/dodopayments-go/option"
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
	"github.com/darkoatanasovski/chat/internal/platform/debug"
	"github.com/darkoatanasovski/chat/internal/platform/logging"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	"github.com/darkoatanasovski/chat/internal/platform/secretbox"
	"github.com/darkoatanasovski/chat/internal/polls"
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/readstate"
	"github.com/darkoatanasovski/chat/internal/realtime"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
	redisstorage "github.com/darkoatanasovski/chat/internal/storage/redis"
	"github.com/darkoatanasovski/chat/internal/topology"
	"github.com/darkoatanasovski/chat/internal/translations"
	"github.com/darkoatanasovski/chat/internal/users"
)

// RunControl is the CONTROL-plane entrypoint (`chat control`): the global
// org/dashboard/billing service (docs/adr/0006-cell-based-tenant-routing.md).
// It reads/writes the config DB and reaches every cell's DB for dashboard
// admin (a legitimate control-plane responsibility, low-volume and operator-
// driven — distinct from end-user data traffic, which stays cell-local).
//
// Cell connections come from infra/topology.yaml: each cell's postgres
// dsn_env names the env var holding its DSN. The default cell (first in the
// topology) backs the dashboard's tenant-data repos for now; making every
// dashboard tenant handler resolve per-app via cellPoolForApp is the
// multi-cell follow-up.
func RunControl() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logging.New("control", cfg.Region)
	m := metrics.New("chat_control")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.ConfigDSN == "" {
		log.Error("CONFIG_DSN is required for the control plane")
		os.Exit(1)
	}
	configPool, err := pgstorage.Connect(ctx, cfg.ConfigDSN)
	if err != nil {
		log.Error("connect config db", "error", err)
		os.Exit(1)
	}

	topo, err := topology.Load(cfg.TopologyPath)
	if err != nil {
		log.Error("load topology", "error", err)
		os.Exit(1)
	}
	idx := topology.NewIndex(topo)

	// Connect every cell's DB from its topology dsn_env. The first cell is
	// the default that backs the tenant-data dashboard repos.
	realCellPools, defaultCell, err := connectCells(ctx, idx)
	if err != nil {
		log.Error("connect cell dbs", "error", err)
		os.Exit(1)
	}
	if defaultCell == nil {
		log.Error("topology has no cells to connect")
		os.Exit(1)
	}

	redisClient, err := redisstorage.ConnectFromEnv(ctx, cfg.ValkeyAddr, cfg.ValkeySentinelAddrs, cfg.ValkeyMasterName)
	if err != nil {
		log.Error("connect redis", "error", err)
		os.Exit(1)
	}

	tiers, err := quota.LoadTiers(cfg.TiersConfigPath)
	if err != nil {
		log.Error("load tiers config", "error", err)
		os.Exit(1)
	}
	q := quota.New(tiers, quota.NewRateLimiter(redisClient))

	secretBox, err := secretbox.New(cfg.AppSecretEncryptionKey)
	if err != nil {
		log.Error("build secretbox for app credentials", "error", err, "hint", "set APP_SECRET_ENCRYPTION_KEY to a base64-encoded 32-byte key, e.g. `openssl rand -base64 32`")
		os.Exit(1)
	}

	orgsRepo := organizations.NewRepo(configPool)
	appsRepo := apps.NewRepo(configPool)
	channelsRepo := channels.NewRepo(defaultCell)

	dodoOpts := []option.RequestOption{
		option.WithBearerToken(cfg.DodoAPIKey),
		option.WithWebhookKey(cfg.DodoWebhookKey),
	}
	if cfg.DodoLiveMode {
		dodoOpts = append(dodoOpts, option.WithEnvironmentLiveMode())
	} else {
		dodoOpts = append(dodoOpts, option.WithEnvironmentTestMode())
	}

	app := &App{
		cfg:            cfg,
		log:            log,
		metrics:        m,
		signer:         auth.NewSigner(cfg.AuthSecret),
		quota:          q,
		configPool:     configPool,
		cellPool:       defaultCell,
		cellPools:      realCellPools,
		topo:           idx,
		orgsSvc:        organizations.NewService(orgsRepo),
		orgsRepo:       orgsRepo,
		orgUsersRepo:   orgusers.NewRepo(configPool),
		orgInvitesRepo: orgusers.NewInviteRepo(configPool),
		appsRepo:       appsRepo,
		appCredentials: apps.NewCredentialRepo(configPool, secretBox),
		appTiers:       apps.NewTierResolver(redisClient, appsRepo.TierSource),
		usersSvc:       users.NewService(users.NewRepo(defaultCell)),
		channelsSvc:    channels.NewService(channelsRepo),
		channelsRepo:   channelsRepo,
		membershipRepo: membership.NewRepo(defaultCell),
		messagesRepo:   messages.NewRepo(),
		pollsRepo:      polls.NewRepo(),
		readStateRepo:  readstate.NewRepo(),
		blocksRepo:     blocks.NewRepo(defaultCell),
		mutesRepo:      mutes.NewRepo(defaultCell),
		bookmarksRepo:  bookmarks.NewRepo(defaultCell),
		membershipCache: realtime.NewMembershipCache(redisClient, m),
		dodo:            dodopayments.NewClient(dodoOpts...),

		translationClient:    translations.NewClient(translations.Config{Key: cfg.AzureTranslatorKey, Region: cfg.AzureTranslatorRegion, Endpoint: cfg.AzureTranslatorEndpoint}),
		translationsRepo:     translations.NewRepo(),
		translationUsageRepo: translations.NewUsageRepo(configPool),
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	debug.Mount(metricsMux)
	go func() {
		log.Info("metrics listening", "addr", cfg.MetricsAddr)
		if err := http.ListenAndServe(cfg.MetricsAddr, metricsMux); err != nil {
			log.Error("metrics server", "error", err)
		}
	}()

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: app.controlRoutes()}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	log.Info("control plane listening", "addr", cfg.HTTPAddr, "cells", len(realCellPools))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("http server", "error", err)
		os.Exit(1)
	}
}

// connectCells opens a Postgres pool for every cell in the topology, keyed by
// "region/shard", reading each cell's DSN from the env var its topology entry
// names (postgres.dsn_env). Returns the pools plus the default cell (the first
// declared), which backs the control plane's tenant-data dashboard repos.
func connectCells(ctx context.Context, idx *topology.Index) (map[string]*pgxpool.Pool, *pgxpool.Pool, error) {
	pools := map[string]*pgxpool.Pool{}
	var defaultCell *pgxpool.Pool
	for _, region := range idx.Regions() {
		for _, cell := range region.Cells {
			// A cell whose DSN env is unset simply isn't provisioned in this
			// deployment (e.g. a second region not run locally) — skip it
			// rather than failing. The control plane connects the cells that
			// exist; dashboard ops for an app in a skipped cell will surface
			// a clear "no cell pool" error via cellPoolForApp.
			dsn := os.Getenv(cell.Postgres.DSNEnv)
			if dsn == "" {
				continue
			}
			pool, err := pgstorage.Connect(ctx, dsn)
			if err != nil {
				return nil, nil, fmt.Errorf("cell %s/%s: %w", region.ID, cell.ID, err)
			}
			pools[region.ID+"/"+cell.ID] = pool
			if defaultCell == nil {
				defaultCell = pool
			}
		}
	}
	return pools, defaultCell, nil
}
