package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestSecurityHeadersMiddlewareSetsServerHeader verifies L29: the Server
// header is overridden to "RRT-Server" to hide framework info.
func TestSecurityHeadersMiddlewareSetsServerHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeadersMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Server"); got != "RRT-Server" {
		t.Errorf("L29: expected Server header 'RRT-Server', got %q", got)
	}
}

// TestAuthRateLimitThreshold verifies L30: AuthRateLimit returns a limiter
// configured for 10 requests per minute with burst 5 (per API spec §11.2).
func TestAuthRateLimitThreshold(t *testing.T) {
	limiter := AuthRateLimit()
	if limiter.requests != 10 {
		t.Errorf("L30: expected requests=10, got %d", limiter.requests)
	}
	if limiter.burst != 5 {
		t.Errorf("L30: expected burst=5, got %d", limiter.burst)
	}
	if limiter.window != time.Minute {
		t.Errorf("L30: expected window=1m, got %v", limiter.window)
	}
}

// TestRateLimitReturnsRetryAfterHeader verifies L28: when rate limited, the
// response includes a Retry-After header with a positive integer value.
func TestRateLimitReturnsRetryAfterHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Create a limiter with very low threshold to trigger rate limiting
	limiter := NewRateLimiter(1, 1) // 1 req/min, burst 1
	r := gin.New()
	r.Use(RateLimit(limiter))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// First request should succeed (uses the burst token)
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request should succeed, got %d", w1.Code)
	}
	// First request should NOT have Retry-After header
	if ra := w1.Header().Get("Retry-After"); ra != "" {
		t.Errorf("first request should not have Retry-After header, got %q", ra)
	}

	// Second request should be rate limited (429)
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request should be rate limited (429), got %d", w2.Code)
	}
	// Second request SHOULD have Retry-After header
	ra := w2.Header().Get("Retry-After")
	if ra == "" {
		t.Fatal("L28: expected Retry-After header on 429 response, got empty")
	}
	// Retry-After should be a positive integer
	raInt, err := parseInt(ra)
	if err != nil {
		t.Fatalf("L28: Retry-After should be integer, got %q: %v", ra, err)
	}
	if raInt <= 0 {
		t.Errorf("L28: Retry-After should be positive, got %d", raInt)
	}
}

// TestRetryAfterCalculation verifies the RetryAfter method returns sensible
// values for different limiter configurations.
func TestRetryAfterCalculation(t *testing.T) {
	// Global limiter: 60 req/min → ~1 token/sec → RetryAfter should be ~2 sec
	globalLimiter := NewRateLimiter(60, 10)
	// Exhaust tokens
	for i := 0; i < 10; i++ {
		globalLimiter.Allow("client-1")
	}
	if !globalLimiter.Allow("client-1") {
		// Last Allow should have returned false; if true, tokens weren't exhausted
	}
	ra := globalLimiter.RetryAfter("client-1")
	if ra <= 0 {
		t.Errorf("RetryAfter for exhausted global limiter should be > 0, got %d", ra)
	}
	// 60 req/min → 1 token/sec → wait ~1-2 sec for 1 token
	if ra > 5 {
		t.Errorf("RetryAfter for 60/min limiter should be small (<=5), got %d", ra)
	}

	// Auth limiter: 10 req/min → ~1 token/6sec → RetryAfter should be ~7 sec
	authLimiter := NewRateLimiter(10, 5)
	for i := 0; i < 5; i++ {
		authLimiter.Allow("client-2")
	}
	authLimiter.Allow("client-2") // exhaust
	raAuth := authLimiter.RetryAfter("client-2")
	if raAuth <= 0 {
		t.Errorf("RetryAfter for exhausted auth limiter should be > 0, got %d", raAuth)
	}
	// 10 req/min → 1 token/6sec → wait ~7 sec for 1 token
	if raAuth < 6 || raAuth > 10 {
		t.Errorf("RetryAfter for 10/min limiter should be ~7, got %d", raAuth)
	}
}

// TestRetryAfterForUnknownClient returns 0 for unknown clients.
func TestRetryAfterForUnknownClient(t *testing.T) {
	limiter := NewRateLimiter(60, 10)
	ra := limiter.RetryAfter("unknown-client")
	if ra != 0 {
		t.Errorf("RetryAfter for unknown client should be 0, got %d", ra)
	}
}

// TestRetryAfterForClientWithTokens returns 0 when client has tokens.
func TestRetryAfterForClientWithTokens(t *testing.T) {
	limiter := NewRateLimiter(60, 10)
	limiter.Allow("client-1") // creates client with tokens
	ra := limiter.RetryAfter("client-1")
	if ra != 0 {
		t.Errorf("RetryAfter for client with tokens should be 0, got %d", ra)
	}
}

// parseInt is a small helper to parse Retry-After header value.
func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, &parseIntError{s: s}
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

type parseIntError struct{ s string }

func (e *parseIntError) Error() string { return "not an integer: " + e.s }
