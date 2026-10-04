package bots

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// isGinRoute404 reports whether the recorder captured gin's default
// "404 page not found" response (route not registered), as opposed to a JSON
// error envelope that happens to use HTTP 404 (e.g. errors.ErrNotFound).
func isGinRoute404(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "404 page not found")
}

// setupBotsTestRouter builds a gin engine with the bots routes registered and
// returns the handler so tests can drive the in-memory DB-backed service. It
// seeds a user + space + membership + bot whose OutputRoomID equals channelID.
func setupBotsTestRouter(t *testing.T) (router *gin.Engine, db *gorm.DB, userID, botID, channelID, accessToken string) {
	gin.SetMode(gin.TestMode)
	db = testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	uid, spaceID := createUserSpaceMembership(t, db)
	channelID = idgen.NextString()
	botID = idgen.NextString()
	now := time.Now()
	bot := &model.Bot{
		ID:           botID,
		SpaceID:      spaceID,
		OutputRoomID: &channelID,
		Name:         "TestBot",
		Token:        idgen.NextString(),
		CreatedBy:    uid,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(bot).Error; err != nil {
		t.Fatalf("create bot: %v", err)
	}

	var u model.User
	if err := db.First(&u, "id = ?", uid).Error; err != nil {
		t.Fatalf("fetch user: %v", err)
	}

	token, _, err := middleware.GenerateTokenPair(u.ID, u.Username, u.Email, u.Role, u.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	accessToken = token

	handler := NewHandler(db, cfg, nil)
	router = gin.New()
	handler.RegisterRoutes(router.Group("/api/v1"))
	return router, db, u.ID, botID, channelID, accessToken
}

// doPOST issues a JSON POST request with the given bearer token and body.
func doPOST(router *gin.Engine, path, token string, body interface{}) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

// TestChannelBasedAliases verifies that the 8 channel-based control aliases
// (skip/pause/resume/seek/playnow/mode/volume/tts) resolve channelId from the
// body and do NOT return 404 (SOLUTIONS.md §3 P1). The frontend api-core.ts
// calls these paths without :botId.
func TestChannelBasedAliases(t *testing.T) {
	router, _, _, _, channelID, token := setupBotsTestRouter(t)

	tests := []struct {
		name string
		path string
		body map[string]interface{}
		// expect200 asserts the service call succeeds and we expect 200.
		// seek returns 500 in nocgo builds (BotPlayer.Seek → errNoCGO) and
		// playnow returns 404 (ErrNotFound) for a missing queueId; tts returns
		// 403/500 (consent + external edge-tts). For these we only assert the
		// route matched (non-gin-404), proving the alias is registered.
		expect200 bool
	}{
		{"skip", "/api/v1/bots/skip", map[string]interface{}{"channelId": channelID}, true},
		{"pause", "/api/v1/bots/pause", map[string]interface{}{"channelId": channelID}, true},
		{"resume", "/api/v1/bots/resume", map[string]interface{}{"channelId": channelID}, true},
		{"seek", "/api/v1/bots/seek", map[string]interface{}{"channelId": channelID, "time": 10}, false},
		{"playnow", "/api/v1/bots/playnow", map[string]interface{}{"channelId": channelID, "queueId": "q_missing"}, false},
		{"mode", "/api/v1/bots/mode", map[string]interface{}{"channelId": channelID, "mode": "order"}, true},
		{"volume", "/api/v1/bots/volume", map[string]interface{}{"channelId": channelID, "volume": 50}, true},
		{"tts", "/api/v1/bots/tts", map[string]interface{}{"channelId": channelID, "text": "hello"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doPOST(router, tt.path, token, tt.body)
			if isGinRoute404(w) {
				t.Fatalf("POST %s returned gin 404 — channel-based alias not registered; body=%s", tt.path, w.Body.String())
			}
			if tt.expect200 && w.Code != http.StatusOK {
				t.Errorf("POST %s: expected 200, got %d: %s", tt.path, w.Code, w.Body.String())
			}
		})
	}
}

// TestChannelBasedAliasMissingChannelId verifies the alias returns a 400 (not
// gin's 404) when channelId is absent, confirming the route is registered and
// the helper reports the missing field rather than gin's 404 handler.
func TestChannelBasedAliasMissingChannelId(t *testing.T) {
	router, _, _, _, _, token := setupBotsTestRouter(t)
	w := doPOST(router, "/api/v1/bots/skip", token, map[string]interface{}{})
	if isGinRoute404(w) {
		t.Fatalf("POST /api/v1/bots/skip returned gin 404 — route not registered")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing channelId, got %d: %s", w.Code, w.Body.String())
	}
}

// TestChannelBasedAliasUnknownChannelId verifies the alias returns a JSON
// ErrNotFound (not gin's route 404) when the channelId does not map to any bot.
func TestChannelBasedAliasUnknownChannelId(t *testing.T) {
	router, _, _, _, _, token := setupBotsTestRouter(t)
	w := doPOST(router, "/api/v1/bots/skip", token, map[string]interface{}{"channelId": "ch_does_not_exist"})
	if isGinRoute404(w) {
		t.Fatalf("POST /api/v1/bots/skip returned gin 404 — route not registered")
	}
	// FindBotByChannelID returns ErrNotFound (404 JSON) for an unknown channel.
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 (ErrNotFound) for unknown channelId, got %d: %s", w.Code, w.Body.String())
	}
}

// TestModuleEnabledMiddlewareDisabled verifies that when the bots module is
// disabled in module_runtime_status, every /api/v1/bots/* route returns 503
// "bots module disabled" (SOLUTIONS.md §4 P2).
func TestModuleEnabledMiddlewareDisabled(t *testing.T) {
	router, db, _, botID, _, token := setupBotsTestRouter(t)

	// Disable the bots module. Note: ModuleRuntimeStatus.Enabled has
	// gorm:"default:true", so GORM treats the false zero value as "unset" on
	// insert and overrides it with the default true — db.Create(&mod{Enabled:
	// false}) would actually store true. We work around this by creating the
	// row with Enabled=true first, then flipping it to false via a map-based
	// Update (which bypasses the zero-value default logic).
	mod := &model.ModuleRuntimeStatus{
		ID:         idgen.NextString(),
		ModuleName: "bots",
		Enabled:    true,
	}
	if err := db.Create(mod).Error; err != nil {
		t.Fatalf("create module: %v", err)
	}
	if err := db.Model(&model.ModuleRuntimeStatus{}).
		Where("module_name = ?", "bots").
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable module: %v", err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/bots/"+botID+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when bots module disabled, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode 503 body: %v", err)
	}
	if msg, _ := resp["error"].(string); msg != "bots module disabled" {
		t.Errorf("expected error 'bots module disabled', got %q", msg)
	}
}

// TestModuleEnabledMiddlewareEnabled verifies that when the bots module is
// enabled (or absent — defaults to enabled), the routes are reachable and
// return 200 for valid requests.
func TestModuleEnabledMiddlewareEnabled(t *testing.T) {
	router, db, _, botID, _, token := setupBotsTestRouter(t)

	// Explicitly enable the bots module.
	mod := &model.ModuleRuntimeStatus{
		ID:         idgen.NextString(),
		ModuleName: "bots",
		Enabled:    true,
	}
	if err := db.Create(mod).Error; err != nil {
		t.Fatalf("create enabled module: %v", err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/bots/"+botID+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when bots module enabled, got %d: %s", w.Code, w.Body.String())
	}
}

// TestModuleEnabledMiddlewareDefaultsEnabled verifies that with no
// module_runtime_status record, the bots routes are reachable (module defaults
// to enabled so fresh installs are not locked out).
func TestModuleEnabledMiddlewareDefaultsEnabled(t *testing.T) {
	router, _, _, botID, _, token := setupBotsTestRouter(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/bots/"+botID+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when no module record (defaults enabled), got %d: %s", w.Code, w.Body.String())
	}
}
