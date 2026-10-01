package admin

import (
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
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// setupAdminHandlerWithHub is like setupAdminHandler but wires a real realtime
// Hub (with Run() started in a goroutine) so presence-backed endpoints can be
// exercised end-to-end. The returned hub is already running; the caller may
// register test users via hub.RegisterTestUser.
func setupAdminHandlerWithHub(t *testing.T) (*Handler, *gin.Engine, *model.User, string, *realtime.Hub) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{
		ID: idgen.NextString(), Username: "owner", Email: "owner@example.com",
		PasswordHash: "x", Role: middleware.RoleOwner, IsActive: true,
	}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	token, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	hub := realtime.NewHub(testutil.TestLogger())
	go hub.Run()
	// Allow Run() to start consuming the register channel before we register
	// any test users.
	time.Sleep(50 * time.Millisecond)

	handler := NewHandler(db, cfg, hub, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))
	return handler, router, owner, token, hub
}

// TestHandlerGetRuntimeBusinessMetrics verifies the 4 business-metric fields
// (§6 P2) are present and non-zero when there is an online user, an active
// voice room, and a recent message. This covers SubTask 5.6.
func TestHandlerGetRuntimeBusinessMetrics(t *testing.T) {
	handler, router, owner, token, hub := setupAdminHandlerWithHub(t)

	// Register the owner as an online WebSocket user via the test helper.
	hub.RegisterTestUser(owner.ID)
	// Wait for the Hub event loop to process the registration so that
	// OnlineUserIDs() and ClientCount() reflect the new connection.
	time.Sleep(100 * time.Millisecond)

	// Seed an active voice room + participant → active_voices >= 1.
	room := &model.VoiceRoom{
		ID: idgen.NextString(), SpaceID: "space-1", LiveKitRoom: "lk-test",
	}
	if err := handler.db.Create(room).Error; err != nil {
		t.Fatalf("create voice room: %v", err)
	}
	participant := &model.VoiceParticipant{
		ID: idgen.NextString(), RoomID: room.ID, UserID: owner.ID,
		JoinedAt: time.Now(), LastActiveAt: time.Now(),
	}
	if err := handler.db.Create(participant).Error; err != nil {
		t.Fatalf("create voice participant: %v", err)
	}

	// Seed a recent message → message_rate >= 1.
	msg := &model.Message{
		ID: idgen.NextString(), ChannelID: "ch-1", UserID: owner.ID,
		Content: "hello", Type: "text",
	}
	if err := handler.db.Create(msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Force the runtime stats cache to be cold so the test sees fresh data.
	handler.service.runtimeStatsMu.Lock()
	handler.service.runtimeStatsAt = time.Time{}
	handler.service.runtimeStatsMu.Unlock()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/runtime", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data object, got: %v", resp)
	}

	// All 4 business-metric fields must be present and non-null.
	cases := []struct {
		field string
		want  float64
	}{
		{"online_users", 1},
		{"active_voices", 1},
		{"message_rate", 1},
		{"websocket_conns", 1},
	}
	for _, c := range cases {
		raw, ok := data[c.field]
		if !ok {
			t.Errorf("expected field %q in runtime response, missing", c.field)
			continue
		}
		val, ok := toFloat64(raw)
		if !ok {
			t.Errorf("field %q is not numeric: %T(%v)", c.field, raw, raw)
			continue
		}
		if val < c.want {
			t.Errorf("field %q = %v, want >= %v", c.field, val, c.want)
		}
	}
}

// TestHandlerGetRuntimeBusinessMetricsNilHub verifies the 4 fields are still
// present (with zero values) when the Hub is unavailable — the previous
// behavior before §6 P2. This guards against regressions in tests that pass a
// nil Hub.
func TestHandlerGetRuntimeBusinessMetricsNilHub(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/runtime", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data object")
	}
	// Fields must exist even when the Hub is nil (so the admin frontend never
	// sees a missing field).
	for _, field := range []string{"online_users", "active_voices", "message_rate", "websocket_conns"} {
		if _, ok := data[field]; !ok {
			t.Errorf("expected field %q present even with nil hub", field)
		}
	}
}

// TestHandlerGetUsersOnlineStatus verifies that the user-management list
// (§7 P2) reflects the real-time presence source: the currently-logged-in
// owner shows Online=true while a non-connected user shows Online=false.
// This covers SubTask 6.5.
func TestHandlerGetUsersOnlineStatus(t *testing.T) {
	handler, router, owner, token, hub := setupAdminHandlerWithHub(t)

	// Create a second user who is NOT registered with the Hub → should be
	// reported as offline.
	offline := &model.User{
		ID: idgen.NextString(), Username: "offline-user", Email: "off@example.com",
		PasswordHash: "x", Role: middleware.RoleMember, IsActive: true,
	}
	if err := handler.db.Create(offline).Error; err != nil {
		t.Fatalf("create offline user: %v", err)
	}

	// Register the owner as online.
	hub.RegisterTestUser(owner.ID)
	time.Sleep(100 * time.Millisecond)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/users?page=1&pageSize=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data object")
	}
	items, ok := data["items"].([]interface{})
	if !ok {
		t.Fatalf("expected items array, got: %T", data["items"])
	}

	// Build a userID → online map from the response.
	onlineByID := map[string]bool{}
	for _, raw := range items {
		item, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("expected item object, got: %T", raw)
		}
		uid, _ := item["id"].(string)
		online, _ := item["online"].(bool)
		onlineByID[uid] = online
	}

	// The owner (registered with the Hub) must be Online.
	if !onlineByID[owner.ID] {
		t.Errorf("expected owner (id=%s) to be Online=true, got false", owner.ID)
	}
	// The offline user must NOT be Online.
	if onlineByID[offline.ID] {
		t.Errorf("expected offline user (id=%s) to be Online=false, got true", offline.ID)
	}
}

// TestHandlerGetUsersOnlineStatusNilHub verifies that when no Hub is wired in,
// all users report Online=false. This matches the pre-fix behavior and ensures
// the field is always present in the response.
func TestHandlerGetUsersOnlineStatusNilHub(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/users?page=1&pageSize=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	items, ok := data["items"].([]interface{})
	if !ok {
		t.Fatalf("expected items array")
	}
	for _, raw := range items {
		item, _ := raw.(map[string]interface{})
		if _, ok := item["online"]; !ok {
			t.Errorf("expected online field present even with nil hub, item: %v", item)
		}
		if online, _ := item["online"].(bool); online {
			t.Errorf("expected Online=false for all users when hub is nil")
		}
	}
}

// toFloat64 converts a JSON-decoded numeric value to float64. JSON numbers
// decode to float64 by default when unmarshaling into interface{}.
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
