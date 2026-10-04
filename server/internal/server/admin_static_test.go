package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/features/cloudfs"
	"ridgericetalk/features/minigames"
	"ridgericetalk/features/schedule"
	"ridgericetalk/features/sharedoc"
	"ridgericetalk/internal/admin"
	"ridgericetalk/internal/auth"
	"ridgericetalk/internal/bots"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/database"
	"ridgericetalk/internal/message"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/serverstate"
	"ridgericetalk/internal/sfx"
	"ridgericetalk/internal/storage"
	"ridgericetalk/internal/voice"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// 内嵌管理页静态服务（GET /admin 与 /admin/*filepath）此前零测试
// （全仓 *_test.go 中 webhost 零命中）。这里用与生产一致的中间件栈和路由
// 注册函数（buildBaseEngine + registerAdminAPIRoutes）做冒烟，锁住三件事：
//   1. /admin 与 index.html 里引用的静态资源必须在专用管理端口可达；
//   2. 不存在的静态资源必须是 404，不能退化成把 index.html 当 JS/CSS 返回；
//   3. /admin/ws 不能被静态路由吃掉（无 token 必须 401 而不是返回页面）。
//
// 前置：server/webhost/dist/admin 是构建产物（被 .gitignore 忽略，见
// server/webhost/dist/），未构建时跳过并说明原因，而不是假装通过。

// findEmbeddedAdminIndex 返回内嵌管理页的 index.html 路径。
func findEmbeddedAdminIndex(t *testing.T) string {
	t.Helper()
	indexPath := filepath.Join(webhostDir("admin"), "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		t.Skipf("内嵌管理页尚未构建（%s 不存在；server/webhost/dist 被 .gitignore 忽略）：%v", indexPath, err)
	}
	return indexPath
}

// newAdminEngineForStaticTest 用生产中间件栈搭一个只有管理端路由的引擎。
// initialized=false 用于验证「服务器还没初始化时管理页也必须可达」
// （middleware.isBootstrapWhitelist 对 GET /admin* 放行）。
func newAdminEngineForStaticTest(t *testing.T, initialized bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	findEmbeddedAdminIndex(t)

	cfg := config.DefaultConfig()
	cfg.Env = "development"
	cfg.LocalDataPath = t.TempDir()
	cfg.JWTSecret = "test-secret"

	log := testutil.TestLogger()
	gormDB := testutil.MustSetupTestDB()
	db := &database.DB{DB: gormDB}
	stateMgr, err := serverstate.NewManager(cfg)
	if err != nil {
		t.Fatalf("new state manager: %v", err)
	}
	if initialized {
		network := serverstate.DefaultNetworkConfig()
		network.ExternalHost = stateMgr.ResolveExternalHost()
		if err := stateMgr.CompleteBootstrap(network); err != nil {
			t.Fatalf("bootstrap state manager: %v", err)
		}
	}
	hub := realtime.NewHub(log)
	go hub.Run()

	appStorage, err := storage.NewLocalStorage(t.TempDir(), "http://localhost/api/v1/files")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}

	engine := buildBaseEngine(log, cfg, stateMgr)
	app := &App{
		cfg:                 cfg,
		log:                 log,
		db:                  db,
		stateMgr:            stateMgr,
		hub:                 hub,
		appStorage:          appStorage,
		authHandler:         auth.NewHandlerWithState(gormDB, cfg, stateMgr),
		voiceHandler:        voice.NewHandler(gormDB, cfg, hub),
		botsHandler:         bots.NewHandler(gormDB, cfg, hub),
		cloudfsHandler:      cloudfs.NewHandler(gormDB, cfg, hub),
		sharedocHandler:     sharedoc.NewHandler(gormDB, cfg, hub),
		scheduleHandler:     schedule.NewHandler(gormDB, cfg, hub),
		minigamesHandler:    minigames.NewHandler(gormDB, cfg, hub),
		sfxService:          sfx.NewService(gormDB, cfg, hub),
		messageService:      message.NewService(gormDB),
		adminSvc:            admin.NewService(gormDB, cfg, hub),
		adminWSHandler:      admin.NewAdminWSHandler(hub, cfg, gormDB, log),
		breakGlassProtector: middleware.NewOwnerBreakGlassProtector(),
		globalLimiter:       middleware.NewRateLimiter(1000, 2000),
		adminEngine:         engine,
	}

	healthHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	app.registerAdminAPIRoutes(healthHandler)
	return engine
}

