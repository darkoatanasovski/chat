// cmd/ogservice is a small, standalone HTTP service whose only job is:
// given a URL, fetch its OpenGraph metadata (title, description, image,
// site name) and cache the result for OG_CACHE_TTL (default 1h). It backs
// the "url_enrichment" channel capability — cmd/api calls it over HTTP
// (see cmd/api/link_preview.go) instead of scraping pages itself, so a
// slow or misbehaving third-party host only ever affects this one service,
// and it can be scaled, deployed, and rate-limited independently of the
// API's own request path (see deploy/docker-compose.yml's og-service).
//
// Deliberately has no dependency on Postgres, Kafka, or any of the other
// platform infrastructure the rest of cmd/* needs — its cache is in-memory
// (internal/opengraph.Cache) and its only external calls are the outbound
// fetches it's asked to make. That also means it does NOT use
// internal/platform/config.Load (which requires AUTH_SECRET and other
// fields this service has no use for); its own tiny env-driven config is
// defined below.
package ogservice

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/darkoatanasovski/chat/internal/opengraph"
	"github.com/darkoatanasovski/chat/internal/platform/debug"
	"github.com/darkoatanasovski/chat/internal/platform/health"
	"github.com/darkoatanasovski/chat/internal/platform/logging"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
)

type config struct {
	httpAddr      string
	metricsAddr   string
	cacheTTL      time.Duration
	fetchTimeout  time.Duration
	sweepInterval time.Duration
	maxFetchBytes int64
}

func loadConfig() config {
	return config{
		httpAddr:      getenvDefault("HTTP_ADDR", ":8080"),
		metricsAddr:   getenvDefault("METRICS_ADDR", ":9100"),
		cacheTTL:      getenvDuration("OG_CACHE_TTL", time.Hour),
		fetchTimeout:  getenvDuration("OG_FETCH_TIMEOUT", 5*time.Second),
		sweepInterval: getenvDuration("OG_SWEEP_INTERVAL", 10*time.Minute),
		maxFetchBytes: getenvInt64("OG_MAX_FETCH_BYTES", 64*1024),
	}
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func Run() {
	cfg := loadConfig()
	log := logging.New("og-service", os.Getenv("REGION"))
	m := metrics.New("chat_og_service")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cache := opengraph.NewCache(cfg.cacheTTL)
	go cache.Run(ctx, cfg.sweepInterval)

	svc := &service{cfg: cfg, cache: cache, log: log, metrics: m}

	// /metrics and pprof (debug.Mount) are served on their own internal-only
	// port, not the public og-service port — mirrors cmd/api, cmd/gateway,
	// and cmd/worker, all of which keep the profiler and metrics endpoint
	// off the port that's actually exposed/reachable for real traffic.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	debug.Mount(metricsMux)
	go func() {
		log.Info("metrics listening", "addr", cfg.metricsAddr)
		if err := http.ListenAndServe(cfg.metricsAddr, metricsMux); err != nil {
			log.Error("metrics server", "error", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/og", svc.handleOG)
	mux.Handle("/healthz", health.Handler(nil))

	srv := &http.Server{Addr: cfg.httpAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	log.Info("og-service listening", "addr", cfg.httpAddr, "cache_ttl", cfg.cacheTTL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "error", err)
		os.Exit(1)
	}
}

type service struct {
	cfg     config
	cache   *opengraph.Cache
	log     *slog.Logger
	metrics *metrics.Metrics
}

// handleOG serves GET /og?url=<url>: a cache hit is served straight from
// memory; a miss fetches, caches for cfg.cacheTTL (including a
// legitimately-empty result — see opengraph.Fetch's doc comment), and
// serves the fresh result. Never blocks waiting on anything other than the
// one outbound fetch itself.
func (s *service) handleOG(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	route := "/og"

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		s.observe(route, r.Method, http.StatusMethodNotAllowed, start)
		return
	}

	raw := r.URL.Query().Get("url")
	target, err := validateURL(raw)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		s.observe(route, r.Method, http.StatusBadRequest, start)
		return
	}

	if data, ok := s.cache.Get(target); ok {
		s.metrics.OGRequestsTotal.WithLabelValues("cache_hit").Inc()
		w.Header().Set("X-Cache", "HIT")
		s.writeJSON(w, http.StatusOK, data)
		s.observe(route, r.Method, http.StatusOK, start)
		return
	}

	data, err := opengraph.Fetch(r.Context(), target, opengraph.Options{
		Timeout:  s.cfg.fetchTimeout,
		MaxBytes: s.cfg.maxFetchBytes,
	})
	if err != nil {
		s.metrics.OGRequestsTotal.WithLabelValues("fetch_error").Inc()
		s.log.Warn("og fetch failed", "url", target, "error", err)
		s.writeError(w, http.StatusBadGateway, "fetch failed")
		s.observe(route, r.Method, http.StatusBadGateway, start)
		return
	}

	s.cache.Set(target, *data)
	s.metrics.OGRequestsTotal.WithLabelValues("fetched").Inc()
	w.Header().Set("X-Cache", "MISS")
	s.writeJSON(w, http.StatusOK, data)
	s.observe(route, r.Method, http.StatusOK, start)
}

// validateURL requires an absolute http(s) URL — anything else (empty,
// relative, a different scheme like file:// or javascript:) is rejected
// before it ever reaches opengraph.Fetch, which otherwise would happily
// hand it to net/http and let that fail less clearly.
func validateURL(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("missing required \"url\" query parameter")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("url must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("url must be absolute")
	}
	return raw, nil
}

func (s *service) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *service) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]string{"error": message})
}

func (s *service) observe(route, method string, status int, start time.Time) {
	s.metrics.HTTPRequestsTotal.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	s.metrics.HTTPRequestDuration.WithLabelValues(route, method).Observe(time.Since(start).Seconds())
}
