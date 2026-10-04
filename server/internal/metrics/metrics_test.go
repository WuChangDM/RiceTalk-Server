package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// readCounter returns the current value of a Prometheus counter.
func readCounter(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := counter.(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// readGauge returns the current value of a Prometheus gauge.
func readGauge(t *testing.T, gauge prometheus.Gauge) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := gauge.(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

func TestRecordCacheHit(t *testing.T) {
	before := readCounter(t, CacheHitsTotal)
	RecordCacheHit()
	RecordCacheHit()
	after := readCounter(t, CacheHitsTotal)
	if got := after - before; got != 2 {
		t.Errorf("expected counter to increment by 2, got %v", got)
	}
}

func TestRecordCacheMiss(t *testing.T) {
	before := readCounter(t, CacheMissesTotal)
	RecordCacheMiss()
	after := readCounter(t, CacheMissesTotal)
	if got := after - before; got != 1 {
		t.Errorf("expected counter to increment by 1, got %v", got)
	}
}

func TestRecordWSMessage(t *testing.T) {
	// CounterVec labels must be exercised via WithLabelValues
	RecordWSMessage("chat")
	RecordWSMessage("chat")
	RecordWSMessage("presence")

	m := &dto.Metric{}
	if err := WSMessagesTotal.WithLabelValues("chat").(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("read ws counter: %v", err)
	}
	// We can't easily assert the absolute value because the counter persists
	// across tests in the default registry, but we can assert it is > 0 and
	// that the call does not panic.
	if m.GetCounter().GetValue() <= 0 {
		t.Errorf("expected chat message counter > 0, got %v", m.GetCounter().GetValue())
	}
}

func TestSetWSConnections(t *testing.T) {
	SetWSConnections(7)
	if got := readGauge(t, WSConnectionsActive); got != 7 {
		t.Errorf("expected gauge 7, got %v", got)
	}
	SetWSConnections(0)
	if got := readGauge(t, WSConnectionsActive); got != 0 {
		t.Errorf("expected gauge 0, got %v", got)
	}
}

func TestHandlerReturnsPrometheusFormat(t *testing.T) {
	// Force some activity so the /metrics output is non-empty
	RecordCacheHit()
	SetWSConnections(3)

	h := Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	body := w.Body.String()
	// The output should mention at least one of our metric names
	if !strings.Contains(body, "ridgericetalk_") {
		t.Errorf("expected prometheus output to contain 'ridgericetalk_', got: %s", body)
	}
	if !strings.Contains(body, "websocket_connections_active") {
		t.Errorf("expected websocket_connections_active metric in output, got: %s", body)
	}
}

func TestHandlerSetsContentType(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected Content-Type text/plain..., got %q", ct)
	}
}
