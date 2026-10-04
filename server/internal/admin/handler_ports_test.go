package admin

import (
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

// setupPortsTestHandler builds a Handler backed by an in-memory SQLite DB, a
// bootstrapped serverstate.Manager and a test owner user. It returns the
// handler, a gin engine with admin routes registered under /api, and a valid
// owner bearer token. (admin-port-config-panel Task 5)
func setupPortsTestHandler(t *testing.T, network serverstate.NetworkConfig) (*Handler, *gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{
		ID:           idgen.NextString(),
		Username:     "owner",
		Email:        "owner@example.com",
		PasswordHash: "x",
		Role:         middleware.RoleOwner,
	}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	stateMgr := newTestStateManager(t, cfg)
	if err := stateMgr.CompleteBootstrap(network); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(
		owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg,
	)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandlerWithState(db, cfg, nil, middleware.NewOwnerBreakGlassProtector(), stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))
	return handler, router, accessToken
}

// defaultPortsNetwork returns a NetworkConfig that satisfies validateNetwork
// for the ports tests. (admin-port-config-panel Task 5)
func defaultPortsNetwork() serverstate.NetworkConfig {
	return serverstate.NetworkConfig{
		ExternalHost:           "voice.example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
		UseHTTPS:               true,
		WebVoiceEnabled:        true,
		ClientAccessEnabled:    true,
	}
}

// portsResponse unmarshals the /api/admin/ports response and returns the
// "ports" array. It fails the test if the response is malformed.
func portsResponse(t *testing.T, w *httptest.ResponseRecorder) []interface{} {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["code"] != "OK" {
		t.Fatalf("code = %v, want OK", resp["code"])
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object, got %T", resp["data"])
	}
	ports, ok := data["ports"].([]interface{})
	if !ok {
		t.Fatalf("expected ports array, got %T", data["ports"])
	}
	return ports
}

// TestGetPorts_ReturnsSixPorts verifies the endpoint returns exactly 6 ports.
func TestGetPorts_ReturnsSixPorts(t *testing.T) {
	_, router, token := setupPortsTestHandler(t, defaultPortsNetwork())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/ports", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	ports := portsResponse(t, w)
	if len(ports) != 6 {
		t.Fatalf("expected 6 ports, got %d", len(ports))
	}
}

// TestGetPorts_FieldsAndProtocols verifies each port carries every required
// field, the protocol annotations are correct, and NatRequired is set only for
// the LiveKit media ports (lkTcp, lkUdp).
func TestGetPorts_FieldsAndProtocols(t *testing.T) {
	_, router, token := setupPortsTestHandler(t, defaultPortsNetwork())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/ports", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	ports := portsResponse(t, w)

	expectedProtocols := map[string]string{
		"api":   "tcp",
		"admin": "tcp",
		"lkWs":  "tcp",
		"lkTcp": "tcp",
		"lkUdp": "udp",
		"vpn":   "udp",
	}
	expectedNat := map[string]bool{
		"api":   false,
		"admin": false,
		"lkWs":  false,
		"lkTcp": true,
		"lkUdp": true,
		"vpn":   false,
	}

	seen := map[string]map[string]interface{}{}
	for _, p := range ports {
		pm, ok := p.(map[string]interface{})
		if !ok {
			t.Fatalf("expected port object, got %T", p)
		}
		key, _ := pm["key"].(string)
		for _, f := range []string{
			"key", "label", "protocol", "internalPort",
			"externalPort", "external", "description", "natRequired",
		} {
			if _, ok := pm[f]; !ok {
				t.Errorf("port %q missing field %q", key, f)
			}
		}
		seen[key] = pm
	}

	for key, proto := range expectedProtocols {
		pm, ok := seen[key]
		if !ok {
			t.Errorf("missing port with key %q", key)
			continue
		}
		if pm["protocol"] != proto {
			t.Errorf("port %q protocol = %v, want %q", key, pm["protocol"], proto)
		}
	}
	for key, nat := range expectedNat {
		pm, ok := seen[key]
		if !ok {
			continue
		}
		if pm["natRequired"] != nat {
			t.Errorf("port %q natRequired = %v, want %v", key, pm["natRequired"], nat)
		}
	}
}

// TestGetPorts_DefaultValueFallback verifies the 0-value fallback added in
// Service.GetConfig is reflected in the response: a persisted AdminConfig row
// with LiveKitTCPPort=0/LiveKitUDPPort=0 surfaces internalPort 7881/7882.
func TestGetPorts_DefaultValueFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{
		ID:           idgen.NextString(),
		Username:     "owner",
		Email:        "owner@example.com",
		PasswordHash: "x",
		Role:         middleware.RoleOwner,
	}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	// Persist an AdminConfig row, then force the TCP/UDP ports to 0 to
	// simulate a pre-migration row (the gorm "default" tag would otherwise
	// backfill 7881/7882 on INSERT).
	ac := model.AdminConfig{
		ID:           idgen.NextString(),
		ServerName:   "test",
		APIPort:      8080,
		AdminPort:    9090,
		LiveKitPort:  7880,
		VPNPort:      41641,
		MaxStorageGB: 10,
	}
	if err := db.Create(&ac).Error; err != nil {
		t.Fatalf("create admin config: %v", err)
	}
	if err := db.Model(&ac).Updates(map[string]interface{}{
		"live_kit_tcp_port": 0,
		"live_kit_udp_port": 0,
	}).Error; err != nil {
		t.Fatalf("reset livekit tcp/udp ports to 0: %v", err)
	}

	stateMgr := newTestStateManager(t, cfg)
	if err := stateMgr.CompleteBootstrap(defaultPortsNetwork()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(
		owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg,
	)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandlerWithState(db, cfg, nil, middleware.NewOwnerBreakGlassProtector(), stateMgr)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/ports", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(w, req)

	ports := portsResponse(t, w)
	for _, p := range ports {
		pm, _ := p.(map[string]interface{})
		key, _ := pm["key"].(string)
		if key == "lkTcp" {
			v, ok := pm["internalPort"].(float64)
			if !ok || int(v) != 7881 {
				t.Errorf("lkTcp internalPort = %v, want 7881 (default fallback)", pm["internalPort"])
			}
		}
		if key == "lkUdp" {
			v, ok := pm["internalPort"].(float64)
			if !ok || int(v) != 7882 {
				t.Errorf("lkUdp internalPort = %v, want 7882 (default fallback)", pm["internalPort"])
			}
		}
	}
}

// TestGetPorts_NoStateManager verifies the error branch when the handler was
// constructed without a serverstate.Manager (NewHandler).
func TestGetPorts_NoStateManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{
		ID:           idgen.NextString(),
		Username:     "owner",
		Email:        "owner@example.com",
		PasswordHash: "x",
		Role:         middleware.RoleOwner,
	}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(
		owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg,
	)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	// NewHandler creates a handler with stateMgr == nil.
	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/ports", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d when stateMgr is nil, got %d: %s",
			http.StatusInternalServerError, w.Code, w.Body.String())
	}
}
