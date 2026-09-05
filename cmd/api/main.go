package api

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dodopayments/dodopayments-go"
	"github.com/dodopayments/dodopayments-go/option"

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
	"github.com/darkoatanasovski/chat/internal/reactions"
	"github.com/darkoatanasovski/chat/internal/readstate"
	"github.com/darkoatanasovski/chat/internal/realtime"
	"github.com/darkoatanasovski/chat/internal/reminders"
	"github.com/darkoatanasovski/chat/internal/routing"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
	redisstorage "github.com/darkoatanasovski/chat/internal/storage/redis"
	"github.com/darkoatanasovski/chat/internal/translations"
	"github.com/darkoatanasovski/chat/internal/users"
)

func Run() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logging.New("api", cfg.Region)
	m := metrics.New("chat_api")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The global config DB (orgs, apps, credentials) and this cell's own
	// database (all tenant data for the apps pinned here). No shard-a/shard-b
	// pair and no any-cell-reaches-any-shard access — see ADR 0006.
	configPool, err := pgstorage.Connect(ctx, cfg.ConfigDSN)
	if err != nil {
		log.Error("connect config db", "error", err)
		os.Exit(1)
	}
	cellPool, err := pgstorage.Connect(ctx, cfg.CellDSN)
	if err != nil {
		log.Error("connect cell db", "shard", cfg.ShardID, "error", err)
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
	rateLimiter := quota.NewRateLimiter(redisClient)
	q := quota.New(tiers, rateLimiter)

	// Tenant-scoped repos read the cell DB; global repos read the config DB.
	channelsRepo := channels.NewRepo(cellPool)
	region := routing.NewRegionResolver(redisClient, channelsRepo.RouteSource)

	orgsRepo := organizations.NewRepo(configPool)
	orgUsersRepo := orgusers.NewRepo(configPool)
	orgInvitesRepo := orgusers.NewInviteRepo(configPool)
	appsRepo := apps.NewRepo(configPool)

	// Only cmd/api ever mints or reveals app credentials, so this is the
	// one place APP_SECRET_ENCRYPTION_KEY is required — see config.go's
	// doc comment on AppSecretEncryptionKey for why Load() itself doesn't
	// enforce that (cmd/gateway and cmd/worker call Load() too, and never
	// touch it).
	secretBox, err := secretbox.New(cfg.AppSecretEncryptionKey)
	if err != nil {
		log.Error("build secretbox for app credentials", "error", err, "hint", "set APP_SECRET_ENCRYPTION_KEY to a base64-encoded 32-byte key, e.g. `openssl rand -base64 32`")
		os.Exit(1)
	}
	appCredentials := apps.NewCredentialRepo(configPool, secretBox)
	appTiers := apps.NewTierResolver(redisClient, appsRepo.TierSource)

	dodoOpts := []option.RequestOption{
		option.WithBearerToken(cfg.DodoAPIKey),
		option.WithWebhookKey(cfg.DodoWebhookKey),
	}
	if cfg.DodoLiveMode {
		dodoOpts = append(dodoOpts, option.WithEnvironmentLiveMode())
	} else {
		dodoOpts = append(dodoOpts, option.WithEnvironmentTestMode())
	}
	dodoClient := dodopayments.NewClient(dodoOpts...)

	translationClient := translations.NewClient(translations.Config{
		Key:      cfg.AzureTranslatorKey,
		Region:   cfg.AzureTranslatorRegion,
		Endpoint: cfg.AzureTranslatorEndpoint,
	})

	app := &App{
		cfg:             cfg,
		log:             log,
		metrics:         m,
		signer:          auth.NewSigner(cfg.AuthSecret),
		region:          region,
		quota:           q,
		configPool:      configPool,
		cellPool:        cellPool,
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
		remindersRepo:   reminders.NewRepo(),
		blocksRepo:      blocks.NewRepo(cellPool),
		mutesRepo:       mutes.NewRepo(cellPool),
		bookmarksRepo:   bookmarks.NewRepo(cellPool),
		membershipCache: realtime.NewMembershipCache(redisClient, m),
		blocksCache:     realtime.NewBlocksCache(redisClient, m),
		dodo:            dodoClient,

		translationClient:    translationClient,
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

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: app.dataRoutes()}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	log.Info("api listening", "addr", cfg.HTTPAddr, "region", cfg.Region)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("http server", "error", err)
		os.Exit(1)
	}
}
