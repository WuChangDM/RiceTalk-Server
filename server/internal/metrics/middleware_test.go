package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestGinMiddlewareRecordsRequest(t *testing.T) {
	r := gin.New()
	r.Use(GinMiddleware())
	r.GET("/test-path", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// Capture counter value before
	before := readCounterVec(t, HTTPRequestTotal, "GET", "/test-path", "200")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test-path", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	after := readCounterVec(t, HTTPRequestTotal, "GET", "/test-path", "200")
	if got := after - before; got != 1 {
		t.Errorf("expected HTTPRequestTotal to increment by 1, got %v", got)
	}
}

func TestGinMiddlewareUnknownPath(t *testing.T) {
	r := gin.New()
	r.Use(GinMiddleware())
	// No route registered — middleware will see path="" and substitute /unknown
	r.NoRoute(func(c *gin.Context) { c.String(http.StatusNotFound, "") })

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/does-not-exist", nil)
	r.ServeHTTP(w, req)

	// Counter for /unknown path + 404 status should exist
	m := &dto.Metric{}
	if err := HTTPRequestTotal.WithLabelValues("GET", "/unknown", "404").(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if m.GetCounter().GetValue() <= 0 {
		t.Errorf("expected /unknown 404 counter > 0")
	}
}

func TestGinMiddlewareRecordsDuration(t *testing.T) {
	r := gin.New()
	r.Use(GinMiddleware())
	r.GET("/slow", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/slow", nil)
	r.ServeHTTP(w, req)

	// Histogram observation count should be > 0 for this label set
	m := &dto.Metric{}
	if err := HTTPRequestDuration.WithLabelValues("GET", "/slow").(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("read histogram: %v", err)
	}
	hist := m.GetHistogram()
	if hist == nil {
		t.Fatal("expected histogram metric, got nil")
	}
	if hist.GetSampleCount() == 0 {
		t.Errorf("expected histogram sample count > 0")
	}
}

// readCounterVec reads a CounterVec value for the given labels.
func readCounterVec(t *testing.T, cv *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := cv.WithLabelValues(labels...).(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("read counter vec: %v", err)
	}
	return m.GetCounter().GetValue()
}
