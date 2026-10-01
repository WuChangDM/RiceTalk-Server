package screenshare

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func testConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-key-for-testing"
	return cfg
}

// setupTestSpace creates a user, space, membership and a channel for screenshare tests.
func setupTestSpace(t *testing.T, db *gorm.DB) (*model.User, *model.Space, *model.Channel, string) {
	passwordHash, _ := crypto.HashPassword("Password123!")
	user := &model.User{
		ID:           idgen.NextString(),
		Username:     "sharer",
		Email:        "sharer@example.com",
		PasswordHash: passwordHash,
		Role:         middleware.RoleMember,
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	space := &model.Space{
		ID:      idgen.NextString(),
		Name:    "Test Space",
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

	channel := &model.Channel{
		ID:      idgen.NextString(),
		SpaceID: space.ID,
		Name:    "Voice Channel",
		Type:    "voice",
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	accessToken, _, err := middleware.GenerateTokenPair(user.ID, user.Username, user.Email, user.Role, 0, space.ID, "", testConfig())
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	return user, space, channel, accessToken
}

func TestHandlerStartScreenShareV2(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	handler := NewHandler(db, cfg, hub)

	_, _, channel, token := setupTestSpace(t, db)

	router := gin.New()
	router.Use(middleware.AuthRequired(cfg, db))
	handler.RegisterRoutes(router.Group("/api"))

	t.Run("desktop client can start screen share", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"channelId":     channel.ID,
			"shareType":     "window",
			"sourceId":      "win-123",
			"resolution":    "1920x1080",
			"frameRate":     30,
			"maxBitrate":    4000,
			"shareAudio":    true,
			"suppressVoice": false,
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/start", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Client-Type", "desktop")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		data, _ := resp["data"].(map[string]interface{})
		if _, ok := data["sessionId"]; !ok {
			t.Errorf("expected sessionId in response, got %v", data)
		}
	})

	t.Run("web client cannot start screen share", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"channelId": channel.ID,
			"shareType": "screen",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/start", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Client-Type", "web")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusForbidden, w.Code, w.Body.String())
		}
		if !bytes.Contains(w.Body.Bytes(), []byte("SCREENSHARE_WEB_FORBIDDEN")) {
			t.Errorf("expected SCREENSHARE_WEB_FORBIDDEN error, got: %s", w.Body.String())
		}
	})

	t.Run("start requires channelId", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{"shareType": "screen"})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/start", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Client-Type", "desktop")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusBadRequest, w.Code, w.Body.String())
		}
	})
}

func TestHandlerStopAndStatusScreenShareV2(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	handler := NewHandler(db, cfg, hub)

	user, space, channel, token := setupTestSpace(t, db)

	router := gin.New()
	router.Use(middleware.AuthRequired(cfg, db))
	handler.RegisterRoutes(router.Group("/api"))

	// Start a share
	startBody, _ := json.Marshal(map[string]interface{}{
		"channelId": channel.ID,
		"shareType": "screen",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/screenshare/start", bytes.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Client-Type", "desktop")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start screenshare failed: %d %s", w.Code, w.Body.String())
	}
	var startResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("unmarshal start response: %v", err)
	}
	startData, _ := startResp["data"].(map[string]interface{})
	sessionID, _ := startData["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("expected sessionId in start response, got %v", startData)
	}

	// Verify status by channel ID
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/screenshare/status?channelId="+channel.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var statusResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	data, _ := statusResp["data"].(map[string]interface{})
	if active, _ := data["active"].(bool); !active {
		t.Errorf("expected screenshare active, got %v", data)
	}
	if userID, _ := data["userId"].(string); userID != user.ID {
		t.Errorf("expected userId %s, got %s", user.ID, userID)
	}

	// Verify active sessions list
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/screenshare/sessions?spaceId="+space.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var listResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	listData, _ := listResp["data"].(map[string]interface{})
	sessions, _ := listData["sessions"].([]interface{})
	if len(sessions) != 1 {
		t.Errorf("expected 1 active session, got %d", len(sessions))
	}

	// Stop the share
	stopBody, _ := json.Marshal(map[string]string{"channelId": channel.ID})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/screenshare/stop", bytes.NewReader(stopBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerForceStopScreenShareV2(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	handler := NewHandler(db, cfg, hub)

	_, space, channel, sharerToken := setupTestSpace(t, db)

	// Create a space admin user
	adminPasswordHash, _ := crypto.HashPassword("Password123!")
	adminUser := &model.User{
		ID:           idgen.NextString(),
		Username:     "spaceadmin",
		Email:        "admin@example.com",
		PasswordHash: adminPasswordHash,
		Role:         middleware.RoleMember,
		IsActive:     true,
	}
	if err := db.Create(adminUser).Error; err != nil {
		t.Fatalf("create admin user: %v", err)
	}
	adminMembership := &model.Membership{
		ID:       idgen.NextString(),
		UserID:   adminUser.ID,
		SpaceID:  space.ID,
		Role:     middleware.RoleAdmin,
		JoinedAt: time.Now().UTC(),
	}
	if err := db.Create(adminMembership).Error; err != nil {
		t.Fatalf("create admin membership: %v", err)
	}
	adminToken, _, err := middleware.GenerateTokenPair(adminUser.ID, adminUser.Username, adminUser.Email, middleware.RoleMember, 0, space.ID, "", cfg)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}

	router := gin.New()
	router.Use(middleware.AuthRequired(cfg, db))
	handler.RegisterRoutes(router.Group("/api"))

	// Start a share as the regular user
	startBody, _ := json.Marshal(map[string]interface{}{
		"channelId": channel.ID,
		"shareType": "screen",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/screenshare/start", bytes.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sharerToken)
	req.Header.Set("X-Client-Type", "desktop")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start screenshare failed: %d %s", w.Code, w.Body.String())
	}
	var startResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("unmarshal start response: %v", err)
	}
	startData, _ := startResp["data"].(map[string]interface{})
	sessionID, _ := startData["sessionId"].(string)

	// Force-stop as space admin via /:id/kick
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/screenshare/"+sessionID+"/kick", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
}
