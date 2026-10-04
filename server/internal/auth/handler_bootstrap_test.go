package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/internal/serverstate"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func TestCreateOwnerWithNetworkConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()

	tmp := t.TempDir()
	cfg.LocalDataPath = tmp
	stateMgr, err := serverstate.NewManager(cfg)
	if err != nil {
		t.Fatalf("new state manager: %v", err)
	}

	svc := NewService(db, cfg)
	token, err := svc.CreateBootstrapToken()
	if err != nil {
		t.Fatalf("create bootstrap token: %v", err)
	}

	handler := NewHandlerWithState(db, cfg, stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	body, _ := json.Marshal(map[string]interface{}{
		"username":          "owner",
		"email":             "owner@example.com",
		"password":          "Password123!",
		"displayName":       "Owner",
		"bootstrapToken":    token,
		"spaceName":         "Test Server",
		"securityQuestions": testSecurityQuestions(),
		"network": map[string]interface{}{
			"externalHost":          "voice.example.com",
			"externalHttpPort":      443,
			"externalLiveKitWsPort": 443,
			"externalMediaUdpPort":  7882,
			"useHttps":              true,
			"webVoiceEnabled":       true,
			"clientAccessEnabled":   true,
		},
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/bootstrap/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	if !stateMgr.IsInitialized() {
		t.Fatal("expected server state to be initialized")
	}
	if stateMgr.Network().ExternalHost != "voice.example.com" {
		t.Errorf("externalHost = %v, want voice.example.com", stateMgr.Network().ExternalHost)
	}
	if !stateMgr.Network().WebVoiceEnabled {
		t.Error("expected WebVoiceEnabled to be true")
	}

	// Verify that the package-level initialization flag is also set so legacy
	// middleware continues to work.
	if !middleware.IsServerInitialized() {
		t.Error("expected legacy IsServerInitialized to be true")
	}
	middleware.ResetServerInitialized()
}

func TestCreateOwnerWithoutNetworkConfigFillsDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()

	tmp := t.TempDir()
	cfg.LocalDataPath = tmp
	stateMgr, err := serverstate.NewManager(cfg)
	if err != nil {
		t.Fatalf("new state manager: %v", err)
	}

	svc := NewService(db, cfg)
	token, err := svc.CreateBootstrapToken()
	if err != nil {
		t.Fatalf("create bootstrap token: %v", err)
	}

	handler := NewHandlerWithState(db, cfg, stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	body, _ := json.Marshal(map[string]interface{}{
		"username":          "owner",
		"email":             "owner@example.com",
		"password":          "Password123!",
		"displayName":       "Owner",
		"bootstrapToken":    token,
		"spaceName":         "Test Server",
		"securityQuestions": testSecurityQuestions(),
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/bootstrap/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	if !stateMgr.IsInitialized() {
		t.Fatal("expected server state to be initialized")
	}
	if stateMgr.Network().ExternalHost == "" {
		t.Error("expected ExternalHost to be filled with a default")
	}
	// N25 修复后默认端口取**实际配置**（cfg.Port）而非硬编 443 —— 443 是反代
	// 生产口径，embedded/直连部署用它会把对外广播地址写错。
	if stateMgr.Network().ExternalHTTPPort != cfg.Port {
		t.Errorf("expected HTTP port %d (cfg.Port), got %d", cfg.Port, stateMgr.Network().ExternalHTTPPort)
	}
	if !stateMgr.Network().ClientAccessEnabled {
		t.Error("expected ClientAccessEnabled=true after bootstrap (N25)")
	}
	// N29：Web 语音缺省启用 —— 自托管语音平台开箱即用；管理员可初始化后
	// 在管理后台显式关闭。缺省 network 未传任何字段时也必须为 true。
	if !stateMgr.Network().WebVoiceEnabled {
		t.Error("expected WebVoiceEnabled=true after bootstrap by default (N29)")
	}
	if stateMgr.CurrentState() != serverstate.StateClientAccessReady {
		t.Errorf("expected state CLIENT_ACCESS_READY after bootstrap, got %v", stateMgr.CurrentState())
	}
	middleware.ResetServerInitialized()
}

// TestCreateOwnerWebVoiceDefaultTrueWithPartialNetwork 验证：bootstrap 请求
// 显式传了 network 但**未包含 webVoiceEnabled 字段**（bool 零值无法区分
// 「未传」与「显式 false」）时，同样落缺省 true（N29 与 N25 同口径）。
func TestCreateOwnerWebVoiceDefaultTrueWithPartialNetwork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()

	tmp := t.TempDir()
	cfg.LocalDataPath = tmp
	stateMgr, err := serverstate.NewManager(cfg)
	if err != nil {
		t.Fatalf("new state manager: %v", err)
	}

	svc := NewService(db, cfg)
	token, err := svc.CreateBootstrapToken()
	if err != nil {
		t.Fatalf("create bootstrap token: %v", err)
	}

	handler := NewHandlerWithState(db, cfg, stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	body, _ := json.Marshal(map[string]interface{}{
		"username":          "owner",
		"email":             "owner@example.com",
		"password":          "Password123!",
		"displayName":       "Owner",
		"bootstrapToken":    token,
		"spaceName":         "Test Server",
		"securityQuestions": testSecurityQuestions(),
		// network 显式给出，但不含 webVoiceEnabled / clientAccessEnabled
		"network": map[string]interface{}{
			"externalHost": "voice.example.com",
		},
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/bootstrap/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !stateMgr.Network().WebVoiceEnabled {
		t.Error("expected WebVoiceEnabled=true when omitted from network config (N29)")
	}
	if !stateMgr.Network().ClientAccessEnabled {
		t.Error("expected ClientAccessEnabled=true when omitted from network config (N25)")
	}
	middleware.ResetServerInitialized()
}

func TestCreateOwnerWithoutStateManagerStillWorks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()

	svc := NewService(db, cfg)
	token, err := svc.CreateBootstrapToken()
	if err != nil {
		t.Fatalf("create bootstrap token: %v", err)
	}

	handler := NewHandler(db, cfg)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	body, _ := json.Marshal(map[string]interface{}{
		"username":          "owner",
		"email":             "owner@example.com",
		"password":          "Password123!",
		"displayName":       "Owner",
		"bootstrapToken":    token,
		"spaceName":         "Test Server",
		"securityQuestions": testSecurityQuestions(),
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/bootstrap/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	middleware.ResetServerInitialized()
}
