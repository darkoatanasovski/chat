// cmd/router is the edge router (docs/adr/0006-cell-based-tenant-routing.md).
//
// It reads the apikey from each request — the ?api_key= query parameter, or a
// verified bearer token's api_key/app_id claim — resolves apikey -> {region,
// shard} from the global config DB (cache-first, internal/appconfig), looks up
// that cell's api/ws endpoints in infra/topology.yaml (internal/topology), and
// reverse-proxies the request there. HTTP requests go to the cell's api
// endpoint; WebSocket upgrades go to its ws endpoint (net/http/httputil's
// ReverseProxy carries the Upgrade through).
//
// The router holds no tenant state and makes one authoritative decision —
// which cell — that every downstream service can then take for granted. This
// is the single routing hop that replaces the old per-channel home-region
// forwarding and any-api-reaches-any-shard model.
package router

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/darkoatanasovski/chat/internal/appconfig"
	"github.com/darkoatanasovski/chat/internal/platform/auth"
	"github.com/darkoatanasovski/chat/internal/platform/config"
	"github.com/darkoatanasovski/chat/internal/platform/debug"
	"github.com/darkoatanasovski/chat/internal/platform/logging"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
	pgstorage "github.com/darkoatanasovski/chat/internal/storage/postgres"
	redisstorage "github.com/darkoatanasovski/chat/internal/storage/redis"
	"github.com/darkoatanasovski/chat/internal/topology"
)

// Run is the router role entrypoint (invoked as `chat router`).
func Run() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logging.New("router", cfg.Region)
	m := metrics.New("chat_router")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.ConfigDSN == "" {
		log.Error("CONFIG_DSN is required for the router")
		os.Exit(1)
	}
	configPool, err := pgstorage.Connect(ctx, cfg.ConfigDSN)
	if err != nil {
		log.Error("connect config db", "error", err)
		os.Exit(1)
	}

	redisClient, err := redisstorage.ConnectFromEnv(ctx, cfg.ValkeyAddr, cfg.ValkeySentinelAddrs, cfg.ValkeyMasterName)
	if err != nil {
		// The router degrades to uncached config lookups rather than failing
		// to start — a cache outage must not take routing down.
		log.Warn("connect redis (router will run uncached)", "error", err)
		redisClient = nil
	}

	topo, err := topology.Load(cfg.TopologyPath)
	if err != nil {
		log.Error("load topology", "error", err)
		os.Exit(1)
	}
	idx := topology.NewIndex(topo)

	rt := &edgeRouter{
		log:        log,
		metrics:    m,
		resolver:   appconfig.New(configPool, redisClient),
		signer:     auth.NewSigner(cfg.AuthSecret),
		topo:       idx,
		controlURL: cfg.ControlURL,
		// region is non-empty when this router serves a single regional
		// hostname (us-east-1.api.chat.io); empty when it's the global edge.
		region: cfg.Region,
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

	srv := &http.Server{Addr: cfg.RouterAddr, Handler: rt}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	log.Info("router listening", "addr", cfg.RouterAddr, "region_scope", orGlobal(cfg.Region))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("http server", "error", err)
		os.Exit(1)
	}
}

type edgeRouter struct {
	log        *slog.Logger
	metrics    *metrics.Metrics
	resolver   *appconfig.Resolver
	signer     *auth.Signer
	topo       *topology.Index
	controlURL string
	region     string
}

// controlPathPrefixes are the URL prefixes the global control plane owns
// (org/dashboard/billing). Everything else is data plane, routed to a cell by
// apikey. See docs/adr/0006-cell-based-tenant-routing.md.
var controlPathPrefixes = []string{"/organizations", "/apps", "/dashboard", "/dodo"}

func isControlPath(path string) bool {
	for _, p := range controlPathPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func (rt *edgeRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Health check bypasses routing so a load balancer can probe the router
	// itself without an apikey.
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	// Control-plane paths (org/dashboard/billing) go to the single global
	// control service, not to a cell — they aren't apikey-scoped.
	if isControlPath(r.URL.Path) {
		if rt.controlURL == "" {
			http.Error(w, `{"error":"control plane not configured"}`, http.StatusNotFound)
			return
		}
		rt.proxyTo(w, r, rt.controlURL)
		return
	}

	cfg, err := rt.resolveApp(r.Context(), r)
	if err != nil {
		rt.log.Warn("route resolution failed", "path", r.URL.Path, "error", err)
		http.Error(w, `{"error":"could not resolve app for request; provide a valid api_key or bearer token"}`, http.StatusUnauthorized)
		return
	}
	if !cfg.Placement.Provisioned() {
		http.Error(w, `{"error":"app is not yet assigned to a cell"}`, http.StatusServiceUnavailable)
		return
	}

	// A regional router only serves apps homed in its own region; anything
	// else is a client that hit the wrong regional hostname. Tell it to use
	// the global endpoint rather than silently proxying cross-region.
	if rt.region != "" && cfg.Placement.Region != rt.region {
		http.Error(w, `{"error":"app is homed in another region; use the global endpoint api.chat.io"}`, http.StatusMisdirectedRequest)
		return
	}

	cell, ok := rt.topo.Cell(cfg.Placement.Region, cfg.Placement.Shard)
	if !ok {
		rt.log.Error("placement points at unknown cell", "region", cfg.Placement.Region, "shard", cfg.Placement.Shard, "app_id", cfg.AppID)
		http.Error(w, `{"error":"cell for this app is not in the router's topology"}`, http.StatusBadGateway)
		return
	}

	target := cell.Endpoints.API
	if isWebSocketUpgrade(r) {
		target = cell.Endpoints.WS
	}
	rt.proxyTo(w, r, target)
}

// resolveApp extracts the apikey and resolves the app's config. Order: the
// explicit ?api_key= query parameter wins (it's how a browser WebSocket, which
// can't set headers, authenticates); otherwise a bearer token's api_key (app
// token) or app_id (user token) claim.
func (rt *edgeRouter) resolveApp(ctx context.Context, r *http.Request) (appconfig.AppConfig, error) {
	if key := r.URL.Query().Get("api_key"); key != "" {
		return rt.resolver.ByAPIKey(ctx, key)
	}
	if tok := bearerToken(r); tok != "" {
		claims, err := rt.signer.Verify(tok)
		if err == nil {
			if claims.APIKey != "" {
				return rt.resolver.ByAPIKey(ctx, claims.APIKey)
			}
			if claims.AppID != 0 {
				return rt.resolver.ByAppID(ctx, claims.AppID)
			}
		}
	}
	return appconfig.AppConfig{}, appconfig.ErrNotFound
}

func (rt *edgeRouter) proxyTo(w http.ResponseWriter, r *http.Request, target string) {
	u, err := url.Parse(target)
	if err != nil {
		rt.log.Error("bad cell endpoint", "target", target, "error", err)
		http.Error(w, `{"error":"internal routing error"}`, http.StatusBadGateway)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		rt.log.Error("upstream error", "target", target, "error", err)
		http.Error(w, `{"error":"upstream cell unavailable"}`, http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
		return tok
	}
	return ""
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func orGlobal(region string) string {
	if region == "" {
		return "global"
	}
	return region
}
