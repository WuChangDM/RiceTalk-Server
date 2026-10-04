package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/features/cloudfs"
	"ridgericetalk/internal/config"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// TestShareGroupCoexistsWithExistingAPIPaths 是两条回归断言（DES-2026-0912-05）：
//
//  1. 路由冲突：/api/v1/share/:token 与既有的 /api/v1/sharedoc/*、
//     /api/v1/screenshare/* 共享 "share" 前缀，gin 的路由树在这里同时出现
//     静态子节点与参数子节点。这个测试用真实路径形态注册一次，确保服务器
//     不会在启动时 panic（"conflicts with existing wildcard"）。
//  2. CSRF：分享 group 与 registerRoutes 一样挂在引擎根上，**不经过**
//     CSRFProtection，因此匿名 POST /verify 必须能打到 handler（返回业务层的
//     401，而不是 CSRF 的 403）——否则浏览器直接打开分享链接会永远失败。
func TestShareGroupCoexistsWithExistingAPIPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := testutil.MustSetupTestDB()
	cfg := &config.Config{Env: "development", LocalDataPath: t.TempDir()}
	h := cloudfs.NewHandler(db, cfg, nil)

	r := gin.New()
	noop := func(c *gin.Context) { c.Status(http.StatusNoContent) }

	// 既有 /api/v1 前缀家族（形态取自各自 handler 的实际注册）。
	apiV1 := r.Group("/api/v1")
	apiV1.Use(middleware.CSRFProtection(cfg))
	apiV1.GET("/sharedoc/list", noop)
	apiV1.GET("/sharedoc/:id/versions", noop)
	apiV1.POST("/screenshare/start", noop)
	apiV1.GET("/screenshare/sessions/active", noop)
	apiV1.GET("/server/version", noop)

	// 与 internal/server/routes.go 相同的挂法。
	h.RegisterShareRoutes(r.Group("/api/v1/share"))

	// 冲突检查：静态分支仍可路由。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/sharedoc/list", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("static sibling route broke after adding the share group: %d", w.Code)
	}

	// 参数分支可路由，且没有 CSRF cookie 也能到达业务层（401 而非 403）。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/share/unknown-token/verify", nil))
	if w.Code == http.StatusForbidden {
		t.Fatalf("share group must not be behind CSRFProtection, got 403: %s", w.Body.String())
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("verify on an unknown token = %d, want 401 from the handler: %s", w.Code, w.Body.String())
	}
}
