package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/serverstate"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// newBroadcastTestEnv 构造「已初始化 server-state + 运行中 Hub + 已订阅 admin」
// 的测试环境，返回 (router, stateMgr, Owner access token, admin 订阅 channel,
// 退订函数)。
func newBroadcastTestEnv(t *testing.T) (*gin.Engine, *serverstate.Manager, string, <-chan []byte, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{ID: idgen.NextString(), Username: "owner", Email: "owner@example.com", PasswordHash: "x", Role: middleware.RoleOwner}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	stateMgr := newTestStateManager(t, cfg)
	if err := stateMgr.CompleteBootstrap(serverstate.NetworkConfig{
		ExternalHost:           "voice.example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		UseHTTPS:               true,
		WebVoiceEnabled:        false, // N29：初始锁定，用例里通过管理端打开
		ClientAccessEnabled:    true,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	hub := realtime.NewHub(testutil.TestLogger())
	go hub.Run()
	sub, unsub := hub.SubscribeAdmin()

	handler := NewHandlerWithState(db, cfg, hub, middleware.NewOwnerBreakGlassProtector(), stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))
	return router, stateMgr, accessToken, sub, unsub
}

// putNetworkWithToken 以 Owner 身份调用 PUT /api/admin/network。
func putNetworkWithToken(t *testing.T, router *gin.Engine, accessToken string, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/network", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(w, req)
	return w
}

// TestUpdateNetworkBroadcastsServerConfigChangedKeys N29：管理端保存网络配置
// 成功后，经 Hub 全局广播 server_config_updated，payload 只含变更键名。
func TestUpdateNetworkBroadcastsServerConfigChangedKeys(t *testing.T) {
	router, stateMgr, accessToken, sub, unsub := newBroadcastTestEnv(t)
	defer unsub()

	// 订阅先于请求建立，避免广播早于注册丢失。
	time.Sleep(50 * time.Millisecond)

	w := putNetworkWithToken(t, router, accessToken, map[string]interface{}{
		"externalHost":           "voice.example.com",
		"externalHttpPort":       443,
		"externalLiveKitWsPort":  443,
		"externalMediaUdpPort":   7882,
		"externalAdminPort":      9090,
		"externalLiveKitTcpPort": 7881,
		"useHttps":               true,
		"webVoiceEnabled":        true, // 唯一变更：false → true
		"clientAccessEnabled":    true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if !stateMgr.Network().WebVoiceEnabled {
		t.Fatal("expected WebVoiceEnabled=true after update")
	}

	select {
	case msg := <-sub:
		var evt struct {
			Type    string `json:"type"`
			Payload struct {
				Keys []string `json:"keys"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(msg, &evt); err != nil {
			t.Fatalf("unmarshal broadcast: %v (raw: %s)", err, string(msg))
		}
		if evt.Type != "server_config_updated" {
			t.Fatalf("event type = %v, want server_config_updated", evt.Type)
		}
		if len(evt.Payload.Keys) != 1 || evt.Payload.Keys[0] != "webVoiceEnabled" {
			t.Fatalf("keys = %v, want [webVoiceEnabled]（其余字段未变不得出现）", evt.Payload.Keys)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server_config_updated broadcast")
	}
}

// TestUpdateNetworkNoBroadcastWhenUnchanged N29：幂等保存（无字段变化）不广播，
// 避免在线客户端被无效刷新风暴打扰。
func TestUpdateNetworkNoBroadcastWhenUnchanged(t *testing.T) {
	router, _, accessToken, sub, unsub := newBroadcastTestEnv(t)
	defer unsub()
	time.Sleep(50 * time.Millisecond)

	w := putNetworkWithToken(t, router, accessToken, map[string]interface{}{
		"externalHost":           "voice.example.com",
		"externalHttpPort":       443,
		"externalLiveKitWsPort":  443,
		"externalMediaUdpPort":   7882,
		"externalAdminPort":      9090,
		"externalLiveKitTcpPort": 7881,
		"useHttps":               true,
		"webVoiceEnabled":        false, // 与现值一致
		"clientAccessEnabled":    true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	select {
	case msg := <-sub:
		t.Fatalf("expected no broadcast for unchanged config, got: %s", string(msg))
	case <-time.After(300 * time.Millisecond):
		// 未收到广播 = 预期行为
	}
}

// TestChangedNetworkKeys 键名推导函数的字段级用例：每个字段变更都能产出与
// NetworkConfig JSON 契约一致的键名。
func TestChangedNetworkKeys(t *testing.T) {
	base := serverstate.NetworkConfig{
		ExternalHost:           "a.example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
		UseHTTPS:               true,
		UPNPEnabled:            true,
		TURNTCPFallbackEnabled: true,
		WebVoiceEnabled:        false,
		ClientAccessEnabled:    true,
	}

	if got := changedNetworkKeys(base, base); len(got) != 0 {
		t.Fatalf("expected no keys for identical configs, got %v", got)
	}

	next := base
	next.WebVoiceEnabled = true
	next.UseHTTPS = false
	got := changedNetworkKeys(base, next)
	if len(got) != 2 {
		t.Fatalf("expected 2 changed keys, got %v", got)
	}
	found := map[string]bool{}
	for _, k := range got {
		found[k] = true
	}
	if !found["webVoiceEnabled"] || !found["useHttps"] {
		t.Fatalf("expected webVoiceEnabled+useHttps, got %v", got)
	}
}
