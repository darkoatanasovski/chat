// Package config centralizes environment-driven configuration for every
// service (cmd/api, cmd/gateway, cmd/worker). Each service reads only the
// fields it needs; unused fields are simply left empty.
package config

import (
	"fmt"
	"os"
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

	AuthSecret string

	ShardsConfigPath string
	TiersConfigPath  string

	// api: internal base URLs for forwarding writes to a channel's home region.
	PeerAPIURL map[string]string

	// api: browser origins allowed to call this API directly (e.g. the demo app).
	CORSAllowedOrigins []string

	// gateway: Kafka consumer group name (one per region).
	KafkaConsumerGroup string

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

	// :3000 is the demo/ chat test harness, :3001 is the dashboard/ app.
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
