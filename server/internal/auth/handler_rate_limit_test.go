package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/tests/testutil"
)

// TestHandlerLoginDedicatedIPRateLimit verifies D2 加固：login/register 挂载了
// 独立的每 IP 专用限速。PER_MIN=1 / BURST=1 时第二次登录就必须 429 —— 若专用
// 桶未挂载，第二次请求仍会打到 handler（组级 10/5 桶还有余量）。
func TestHandlerLoginDedicatedIPRateLimit(t *testing.T) {
	t.Setenv("RRT_AUTH_RATE_LIMIT_PER_MIN", "1")
	t.Setenv("RRT_AUTH_RATE_LIMIT_BURST", "1")

	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	handler := NewHandler(db, cfg)

	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	doLogin := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{
			"email":    "ratelimit@example.com",
			"password": "wrong-password",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}

	// 第一次：专用桶放行，落到 handler（凭据错误 → 4xx，但绝不是 429）。
	w1 := doLogin()
	if w1.Code == http.StatusTooManyRequests {
		t.Fatalf("first login should not be rate limited, got 429: %s", w1.Body.String())
	}

	// 第二次：专用桶（burst=1 已耗尽）→ 429 + 标准错误信封。
	w2 := doLogin()
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second login should be rate limited (429), got %d: %s", w2.Code, w2.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["code"] != "SYSTEM_RATE_LIMITED" {
		t.Errorf("expected SYSTEM_RATE_LIMITED, got %v", resp["code"])
	}
	if w2.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429")
	}
}

// TestHandlerLoginDedicatedIPRateLimitDisabled verifies PER_MIN=0 时专用桶不
// 挂载：同 IP 连续多次登录不再被专用限速拦下（第 2 次请求能打到 handler，
// 返回凭据错误而非 429）。
func TestHandlerLoginDedicatedIPRateLimitDisabled(t *testing.T) {
	t.Setenv("RRT_AUTH_RATE_LIMIT_PER_MIN", "0")

	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	handler := NewHandler(db, cfg)

	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	for i := 0; i < 2; i++ {
		body, _ := json.Marshal(map[string]string{
			"email":    "disabled@example.com",
			"password": "wrong-password",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d should not be rate limited when PER_MIN=0", i+1)
		}
	}
}
