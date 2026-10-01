package virtualnet

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func init() {
	gin.SetMode(gin.TestMode)
	_ = idgen.Init(1, 1)
}

// setupTestSpace creates a user, space, and membership for virtualnet tests.
// Returns the user, space and an access token bound to the space.
func setupTestSpace(t *testing.T, db *gorm.DB) (*model.User, *model.Space, string) {
	user := &model.User{
		ID:           idgen.NextString(),
		Username:     "vnuser",
		Email:        "vn@example.com",
		PasswordHash: "x",
		Role:         middleware.RoleMember,
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{
		ID:      idgen.NextString(),
		Name:    "VN Space",
		OwnerID: user.ID,
	}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	membership := &model.Membership{
		ID:       idgen.NextString(),
		UserID:   user.ID,
		SpaceID:  space.ID,
		Role:     middleware.RoleMember,
		JoinedAt: time.Now().UTC(),
	}
	if err := db.Create(membership).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	token, _, err := middleware.GenerateTokenPair(user.ID, user.Username, user.Email, user.Role, 0, space.ID, "", testConfig())
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return user, space, token
}

func testConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-key-for-testing"
	return cfg
}

// newHandlerWithAuth wires the virtualnet handler with real AuthRequired
// middleware so RequireSpaceID can read the space ID from the JWT claim.
func newHandlerWithAuth(t *testing.T) (*Handler, *gin.Engine, *model.User, *model.Space, string) {
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	handler := NewHandler(db, cfg, hub)

	user, space, token := setupTestSpace(t, db)

	r := gin.New()
	r.Use(middleware.AuthRequired(cfg, db))
	handler.RegisterRoutes(r.Group("/api"))
	return handler, r, user, space, token
}

// newHandlerNoAuth wires the handler without AuthRequired so we can set
// context values manually (e.g. to simulate non-members or arbitrary users).
func newHandlerNoAuth(t *testing.T) (*Handler, *gin.Engine, *gorm.DB) {
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	handler := NewHandler(db, cfg, hub)

	r := gin.New()
	vn := r.Group("/api/virtualnet")
	{
		vn.GET("/status", handler.GetStatus)
		vn.POST("/connect", handler.Connect)
		vn.POST("/disconnect", handler.Disconnect)
		vn.GET("/nodes", handler.GetNodes)
	}
	return handler, r, db
}

// setAuthContext sets the gin context values that AuthRequired would populate.
func setAuthContext(c *gin.Context, userID, username, role, spaceID string) {
	c.Set("user_id", userID)
	c.Set("username", username)
	c.Set("email", username+"@example.com")
	c.Set("role", role)
	c.Set("space_id", spaceID)
}

func TestGetStatusNotConfigured(t *testing.T) {
	_, router, user, space, token := newHandlerWithAuth(t)

	// Headscale is not configured → enabled=false, status=disconnected
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/virtualnet/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data in response: %v", resp)
	}
	if enabled, _ := data["enabled"].(bool); enabled {
		t.Errorf("expected enabled=false when headscale not configured")
	}
	if status, _ := data["status"].(string); status != "disconnected" {
		t.Errorf("expected status disconnected, got %q", status)
	}
	// reference user/space to avoid unused warnings in case of refactor
	_ = user
	_ = space
}

