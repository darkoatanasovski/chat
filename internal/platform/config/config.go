// Package config centralizes environment-driven configuration for every
// service (cmd/api, cmd/gateway, cmd/worker). Each service reads only the
// fields it needs; unused fields are simply left empty.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Region      string // eu | us | asia
	HTTPAddr    string
	MetricsAddr string

	ControlDSN string
	ShardADSN  string
	ShardBDSN  string

	ValkeyAddr   string
	KafkaBrokers []string

	// Sentinel-managed Valkey/Redis (INSTRUCTIONS.md's Redis-HA follow-up):
	// when ValkeySentinelAddrs is non-empty, storage/redis.ConnectSentinel
	// is used instead of Connect(ValkeyAddr) — the client discovers the
	// current primary through Sentinel and follows it across failover
	// rather than pinning to one address. ValkeyMasterName must match the
	// name in every Sentinel's own "sentinel monitor <name> ..." config.
	ValkeySentinelAddrs []string
	ValkeyMasterName    string

	AuthSecret string

	ShardsConfigPath string
	TiersConfigPath  string

	// api: internal base URLs for forwarding writes to a channel's home region.
	PeerAPIURL map[string]string

	// api: browser origins allowed to call this API directly (e.g. the demo app).
	CORSAllowedOrigins []string

	// gateway: Kafka consumer group name (one per region).
	KafkaConsumerGroup string

	// gateway: number of per-channel shards Fanout spreads concurrent
	// delivery across (see internal/realtime/channel_shard_pool.go). <= 0
	// (including unset) falls back to Fanout's own default.
	FanoutShards int

	// worker: which physical shard this instance publishes the outbox for.
	ShardID  string
	ShardDSN string
}

func Load() (Config, error) {
	c := Config{
		Region:             os.Getenv("REGION"),
		HTTPAddr:           getenvDefault("HTTP_ADDR", ":8080"),
		MetricsAddr:        getenvDefault("METRICS_ADDR", ":9100"),
		ControlDSN:         os.Getenv("CONTROL_DSN"),
		ShardADSN:          os.Getenv("SHARD_A_DSN"),
		ShardBDSN:          os.Getenv("SHARD_B_DSN"),
		ValkeyAddr:         os.Getenv("VALKEY_ADDR"),
		ValkeyMasterName:   getenvDefault("VALKEY_MASTER_NAME", "mymaster"),
		AuthSecret:         os.Getenv("AUTH_SECRET"),
		ShardsConfigPath:   os.Getenv("SHARDS_CONFIG"),
		TiersConfigPath:    getenvDefault("TIERS_CONFIG", "/etc/chat/tiers.yaml"),
		KafkaConsumerGroup: os.Getenv("KAFKA_CONSUMER_GROUP"),
		ShardID:            os.Getenv("SHARD_ID"),
		ShardDSN:           os.Getenv("SHARD_DSN"),
	}

	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		c.KafkaBrokers = strings.Split(brokers, ",")
	}

	if sentinels := os.Getenv("VALKEY_SENTINEL_ADDRS"); sentinels != "" {
		c.ValkeySentinelAddrs = strings.Split(sentinels, ",")
	}

	if n, err := strconv.Atoi(os.Getenv("FANOUT_SHARDS")); err == nil {
		c.FanoutShards = n
	}

	// :3000 is the demo/ chat test harness, :3001 is the console/ app.
	origins := getenvDefault("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:3001")
	c.CORSAllowedOrigins = strings.Split(origins, ",")

	c.PeerAPIURL = map[string]string{
		"eu":   os.Getenv("PEER_API_EU_URL"),
		"us":   os.Getenv("PEER_API_US_URL"),
		"asia": os.Getenv("PEER_API_ASIA_URL"),
	}

	if c.AuthSecret == "" {
		return c, fmt.Errorf("config: AUTH_SECRET is required")
	}

	return c, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
