package virtualnet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// newEasytierMock returns an httptest.Server that simulates the EasyTier Web API
// for the endpoints used by virtualnet (health check).
// DES-2026-0731-02: EasyTier 服务端仅做健康检查，不通过 Web API 注册节点。
func newEasytierMock(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Health endpoint — returns 200 OK when EasyTier is "running"
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return httptest.NewServer(mux)
}

// newHandlerWithEasytier wires a handler whose config points at the mock
// EasyTier server, plus a user/space/membership for membership checks.
func newHandlerWithEasytier(t *testing.T) (*Handler, *gin.Engine, *model.User, *model.Space, *gorm.DB) {
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	// Point config at the mock EasyTier server
	et := newEasytierMock(t)
	t.Cleanup(et.Close)
	cfg.EasytierURL = et.URL
	cfg.EasytierSecret = "test-network-secret"
	cfg.EasytierPort = 5007
	cfg.PublicAddress = "127.0.0.1"

	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	handler := NewHandler(db, cfg, hub)

	user := &model.User{
		ID: idgen.NextString(), Username: "user", Email: "user@example.com",
		PasswordHash: "x", Role: middleware.RoleMember, IsActive: true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{
		ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID,
		Role: middleware.RoleMember, JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	r := gin.New()
	vn := r.Group("/api/virtualnet")
	{
		vn.GET("/status", handler.GetStatus)
		vn.POST("/connect", handler.Connect)
		vn.POST("/disconnect", handler.Disconnect)
		vn.GET("/nodes", handler.GetNodes)
	}
	return handler, r, user, space, db
}

// --- isEasytierAvailable with mock ---

func TestIsEasytierAvailableWithMock(t *testing.T) {
	handler, _, _, _, _ := newHandlerWithEasytier(t)

	if !handler.isEasytierAvailable() {
		t.Errorf("expected isEasytierAvailable=true when mock is running")
	}
}

func TestIsEasytierAvailableUnreachable(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	// Point at a closed port — connection refused
	cfg.EasytierURL = "http://127.0.0.1:1"
	cfg.EasytierSecret = "key"
	h := NewHandler(db, cfg, nil)

	if h.isEasytierAvailable() {
		t.Errorf("expected isEasytierAvailable=false when server unreachable")
	}
}

// TestIsEasytierAvailableTCPProbeIgnoresHTTPStatus 固化 isEasytierAvailable 的
// 实际契约：可用性由“TCP 端口能否建立连接”判定，与 HTTP 响应状态无关。
// EasyTier 的 RPC Portal（127.0.0.1:11210）是 gRPC 服务，不提供 HTTP/1.1
// 健康端点，因此只要端口在监听即视为可用——健康端点的 HTTP 状态不参与判定。
//
// 历史说明：本用例原为 TestIsEasytierAvailableBadStatus，断言“健康检查返回
// 500 时不可用”。该断言源自更早的 Headscale（HTTP 健康检查）实现；2026-07-31
// 切换到 EasyTier 并把探测改为 TCP 后，用例体未同步更新，故长期误报失败。
func TestIsEasytierAvailableTCPProbeIgnoresHTTPStatus(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	cfg.EasytierURL = srv.URL
	cfg.EasytierSecret = "key"
	h := NewHandler(db, cfg, nil)

	// TCP 端口在监听 → 可用；HTTP 500 不改变判定。
	if !h.isEasytierAvailable() {
		t.Errorf("expected isEasytierAvailable=true when the TCP port is listening (HTTP status is not probed)")
	}
}

// TestIsEasytierAvailableNotConfigured lives in handler_test.go to avoid duplicate declaration.

// --- easytierRequest with mock ---

func TestEasytierRequestSuccess(t *testing.T) {
	handler, _, _, _, _ := newHandlerWithEasytier(t)

	body, status, err := handler.easytierRequest("GET", "/api/v1/health")
	if err != nil {
		t.Fatalf("easytierRequest: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if len(body) == 0 {
		t.Errorf("expected non-empty body")
	}
}

func TestEasytierRequestConnectionError(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	cfg.EasytierURL = "http://127.0.0.1:1"
	cfg.EasytierSecret = "key"
	h := NewHandler(db, cfg, nil)

	_, _, err := h.easytierRequest("GET", "/api/v1/health")
	if err == nil {
		t.Errorf("expected error when server unreachable")
	}
}

// --- Connect/Disconnect with EasyTier ---

func TestConnectEasytierSuccess(t *testing.T) {
	handler, _, user, space, _ := newHandlerWithEasytier(t)

	// 新建 engine：必须先注册中间件再注册路由，否则 Gin 中间件不生效
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", user.ID)
		c.Set("username", user.Username)
		c.Set("space_id", space.ID)
		c.Next()
	})
	r.POST("/api/virtualnet/connect", handler.Connect)

	w := performRequest(r, "POST", "/api/virtualnet/connect", map[string]string{})
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data in response")
	}

	// DES-2026-0731-02: EasyTier Connect 应返回网络凭证
	if data["networkName"] != "ridgericetalk" {
		t.Errorf("networkName = %v, want ridgericetalk", data["networkName"])
	}
	if data["networkSecret"] != "test-network-secret" {
		t.Errorf("networkSecret = %v, want test-network-secret", data["networkSecret"])
	}
	// username 已退役：节点名改用 userId（ASCII 安全、唯一、非展示字段）
	if data["nodeName"] != fmt.Sprintf("rrt-%s", user.ID) {
		t.Errorf("nodeName = %v, want rrt-%s", data["nodeName"], user.ID)
	}
}

// performRequest is a helper that executes an HTTP request against a gin engine
func performRequest(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		req, _ = http.NewRequest(method, path, bytes.NewReader(jsonBody))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
