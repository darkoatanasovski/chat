// cmd/gateway is the WebSocket edge (INSTRUCTIONS.md §17): it authenticates
// connections, tracks them locally, and shares one Kafka consumer group
// with every other gateway instance across every region, so any number of
// them can run concurrently and Kafka splits the message.created stream
// across whichever are alive. Each delivers directly to its own local
// connections and routes everyone else's delivery to whichever instance
// actually holds them, over Redis Pub/Sub (see internal/realtime/pubsub.go
// and fanout.go). It carries no business logic beyond that.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/darkoatanasovski/chat/internal/events"
	"github.com/darkoatanasovski/chat/internal/membership"
	"github.com/darkoatanasovski/chat/internal/platform/auth"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/debug"
	"github.com/darkoatanasovski/chat/internal/platform/health"
	"github.com/darkoatanasovski/chat/internal/platform/logging"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	"github.com/darkoatanasovski/chat/internal/realtime"
	kafkastorage "github.com/darkoatanasovski/chat/internal/storage/kafka"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
	redisstorage "github.com/darkoatanasovski/chat/internal/storage/redis"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logging.New("gateway", cfg.Region)
	m := metrics.New("chat_gateway")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	redisClient, err := redisstorage.ConnectFromEnv(ctx, cfg.ValkeyAddr, cfg.ValkeySentinelAddrs, cfg.ValkeyMasterName)
	if err != nil {
		log.Error("connect redis", "error", err)
		os.Exit(1)
	}

	controlPool, err := pgstorage.Connect(ctx, cfg.ControlDSN)
	if err != nil {
		log.Error("connect control db", "error", err)
		os.Exit(1)
	}

	signer := auth.NewSigner(cfg.AuthSecret)
	hub := realtime.NewHub(func(c *realtime.Connection, reason string) {
		log.Info("connection evicted", "reason", reason, "user_id", c.UserID)
	})
	registry := realtime.NewRegistry(redisClient, m)
	cache := realtime.NewMembershipCache(redisClient, m)

	// Unique per instance, not just per region: once more than one gateway
	// process can share a region (see docker-compose's gateway-eu-2), a
	// region-only id would collide — two instances would claim the same
	// Pub/Sub channel and Registry gateway_id. os.Hostname() is the
	// container id under Docker, already unique with no extra config.
	hostname, err := os.Hostname()
	if err != nil {
		log.Error("read hostname", "error", err)
		os.Exit(1)
	}
	gatewayID := cfg.Region + "-" + hostname

	// Namespaced by consumer group, not gatewayID — see dedup.go's doc
	// comment for why that distinction matters now that every gateway
	// shares one group.
	dedup := realtime.NewDedup(redisClient, cfg.KafkaConsumerGroup, m)
	membershipRepo := membership.NewRepo(controlPool)
	publisher := realtime.NewPublisher(redisClient, m)

	delivery := realtime.NewDelivery(hub, cache, membershipRepo, registry, publisher, log)

	consumerTopics := []string{events.TopicMessageCreated, events.TopicReactionUpdated, events.TopicReadUpdated}
	consumer := kafkastorage.NewConsumer(cfg.KafkaBrokers, consumerTopics, cfg.KafkaConsumerGroup)
	fanout := realtime.NewFanout(consumer, delivery, dedup, m, log)
	fanout.SetShards(cfg.FanoutShards)

	go func() {
		if err := fanout.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("fanout stopped", "error", err)
		}
	}()

	lagPoller := kafkastorage.NewLagPoller(cfg.KafkaBrokers, cfg.KafkaConsumerGroup, consumerTopics, log,
		func(topic string, partition int, lag int64) {
			m.KafkaConsumerLagGap.WithLabelValues(topic, strconv.Itoa(partition)).Set(float64(lag))
		})
	go lagPoller.Run(ctx, 15*time.Second)

	subscriber := realtime.NewSubscriber(redisClient, gatewayID, hub, log)
	go func() {
		if err := subscriber.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("pubsub subscriber stopped", "error", err)
		}
	}()

	connectHandler := realtime.NewConnectHandler(signer, hub, registry, delivery, gatewayID, m, log)

	mux := http.NewServeMux()
	mux.Handle("/connect", connectHandler)
	mux.Handle("/healthz", health.Handler(map[string]health.Checker{
		"redis":   func(ctx context.Context) error { return redisClient.Ping(ctx).Err() },
		"control": func(ctx context.Context) error { return controlPool.Ping(ctx) },
	}))

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	debug.Mount(metricsMux)

	go func() {
		log.Info("metrics listening", "addr", cfg.MetricsAddr)
		if err := http.ListenAndServe(cfg.MetricsAddr, metricsMux); err != nil {
			log.Error("metrics server", "error", err)
		}
	}()

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = consumer.Close()
		_ = srv.Close()
	}()

	log.Info("gateway listening", "addr", cfg.HTTPAddr, "region", cfg.Region, "active_connections", hub.ActiveConnections())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("http server", "error", err)
		os.Exit(1)
	}
}
