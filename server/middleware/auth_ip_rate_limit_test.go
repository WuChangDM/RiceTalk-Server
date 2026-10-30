package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// withAuthIPLimitEnv 在测试期间设置专用限速环境变量，结束后自动恢复。
func withAuthIPLimitEnv(t *testing.T, perMin, burst string) {
	t.Helper()
	t.Setenv("RRT_AUTH_RATE_LIMIT_PER_MIN", perMin)
	t.Setenv("RRT_AUTH_RATE_LIMIT_BURST", burst)
}

// TestAuthIPRateLimitDefaults verifies the D2 defaults: 10 requests/min per
// IP with burst 5, applied on a 1-minute window.
func TestAuthIPRateLimitDefaults(t *testing.T) {
	limiter := AuthIPRateLimit()
	if limiter == nil {
		t.Fatal("expected non-nil limiter when env unset")
	}
	if limiter.requests != 10 {
		t.Errorf("expected requests=10, got %d", limiter.requests)
	}
	if limiter.burst != 5 {
		t.Errorf("expected burst=5, got %d", limiter.burst)
	}
	if limiter.window != time.Minute {
		t.Errorf("expected window=1m, got %v", limiter.window)
	}
}

// TestAuthIPRateLimitEnvOverride verifies RRT_AUTH_RATE_LIMIT_PER_MIN and
// RRT_AUTH_RATE_LIMIT_BURST override the defaults.
func TestAuthIPRateLimitEnvOverride(t *testing.T) {
	withAuthIPLimitEnv(t, "3", "2")
	limiter := AuthIPRateLimit()
	if limiter == nil {
		t.Fatal("expected non-nil limiter with override env")
	}
	if limiter.requests != 3 {
		t.Errorf("expected requests=3, got %d", limiter.requests)
	}
	if limiter.burst != 2 {
		t.Errorf("expected burst=2, got %d", limiter.burst)
	}
}

// TestAuthIPRateLimitDisabled verifies RRT_AUTH_RATE_LIMIT_PER_MIN=0 disables
// the dedicated limiter (nil returned → caller skips mounting it, traffic
// falls back to the global bucket + auth group bucket).
func TestAuthIPRateLimitDisabled(t *testing.T) {
	withAuthIPLimitEnv(t, "0", "")
	if limiter := AuthIPRateLimit(); limiter != nil {
		t.Fatalf("expected nil limiter with PER_MIN=0, got %+v", limiter)
	}
	// Invalid / negative values keep the dedicated limiter enabled with
	// defaults (only an explicit 0 opts out).
	t.Setenv("RRT_AUTH_RATE_LIMIT_PER_MIN", "-5")
	if limiter := AuthIPRateLimit(); limiter == nil {
		t.Fatal("expected non-nil limiter for negative env value")
	}
}

// TestAuthIPRateLimitHitAndMiss verifies the burst budget: the first `burst`
// requests pass, the next one is rejected, and another IP is unaffected
// (per-IP isolation).
func TestAuthIPRateLimitHitAndMiss(t *testing.T) {
	limiter := AuthIPRateLimit() // 10/min, burst 5

	for i := 0; i < 5; i++ {
		if !limiter.Allow("203.0.113.10") {
			t.Fatalf("request %d from ip-a should pass within burst", i+1)
		}
	}
	// 6th request from the same IP must be rejected.
	if limiter.Allow("203.0.113.10") {
		t.Fatal("6th request from ip-a should be rate limited")
	}
	// A different IP has its own budget.
	for i := 0; i < 5; i++ {
		if !limiter.Allow("198.51.100.20") {
			t.Fatalf("request %d from ip-b should pass (per-IP isolation)", i+1)
		}
	}
	if limiter.Allow("198.51.100.20") {
		t.Fatal("6th request from ip-b should be rate limited")
	}
}

// TestAuthIPRateLimitWindowReset verifies that once the window elapses the
// tokens are replenished and the same IP is allowed again. The test backdates
// the client's lastCheck instead of sleeping out the real minute.
func TestAuthIPRateLimitWindowReset(t *testing.T) {
	limiter := AuthIPRateLimit() // 10/min, burst 5
	ip := "203.0.113.30"

	for i := 0; i < 5; i++ {
		if !limiter.Allow(ip) {
			t.Fatalf("request %d should pass within burst", i+1)
		}
	}
	if limiter.Allow(ip) {
		t.Fatal("request beyond burst should be rate limited")
	}

	// Backdate lastCheck by a full window: elapsed >= window replenishes
	// `requests` tokens (capped at burst), so the next request passes again.
	idx := shardIndex(ip)
	shard := limiter.shards[idx]
	shard.mu.Lock()
	cl, ok := shard.clients[ip]
	if !ok {
		shard.mu.Unlock()
		t.Fatal("client entry missing after Allow calls")
	}
	cl.lastCheck = time.Now().Add(-2 * time.Minute)
	shard.mu.Unlock()

	if !limiter.Allow(ip) {
		t.Fatal("request after a full window should be allowed (window reset)")
	}
}

// TestAuthIPRateLimitMiddleware429 wires the credential limiter via the same
// RateLimit middleware used in production and asserts the 429 response uses
// the standard error envelope (code SYSTEM_RATE_LIMITED) plus Retry-After.
func TestAuthIPRateLimitMiddleware429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withAuthIPLimitEnv(t, "10", "2")

	limiter := AuthIPRateLimit()
	if limiter == nil {
		t.Fatal("expected non-nil limiter")
	}
	r := gin.New()
	r.POST("/api/auth/login", RateLimit(limiter), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": "OK"})
	})

	do := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"email": "a@b.c", "password": "x"})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	// burst=2 → two passes, third blocked with the standard error style.
	if w := do(); w.Code != http.StatusOK {
		t.Fatalf("request 1 should pass, got %d", w.Code)
	}
	if w := do(); w.Code != http.StatusOK {
		t.Fatalf("request 2 should pass, got %d", w.Code)
	}
	w := do()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("request 3 should be rate limited (429), got %d: %s", w.Code, w.Body.String())
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Error("expected Retry-After header on 429")
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if resp["code"] != "SYSTEM_RATE_LIMITED" {
		t.Errorf("expected error code SYSTEM_RATE_LIMITED, got %v", resp["code"])
	}
}