func adminStaticGET(t *testing.T, engine *gin.Engine, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	engine.ServeHTTP(w, req)
	return w
}

func TestAdminStaticServesEmbeddedSPA(t *testing.T) {
	engine := newAdminEngineForStaticTest(t, false)

	w := adminStaticGET(t, engine, "/admin")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /admin status = %d, want 200; body=%.200s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="root"`) || !strings.Contains(body, "RidgeRiceTalk") {
		t.Errorf("GET /admin 未返回管理页 index.html: %.200s", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("GET /admin Content-Type = %q, want text/html", ct)
	}

	// index.html 里引用的资源必须真的能取到（相对 /admin/ base）。
	refs := regexp.MustCompile(`(?:src|href)="(/admin/assets/[^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(refs) == 0 {
		t.Fatalf("index.html 未引用任何 /admin/assets/ 资源: %.400s", body)
	}
	for _, m := range refs {
		asset := m[1]
		aw := adminStaticGET(t, engine, asset)
		if aw.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200（index.html 引用的资源必须可达）", asset, aw.Code)
		}
	}
}

func TestAdminStaticMissingAssetIs404NotSPAFallback(t *testing.T) {
	engine := newAdminEngineForStaticTest(t, false)

	for _, target := range []string{
		"/admin/assets/does-not-exist.js",
		"/admin/channels", // 管理页是 tab 式 SPA，没有 URL 路由；不存在的路径不能返回 HTML
	} {
		w := adminStaticGET(t, engine, target)
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", target, w.Code)
		}
		if strings.Contains(w.Body.String(), `id="root"`) {
			t.Errorf("GET %s 返回了 index.html —— 前端会把 HTML 当 JS/CSS 解析", target)
		}
	}
}

// 初始化完成后，非 /api 的未知路径走 NoRoute 的 SPA 回退 —— 运维直接打开
// http://host:9090/ 也能拿到管理页。（未初始化时会被 InitCheck 提前拦成 503，
// 因此这里用 initialized=true 的引擎。）
func TestAdminEngineNoRouteFallsBackToSPA(t *testing.T) {
	engine := newAdminEngineForStaticTest(t, true)

	for _, target := range []string{"/", "/some-unknown-entry"} {
		w := adminStaticGET(t, engine, target)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200（SPA 回退）; body=%.200s", target, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `id="root"`) {
			t.Errorf("GET %s 未回退到管理页 index.html: %.200s", target, w.Body.String())
		}
	}

	// SPA 回退不能吃掉 /api：管理端口的 API 未知路径必须是 JSON 404，
	// 否则前端 fetch 会把 index.html 当成 JSON 解析。
	w := adminStaticGET(t, engine, "/api/admin/not-a-real-endpoint")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /api/admin/not-a-real-endpoint status = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), `id="root"`) {
		t.Error("/api 路径被 SPA 回退吃掉了")
	}
}

// 未建连接的 /admin/ws 必须由 WS 处理器回答（401），不能被静态文件服务吃掉
// —— 否则客户端会拿到 200 + HTML，鉴权与事件推送同时失效。
func TestAdminWSPathIsNotSwallowedByStaticHandler(t *testing.T) {
	engine := newAdminEngineForStaticTest(t, false)

	w := adminStaticGET(t, engine, "/admin/ws")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /admin/ws status = %d, want 401; body=%.200s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `id="root"`) {
		t.Error("/admin/ws 被静态路由吃掉了（返回了 index.html）")
	}
	if !strings.Contains(w.Body.String(), "AUTH_UNAUTHORIZED") {
		t.Errorf("GET /admin/ws body = %.200s, want AUTH_UNAUTHORIZED 错误信封", w.Body.String())
	}
}

// /admin 目录本身也要可服务（http.FileServer 的目录行为）——不能 500。
func TestAdminStaticDirectoryDoesNotError(t *testing.T) {
	engine := newAdminEngineForStaticTest(t, false)

	w := adminStaticGET(t, engine, "/admin/")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /admin/ status = %d, want 200", w.Code)
	}
}
