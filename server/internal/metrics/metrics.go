package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTPRequestTotal counts HTTP requests by method, path, and status
	HTTPRequestTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ridgericetalk",
		Name:      "http_requests_total",
		Help:      "Total number of HTTP requests",
	}, []string{"method", "path", "status"})

	// HTTPRequestDuration records HTTP request duration in seconds
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "ridgericetalk",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request duration in seconds",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "path"})

	// WSConnectionsActive tracks current WebSocket connections
	WSConnectionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "ridgericetalk",
		Name:      "websocket_connections_active",
		Help:      "Number of active WebSocket connections",
	})

	// WSMessagesTotal counts WebSocket messages by type
	WSMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ridgericetalk",
		Name:      "websocket_messages_total",
		Help:      "Total number of WebSocket messages",
	}, []string{"type"})

	// CacheHitsTotal counts cache hits
	CacheHitsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "ridgericetalk",
		Name:      "cache_hits_total",
		Help:      "Total number of cache hits",
	})

	// CacheMissesTotal counts cache misses
	CacheMissesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "ridgericetalk",
		Name:      "cache_misses_total",
		Help:      "Total number of cache misses",
	})
)

// RecordCacheHit increments cache hit counter
func RecordCacheHit() {
	CacheHitsTotal.Inc()
}

// RecordCacheMiss increments cache miss counter
func RecordCacheMiss() {
	CacheMissesTotal.Inc()
}

// RecordWSMessage increments WebSocket message counter for given type
func RecordWSMessage(msgType string) {
	WSMessagesTotal.WithLabelValues(msgType).Inc()
}

// SetWSConnections sets the active WebSocket connection gauge
func SetWSConnections(n float64) {
	WSConnectionsActive.Set(n)
}

// Handler returns the Prometheus HTTP handler for /metrics
func Handler() http.Handler {
	return promhttp.Handler()
}
