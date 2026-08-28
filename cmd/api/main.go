package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/darkoatanasovski/chat/internal/apps"
	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/membership"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/organizations"
	"github.com/darkoatanasovski/chat/internal/orgusers"
	"github.com/darkoatanasovski/chat/internal/platform/auth"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/debug"
	"github.com/darkoatanasovski/chat/internal/platform/logging"
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

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logging.New("api", cfg.Region)
	m := metrics.New("chat_api")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	controlPool, err := pgstorage.Connect(ctx, cfg.ControlDSN)
	if err != nil {
		log.Error("connect control db", "error", err)
		os.Exit(1)
	}
	shardADSN, err := pgstorage.Connect(ctx, cfg.ShardADSN)
	if err != nil {
		log.Error("connect shard-a", "error", err)
		os.Exit(1)
	}
	shardBDSN, err := pgstorage.Connect(ctx, cfg.ShardBDSN)
	if err != nil {
		log.Error("connect shard-b", "error", err)
		os.Exit(1)
	}
	shardPools := pgstorage.ShardPools{"shard-a": shardADSN, "shard-b": shardBDSN}

	redisClient, err := redisstorage.Connect(ctx, cfg.ValkeyAddr)
	if err != nil {
		log.Error("connect redis", "error", err)
		os.Exit(1)
	}

	shardsCfg, err := routing.LoadShardsConfig(cfg.ShardsConfigPath)
	if err != nil {
		log.Error("load shards config", "error", err)
		os.Exit(1)
	}
	router := routing.NewRouter(shardsCfg)

	tiers, err := quota.LoadTiers(cfg.TiersConfigPath)
	if err != nil {
		log.Error("load tiers config", "error", err)
		os.Exit(1)
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

	app := &App{
		cfg:             cfg,
		log:             log,
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
		membershipCache: realtime.NewMembershipCache(redisClient, m),
		peerClient:      newPeerClient(),
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

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: app.routes()}
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
