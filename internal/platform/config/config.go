// Package config centralizes environment-driven configuration for every
// service (cmd/api, cmd/gateway, cmd/worker). Each service reads only the
// fields it needs; unused fields are simply left empty.
package config

import (
	"encoding/base64"
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

	// Cell-based routing (docs/adr/0006-cell-based-tenant-routing.md).
	// ConfigDSN is the global config database (migrations/config); every
	// role and the router read app placement/credentials/settings from it
	// via internal/appconfig. CellDSN is THIS cell's own Postgres
	// (migrations/cell) holding all tenant data for the apps pinned here —
	// it replaces the ShardADSN/ShardBDSN pair and the any-api-reaches-any-
	// shard model. TopologyPath points at infra/topology.yaml (regions ->
	// cells -> endpoints), read by the router.
	ConfigDSN    string
	CellDSN      string
	TopologyPath string
	// RouterAddr is the router role's public listen address.
	RouterAddr string
	// ControlURL is where the router forwards control-plane paths
	// (/organizations, /apps, /dashboard, /dodo) — the global control-plane
	// service (chat control). Empty means "no control plane wired": the
	// router then 404s those paths instead of proxying.
	ControlURL string

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

	// TurnstileSecret enables Cloudflare Turnstile verification on dashboard
	// signup (control plane). Empty = disabled (dev/tests/unprotected).
	TurnstileSecret string

	// InternalAuthKey gates the control plane's /internal/* endpoints (the
	// edge router's read-through placement lookup). Empty = open (dev).
	InternalAuthKey string

	// AppSecretEncryptionKey decrypts/encrypts app_credentials.secret_encrypted
	// (internal/platform/secretbox) so a dashboard user can reveal a
	// credential's secret again after the one-time creation response is
	// gone — see internal/apps.CredentialRepo.Reveal. Loaded from
	// APP_SECRET_ENCRYPTION_KEY (base64, must decode to exactly
	// secretbox.KeySize bytes), a separate key from AuthSecret since the
	// two protect different things (request signing vs. secrets at rest).
	//
	// api only: like DodoAPIKey below, empty is a valid value here — Load
	// doesn't require it, because cmd/gateway and cmd/worker also call
	// Load and never touch app credentials. cmd/api/main.go is what
	// requires and validates it (fails fast at startup, not lazily on the
	// first credential request) since it's the only caller that needs it.
	AppSecretEncryptionKey []byte

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

	// api: Dodo Payments billing (self-serve plan upgrades from the
	// console) — see cmd/api/handlers_billing.go. All empty/false is a
	// valid "billing not configured" state; handlers report that as an
	// error rather than the service failing to start, since most
	// dev/test environments have no need for it.
	DodoAPIKey     string
	DodoWebhookKey string
	DodoLiveMode   bool
	// DodoProductIDs maps an upgradable tier (PRO, BUSINESS) to the Dodo
	// product id a checkout for that tier should sell.
	DodoProductIDs map[string]string
	// ConsoleBaseURL builds the return_url Dodo redirects the customer to
	// once checkout completes.
	ConsoleBaseURL string

	// api: base URL of the standalone og-service (cmd/ogservice) —
	// enrichLinkPreview (cmd/api/link_preview.go) calls
	// "<OGServiceURL>/og?url=..." for the "url_enrichment" capability
	// instead of scraping pages itself. Only cmd/api reads this; gateway
	// and worker load it too (Load is shared) but simply never use it.
	OGServiceURL string

	// api: Azure (Microsoft) Translator Text API — backs the
	// "translations" channel capability (internal/translations,
	// cmd/api/handlers_translate.go). Chosen over Google Cloud Translation
	// and DeepL for being both the cheapest ($10/1M chars vs. $20/1M and
	// $25/1M respectively, plus a permanent 2M-chars/month free tier) and
	// the fastest (sub-100ms median) of the major providers as of this
	// writing. Empty AzureTranslatorKey is a valid "translation not
	// configured" state, same as DodoAPIKey above — the handler reports
	// that as a 503 rather than the service failing to start.
	AzureTranslatorKey string
	// AzureTranslatorRegion is the Azure resource's region (e.g.
	// "westeurope") — required by the Translator Text API alongside the
	// key for any multi-service or regional Translator resource.
	AzureTranslatorRegion string
	// AzureTranslatorEndpoint defaults to Azure's global Translator
	// endpoint; only overridden in tests or for a sovereign-cloud
	// deployment.
	AzureTranslatorEndpoint string
}

func Load() (Config, error) {
	c := Config{
		Region:             os.Getenv("REGION"),
		HTTPAddr:           getenvDefault("HTTP_ADDR", ":8080"),
		MetricsAddr:        getenvDefault("METRICS_ADDR", ":9100"),
		ControlDSN:         os.Getenv("CONTROL_DSN"),
		ShardADSN:          os.Getenv("SHARD_A_DSN"),
		ShardBDSN:          os.Getenv("SHARD_B_DSN"),
		ConfigDSN:          os.Getenv("CONFIG_DSN"),
		CellDSN:            os.Getenv("CELL_DSN"),
		TopologyPath:       getenvDefault("TOPOLOGY_CONFIG", "/etc/chat/topology.yaml"),
		RouterAddr:         getenvDefault("ROUTER_ADDR", ":8080"),
		ControlURL:         os.Getenv("CONTROL_URL"),
		ValkeyAddr:         os.Getenv("VALKEY_ADDR"),
		ValkeyMasterName:   getenvDefault("VALKEY_MASTER_NAME", "mymaster"),
		AuthSecret:         os.Getenv("AUTH_SECRET"),
		TurnstileSecret:    os.Getenv("TURNSTILE_SECRET"),
		InternalAuthKey:    os.Getenv("INTERNAL_AUTH_KEY"),
		ShardsConfigPath:   os.Getenv("SHARDS_CONFIG"),
		TiersConfigPath:    getenvDefault("TIERS_CONFIG", "/etc/chat/tiers.yaml"),
		KafkaConsumerGroup: os.Getenv("KAFKA_CONSUMER_GROUP"),
		ShardID:            os.Getenv("SHARD_ID"),
		ShardDSN:           os.Getenv("SHARD_DSN"),
		DodoAPIKey:         os.Getenv("DODO_PAYMENTS_API_KEY"),
		DodoWebhookKey:     os.Getenv("DODO_PAYMENTS_WEBHOOK_KEY"),
		DodoLiveMode:       os.Getenv("DODO_PAYMENTS_LIVE_MODE") == "true",
		ConsoleBaseURL:     getenvDefault("CONSOLE_BASE_URL", "http://localhost:3001"),
		OGServiceURL:       getenvDefault("OG_SERVICE_URL", "http://localhost:8095"),

		AzureTranslatorKey:      os.Getenv("AZURE_TRANSLATOR_KEY"),
		AzureTranslatorRegion:   os.Getenv("AZURE_TRANSLATOR_REGION"),
		AzureTranslatorEndpoint: getenvDefault("AZURE_TRANSLATOR_ENDPOINT", "https://api.cognitive.microsofttranslator.com"),
	}

	c.DodoProductIDs = map[string]string{
		"PRO":      os.Getenv("DODO_PRODUCT_ID_PRO"),
		"BUSINESS": os.Getenv("DODO_PRODUCT_ID_BUSINESS"),
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

	if keyB64 := os.Getenv("APP_SECRET_ENCRYPTION_KEY"); keyB64 != "" {
		key, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return c, fmt.Errorf("config: APP_SECRET_ENCRYPTION_KEY must be base64: %w", err)
		}
		c.AppSecretEncryptionKey = key
	}

	return c, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
