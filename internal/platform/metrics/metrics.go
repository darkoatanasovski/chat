// Package metrics defines the Prometheus metrics tracked across services, per
// the minimum list in INSTRUCTIONS.md §37. Every service registers the subset
// it produces and serves them on /metrics (see Handler).
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	WebSocketConnectionsActive prometheus.Gauge
	WebSocketDisconnectsTotal  *prometheus.CounterVec

	MessagesSentTotal      *prometheus.CounterVec
	MessagePersistLatency  prometheus.Histogram
	MessageDeliveryLatency prometheus.Histogram

	KafkaProducerLatency prometheus.Histogram
	KafkaConsumerLagGap  *prometheus.GaugeVec

	PostgresQueryLatency *prometheus.HistogramVec
	RedisLatency         *prometheus.HistogramVec
	RedisErrorsTotal     *prometheus.CounterVec

	RateLimitRejectionsTotal *prometheus.CounterVec
	QuotaRejectionsTotal     *prometheus.CounterVec

	CrossRegionLatency *prometheus.HistogramVec
}

func New(namespace string) *Metrics {
	return &Metrics{
		HTTPRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "http_requests_total",
		}, []string{"route", "method", "status"}),
		HTTPRequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "http_request_duration_seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method"}),

		WebSocketConnectionsActive: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "websocket_connections_active",
		}),
		WebSocketDisconnectsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "websocket_disconnects_total",
		}, []string{"reason"}),

		MessagesSentTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "messages_sent_total",
		}, []string{"region"}),
		MessagePersistLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "message_persist_latency_seconds",
			Buckets: prometheus.DefBuckets,
		}),
		MessageDeliveryLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "message_delivery_latency_seconds",
			Buckets: prometheus.DefBuckets,
		}),

		KafkaProducerLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "kafka_producer_latency_seconds",
			Buckets: prometheus.DefBuckets,
		}),
		KafkaConsumerLagGap: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "kafka_consumer_lag",
		}, []string{"topic", "partition"}),

		PostgresQueryLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "postgres_query_latency_seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"query"}),
		RedisLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "redis_latency_seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"op"}),
		RedisErrorsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "redis_errors_total",
		}, []string{"op"}),

		RateLimitRejectionsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "rate_limit_rejections_total",
		}, []string{"capability"}),
		QuotaRejectionsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "quota_rejections_total",
		}, []string{"capability"}),

		CrossRegionLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "cross_region_latency_seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"from_region", "to_region"}),
	}
}

func Handler() http.Handler {
	return promhttp.Handler()
}

// TimePostgres runs fn and records its duration under the given query label.
// Callers pass a short, low-cardinality query name (e.g. "insert_message"),
// never interpolated SQL or IDs. A nil *Metrics (many tests construct
// storage types without one) just runs fn — never a required dependency.
func (m *Metrics) TimePostgres(query string, fn func() error) error {
	if m == nil {
		return fn()
	}
	start := time.Now()
	err := fn()
	m.PostgresQueryLatency.WithLabelValues(query).Observe(time.Since(start).Seconds())
	return err
}

// TimeRedis runs fn and records latency/error metrics under the given op
// label. A nil *Metrics just runs fn — see TimePostgres.
func (m *Metrics) TimeRedis(op string, fn func() error) error {
	if m == nil {
		return fn()
	}
	start := time.Now()
	err := fn()
	m.RedisLatency.WithLabelValues(op).Observe(time.Since(start).Seconds())
	if err != nil {
		m.RedisErrorsTotal.WithLabelValues(op).Inc()
	}
	return err
}
