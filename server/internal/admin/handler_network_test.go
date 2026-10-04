package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/serverstate"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func newTestStateManager(t *testing.T, cfg *config.Config) *serverstate.Manager {
	tmp := t.TempDir()
	cfg.LocalDataPath = tmp
	mgr, err := serverstate.NewManager(cfg)
	if err != nil {
		t.Fatalf("new state manager: %v", err)
	}
	return mgr
}

func TestHandlerGetNetwork(t *testing.T) {
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
		WebVoiceEnabled:        true,
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

	handler := NewHandlerWithState(db, cfg, nil, middleware.NewOwnerBreakGlassProtector(), stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/network", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["code"] != "OK" {
		t.Fatalf("expected code OK, got %v", resp["code"])
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object, got %T", resp["data"])
	}
	net, ok := data["network"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected network object, got %T", data["network"])
	}
	if net["externalHost"] != "voice.example.com" {
		t.Errorf("externalHost = %v, want voice.example.com", net["externalHost"])
	}
	if len(data["suggestedSrvRecords"].([]interface{})) != 3 {
		t.Errorf("expected 3 SRV records, got %d", len(data["suggestedSrvRecords"].([]interface{})))
	}
}

func TestHandlerUpdateNetwork(t *testing.T) {
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
		ExternalHost:           "old.example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandlerWithState(db, cfg, nil, middleware.NewOwnerBreakGlassProtector(), stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	body, _ := json.Marshal(serverstate.NetworkConfig{
		ExternalHost:           "new.example.com",
		ExternalHTTPPort:       8443,
		ExternalLiveKitWSPort:  8443,
		ExternalMediaUDPPort:   7882,
		UseHTTPS:               true,
		WebVoiceEnabled:        true,
		ClientAccessEnabled:    true,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/network", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	if stateMgr.Network().ExternalHost != "new.example.com" {
		t.Errorf("externalHost = %v, want new.example.com", stateMgr.Network().ExternalHost)
	}
	if stateMgr.Network().ExternalHTTPPort != 8443 {
		t.Errorf("externalHttpPort = %d, want 8443", stateMgr.Network().ExternalHTTPPort)
	}
}

func TestHandlerDetectNetwork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{ID: idgen.NextString(), Username: "owner", Email: "owner@example.com", PasswordHash: "x", Role: middleware.RoleOwner}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/network/detect", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected result object, got %T", resp["result"])
	}
	if _, ok := result["localIpv4s"]; !ok {
		t.Errorf("expected localIpv4s in detection result")
	}
}

func TestHandlerVerifyNetwork(t *testing.T) {
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
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandlerWithState(db, cfg, nil, middleware.NewOwnerBreakGlassProtector(), stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	body, _ := json.Marshal(map[string]interface{}{
		"network": map[string]interface{}{
			"externalHost":          "check.example.com",
			"externalHttpPort":      8443,
			"externalLiveKitWsPort": 8443,
			"externalMediaUdpPort":  7882,
			"useHttps":              true,
		},
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/network/verify", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected result object, got %T", resp["result"])
	}
	if result["externalHost"] != "check.example.com" {
		t.Errorf("externalHost = %v, want check.example.com", result["externalHost"])
	}
}

// TestHandlerGetNetworkNoStateManager covers the error branch where the
// handler was created without a serverstate.Manager (NewHandler).
func TestHandlerGetNetworkNoStateManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{ID: idgen.NextString(), Username: "owner", Email: "owner@example.com", PasswordHash: "x", Role: middleware.RoleOwner}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	// NewHandler creates a handler with stateMgr == nil.
	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/network", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d when stateMgr is nil, got %d: %s",
			http.StatusInternalServerError, w.Code, w.Body.String())
	}
}

// TestHandlerUpdateNetworkNoStateManager covers the error branch where the
// handler was created without a serverstate.Manager (NewHandler).
func TestHandlerUpdateNetworkNoStateManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{ID: idgen.NextString(), Username: "owner", Email: "owner@example.com", PasswordHash: "x", Role: middleware.RoleOwner}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	body, _ := json.Marshal(serverstate.NetworkConfig{
		ExternalHost:           "new.example.com",
		ExternalHTTPPort:       8443,
		ExternalLiveKitWSPort:  8443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/network", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d when stateMgr is nil, got %d: %s",
			http.StatusInternalServerError, w.Code, w.Body.String())
	}
}

// TestHandlerUpdateNetworkInvalidBody covers the bad-request branch of
// UpdateNetwork when the JSON body is malformed.
func TestHandlerUpdateNetworkInvalidBody(t *testing.T) {
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
		ExternalHost:           "old.example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandlerWithState(db, cfg, nil, middleware.NewOwnerBreakGlassProtector(), stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/network", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid body, got %d: %s",
			http.StatusBadRequest, w.Code, w.Body.String())
	}
}
