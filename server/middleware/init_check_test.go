package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// N15：引导白名单放行 HEAD。
// 服务器未初始化（无 Owner）时，运维/监控以 HEAD 探活管理页（curl -sI /admin），
// 此前白名单对 /admin 前缀仅放行 GET，HEAD 落入 503 AUTH_SERVER_NOT_INITIALIZED
// 误报。本文件锁定：HEAD /admin 与 GET 同权放行、/api/admin 仍被拦截、
// health 类后缀本就方法无关（无需改动，防止回退）。

// newInitCheckTestRouter 用与生产一致的 InitCheckWithState 搭一个最小路由，
// stateMgr 传 nil 且重置包级初始化标志，模拟「服务器尚未初始化」。
func newInitCheckTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ResetServerInitialized() // 确保处于未初始化状态（测试专用入口）

	r := gin.New()
	r.Use(InitCheckWithState(nil))
	// 下游 handler 只有被执行到才返回 200，用于区分「白名单放行」与「503 拦截」。
	ok := func(c *gin.Context) { c.String(http.StatusOK, "reached-handler") }
	r.GET("/admin", ok)
	r.HEAD("/admin", ok)
	r.HEAD("/api/admin/users", ok)
	r.HEAD("/health", ok)
	return r
}

func doHead(r *gin.Engine, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, target, nil)
	r.ServeHTTP(w, req)
	return w
}

// TestBootstrapWhitelist_HeadAdminAllowed 与 GET 同权放行 /admin 前缀。
func TestBootstrapWhitelist_HeadAdminAllowed(t *testing.T) {
	for _, path := range []string{"/admin", "/admin/", "/admin/assets/app.js"} {
		if !isBootstrapWhitelist(path, http.MethodHead) {
			t.Errorf("isBootstrapWhitelist(%q, HEAD) = false, want true", path)
		}
		// GET 行为不回退
		if !isBootstrapWhitelist(path, http.MethodGet) {
			t.Errorf("isBootstrapWhitelist(%q, GET) = false, want true", path)
		}
		// 白名单最小性：POST 不因本次改动被连带放行
		if isBootstrapWhitelist(path, http.MethodPost) {
			t.Errorf("isBootstrapWhitelist(%q, POST) = true, want false", path)
		}
	}
}

// TestBootstrapWhitelist_HealthSuffixIsMethodAgnostic 核对 /health 类后缀
// 本就与方法无关（HEAD 天然放行），无需改动——锁定该结论防止回退。
func TestBootstrapWhitelist_HealthSuffixIsMethodAgnostic(t *testing.T) {
	for _, path := range []string{"/api/v1/health", "/health", "/admin/healthz"} {
		if !isBootstrapWhitelist(path, http.MethodHead) {
			t.Errorf("isBootstrapWhitelist(%q, HEAD) = false, want true（health 后缀应方法无关）", path)
		}
	}
}

// TestBootstrapWhitelist_HeadApiAdminStillBlocked /api/admin 前缀不命中
// /admin 静态页白名单（Admin API 位于 /api/admin，引导期必须继续拦截）。
func TestBootstrapWhitelist_HeadApiAdminStillBlocked(t *testing.T) {
	for _, method := range []string{http.MethodHead, http.MethodGet, http.MethodPost} {
		if isBootstrapWhitelist("/api/admin/users", method) {
			t.Errorf("isBootstrapWhitelist(/api/admin/users, %s) = true, want false", method)
		}
	}
}

// TestInitCheckUninitialized_HeadAdminPasses 中间件级：未初始化时
// HEAD /admin 放行进入下游（等价于 GET 的探活体验）。
func TestInitCheckUninitialized_HeadAdminPasses(t *testing.T) {
	r := newInitCheckTestRouter(t)

	w := doHead(r, "/admin")
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD /admin status = %d, want 200（未初始化阶段 HEAD 探活应与 GET 同权放行）", w.Code)
	}
	if w.Body.String() != "reached-handler" {
		t.Errorf("HEAD /admin 未进入下游 handler, body = %q", w.Body.String())
	}
}

// TestInitCheckUninitialized_HeadApiAdminBlocked 中间件级：未初始化时
// HEAD /api/admin/users 仍被 503 拦截——白名单只覆盖静态页与引导端点，
// 不得扩大到 Admin API。
func TestInitCheckUninitialized_HeadApiAdminBlocked(t *testing.T) {
	r := newInitCheckTestRouter(t)

	w := doHead(r, "/api/admin/users")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("HEAD /api/admin/users status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "AUTH_SERVER_NOT_INITIALIZED") {
		t.Errorf("HEAD /api/admin/users body 未包含 AUTH_SERVER_NOT_INITIALIZED: %.200s", w.Body.String())
	}
}