func TestGetStatusWithActiveSession(t *testing.T) {
	handler, _, db := newHandlerNoAuth(t)

	// Create a user + space + membership directly in the DB
	user := &model.User{ID: idgen.NextString(), Username: "u", Email: "u@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID, Role: middleware.RoleMember, JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	// Seed an active session
	if err := db.Create(&model.VirtualNetSession{
		ID: idgen.NextString(), SpaceID: space.ID, UserID: user.ID, NodeID: "n-1",
		Status: "connected", IP: "10.0.0.5",
	}).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Re-build router with context injection so we can set the auth context
	// that AuthRequired would normally populate.
	r := gin.New()
	r.GET("/api/virtualnet/status", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.GetStatus(c)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/virtualnet/status", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if status, _ := data["status"].(string); status != "connected" {
		t.Errorf("expected status connected, got %v", data["status"])
	}
	if ip, _ := data["ip"].(string); ip != "10.0.0.5" {
		t.Errorf("expected ip 10.0.0.5, got %v", data["ip"])
	}
}

func TestGetStatusNonMemberForbidden(t *testing.T) {
	handler, _, db := newHandlerNoAuth(t)

	user := &model.User{ID: idgen.NextString(), Username: "outsider", Email: "o@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "other"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	// No membership created → forbidden

	r := gin.New()
	r.GET("/api/virtualnet/status", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.GetStatus(c)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/virtualnet/status", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status %d for non-member, got %d, body: %s",
			http.StatusForbidden, w.Code, w.Body.String())
	}
}

// TestConnectInvalidCIDR removed: EasyTier 模式下不再校验 CIDR（DES-2026-0731-02）。
// 旧的 networkCidr 字段仅为向后兼容保留解析，不再影响请求流程。

func TestConnectAlreadyConnected(t *testing.T) {
	handler, _, db := newHandlerNoAuth(t)

	user := &model.User{ID: idgen.NextString(), Username: "u", Email: "u@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID, Role: middleware.RoleMember, JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	// Seed an active session → "already connected"
	if err := db.Create(&model.VirtualNetSession{
		ID: idgen.NextString(), SpaceID: space.ID, UserID: user.ID, Status: "connected",
	}).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}

	r := gin.New()
	r.POST("/api/virtualnet/connect", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.Connect(c)
	})

	body, _ := json.Marshal(map[string]string{"networkCidr": ""})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/virtualnet/connect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected status %d for already connected, got %d, body: %s",
			http.StatusConflict, w.Code, w.Body.String())
	}
}

func TestConnectEasytierUnavailable(t *testing.T) {
	handler, _, db := newHandlerNoAuth(t)

	user := &model.User{ID: idgen.NextString(), Username: "u", Email: "u@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID, Role: middleware.RoleMember, JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	r := gin.New()
	r.POST("/api/virtualnet/connect", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.Connect(c)
	})

	// Empty body — EasyTier not configured → SERVICE_UNAVAILABLE
	body, _ := json.Marshal(map[string]string{})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/virtualnet/connect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// Service unavailable because EasyTier is not configured (DES-2026-0731-02)
	if !bytes.Contains(w.Body.Bytes(), []byte("VIRTUALNET_SERVICE_UNAVAILABLE")) {
		t.Errorf("expected VIRTUALNET_SERVICE_UNAVAILABLE error, got: %s", w.Body.String())
	}
}

func TestDisconnectNotConnected(t *testing.T) {
	handler, _, db := newHandlerNoAuth(t)

	user := &model.User{ID: idgen.NextString(), Username: "u", Email: "u@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID, Role: middleware.RoleMember, JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	r := gin.New()
	r.POST("/api/virtualnet/disconnect", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.Disconnect(c)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/virtualnet/disconnect", nil)
	r.ServeHTTP(w, req)

	if !bytes.Contains(w.Body.Bytes(), []byte("VIRTUALNET_NOT_CONNECTED")) {
		t.Errorf("expected VIRTUALNET_NOT_CONNECTED error, got: %s", w.Body.String())
	}
}

func TestDisconnectSuccess(t *testing.T) {
	handler, _, db := newHandlerNoAuth(t)

	user := &model.User{ID: idgen.NextString(), Username: "u", Email: "u@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID, Role: middleware.RoleMember, JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	session := &model.VirtualNetSession{
		ID: idgen.NextString(), SpaceID: space.ID, UserID: user.ID, NodeID: "n-1",
		Status: "connected", IP: "10.0.0.5",
	}
	if err := db.Create(session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Seed a node
	if err := db.Create(&model.VirtualNetNode{
		ID: idgen.NextString(), SpaceID: space.ID, SessionID: session.ID,
		Name: "rrt-u", IP: "10.0.0.5", Status: "active",
	}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}

	r := gin.New()
	r.POST("/api/virtualnet/disconnect", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.Disconnect(c)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/virtualnet/disconnect", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}

	// Verify session status was updated
	var updated model.VirtualNetSession
	if err := db.First(&updated, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("find session: %v", err)
	}
	if updated.Status != "disconnected" {
		t.Errorf("expected session status disconnected, got %q", updated.Status)
	}
	// Verify node status was updated
	var node model.VirtualNetNode
	if err := db.Where("session_id = ?", session.ID).First(&node).Error; err != nil {
		t.Fatalf("find node: %v", err)
	}
	if node.Status != "inactive" {
		t.Errorf("expected node status inactive, got %q", node.Status)
	}
}

func TestGetNodesNotConnected(t *testing.T) {
	handler, _, db := newHandlerNoAuth(t)

	user := &model.User{ID: idgen.NextString(), Username: "u", Email: "u@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID, Role: middleware.RoleMember, JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	r := gin.New()
	r.GET("/api/virtualnet/nodes", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.GetNodes(c)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/virtualnet/nodes", nil)
	r.ServeHTTP(w, req)

	if !bytes.Contains(w.Body.Bytes(), []byte("VIRTUALNET_NOT_CONNECTED")) {
		t.Errorf("expected VIRTUALNET_NOT_CONNECTED error, got: %s", w.Body.String())
	}
}

func TestGetNodesReturnsActiveNodes(t *testing.T) {
	handler, _, db := newHandlerNoAuth(t)

	user := &model.User{ID: idgen.NextString(), Username: "u", Email: "u@e.com", PasswordHash: "x", Role: middleware.RoleMember, IsActive: true, DisplayName: "小明"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: user.ID}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: user.ID, SpaceID: space.ID, Role: middleware.RoleMember, JoinedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	session := &model.VirtualNetSession{
		ID: idgen.NextString(), SpaceID: space.ID, UserID: user.ID, Status: "connected", IP: "10.0.0.5",
	}
	if err := db.Create(session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := db.Create(&model.VirtualNetNode{
		ID: idgen.NextString(), SpaceID: space.ID, SessionID: session.ID,
		Name: "rrt-" + user.ID, IP: "10.0.0.5", Status: "active",
	}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}

	r := gin.New()
	r.GET("/api/virtualnet/nodes", func(c *gin.Context) {
		setAuthContext(c, user.ID, user.Username, user.Role, space.ID)
		handler.GetNodes(c)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/virtualnet/nodes", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Errorf("expected 1 node, got %d", len(data))
	}
	node := data[0].(map[string]interface{})
	// 节点名显示 displayName，hostname 保留 EasyTier 节点名（rrt-<userId>）
	if node["name"] != "小明" {
		t.Errorf("expected name=小明 (displayName), got %v", node["name"])
	}
	if node["hostname"] != "rrt-"+user.ID {
		t.Errorf("expected hostname=rrt-%s, got %v", user.ID, node["hostname"])
	}
}

func TestIsEasytierAvailableNotConfigured(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	// EasytierSecret is empty in DefaultConfig — should return false
	h := NewHandler(db, cfg, nil)
	if h.isEasytierAvailable() {
		t.Errorf("expected isEasytierAvailable=false when not configured")
	}
}

func TestEasytierRequestNotConfigured(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.EasytierURL = "" // explicitly empty
	h := NewHandler(db, cfg, nil)
	_, _, err := h.easytierRequest("GET", "/api/v1/health")
	if err == nil {
		t.Errorf("expected error when easytier not configured")
	}
}

func TestRegisterRoutes(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	h := NewHandler(db, cfg, nil)
	r := gin.New()
	h.RegisterRoutes(r.Group("/api"))
	routes := r.Routes()

	expected := map[string]bool{
		"/api/virtualnet/status":    false,
		"/api/virtualnet/connect":   false,
		"/api/virtualnet/disconnect": false,
		"/api/virtualnet/nodes":     false,
		"/api/virtualnet/join":      false, // legacy alias
		"/api/virtualnet/leave":     false, // legacy alias
	}
	for _, r := range routes {
		if _, ok := expected[r.Path]; ok {
			expected[r.Path] = true
		}
	}
	for path, found := range expected {
		if !found {
			t.Errorf("expected route %s to be registered", path)
		}
	}
}
