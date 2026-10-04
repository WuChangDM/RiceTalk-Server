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
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// newScreenshareRouter builds a fresh handler+router for handler-level tests.
func newScreenshareRouter(t *testing.T) (*Handler, *gin.Engine, *model.User, *model.Space, *model.Channel, string, *gorm.DB) {
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
	return handler, router, user, space, channel, token, db
}

// TestHandlerGetChannelStatusErrorPaths covers the GetChannelStatus error branches.
func TestHandlerGetChannelStatusErrorPaths(t *testing.T) {
	t.Run("missing channelId returns 400", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/screenshare/status", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusBadRequest, w.Code, w.Body.String())
		}
	})

	t.Run("non-existent channel returns 404", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/screenshare/status?channelId=nonexistent", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusNotFound, w.Code, w.Body.String())
		}
		if !bytes.Contains(w.Body.Bytes(), []byte("CHANNEL_NOT_FOUND")) {
			t.Errorf("expected CHANNEL_NOT_FOUND error, got: %s", w.Body.String())
		}
	})

	t.Run("user not in space returns 403", func(t *testing.T) {
		_, router, _, _, _, _, db := newScreenshareRouter(t)

		// Create an isolated channel in a different space that no authed user
		// is a member of. The token created by newScreenshareRouter is bound
		// to a different space, so IsUserInSpace should return false.
		otherSpace := &model.Space{ID: idgen.NextString(), Name: "Other", OwnerID: "other"}
		if err := db.Create(otherSpace).Error; err != nil {
			t.Fatalf("create other space: %v", err)
		}
		otherChannel := &model.Channel{
			ID: idgen.NextString(), SpaceID: otherSpace.ID, Name: "Other Voice", Type: "voice",
		}
		if err := db.Create(otherChannel).Error; err != nil {
			t.Fatalf("create other channel: %v", err)
		}

		// Re-issue a token for an isolated user that is NOT a member of otherSpace.
		otherUser := &model.User{
			ID: idgen.NextString(), Username: "isolated", Email: "isolated@example.com",
			PasswordHash: "x", Role: middleware.RoleMember, IsActive: true,
		}
		if err := db.Create(otherUser).Error; err != nil {
			t.Fatalf("create isolated user: %v", err)
		}
		token, _, err := middleware.GenerateTokenPair(otherUser.ID, otherUser.Username, otherUser.Email, middleware.RoleMember, 0, idgen.NextString(), "", testConfig())
		if err != nil {
			t.Fatalf("generate token: %v", err)
		}

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/screenshare/status?channelId="+otherChannel.ID, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusForbidden, w.Code, w.Body.String())
		}
	})
}

// TestHandlerGetSessionStatusErrorPaths covers GetSessionStatus error branches.
func TestHandlerGetSessionStatusErrorPaths(t *testing.T) {
	t.Run("non-existent session returns 404", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/screenshare/sessions/nonexistent", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusNotFound, w.Code, w.Body.String())
		}
	})
}

// TestHandlerGetActiveSessionsErrorPaths covers the getActiveSessions helper.
func TestHandlerGetActiveSessionsErrorPaths(t *testing.T) {
	t.Run("missing spaceId returns 400", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/screenshare/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusBadRequest, w.Code, w.Body.String())
		}
	})

	t.Run("user not in space returns 403", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/screenshare/sessions?spaceId=nonexistent-space", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusForbidden, w.Code, w.Body.String())
		}
	})
}

// TestHandlerStartErrorPaths covers Start handler error branches.
func TestHandlerStartErrorPaths(t *testing.T) {
	t.Run("non-existent channel returns 404", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		body, _ := json.Marshal(map[string]interface{}{
			"channelId": "nonexistent",
			"shareType": "screen",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/start", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Client-Type", "desktop")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusNotFound, w.Code, w.Body.String())
		}
		if !bytes.Contains(w.Body.Bytes(), []byte("CHANNEL_NOT_FOUND")) {
			t.Errorf("expected CHANNEL_NOT_FOUND error, got: %s", w.Body.String())
		}
	})

	t.Run("invalid body returns 400", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/start", bytes.NewReader([]byte("not-json")))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Client-Type", "desktop")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusBadRequest, w.Code, w.Body.String())
		}
	})
}

// TestHandlerStopErrorPaths covers Stop handler error branches.
func TestHandlerStopErrorPaths(t *testing.T) {
	t.Run("missing channelId returns 400", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		body, _ := json.Marshal(map[string]string{})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/stop", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusBadRequest, w.Code, w.Body.String())
		}
	})

	t.Run("no active session returns 404", func(t *testing.T) {
		_, router, _, _, channel, token, _ := newScreenshareRouter(t)

		body, _ := json.Marshal(map[string]string{"channelId": channel.ID})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/stop", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusNotFound, w.Code, w.Body.String())
		}
	})
}

// TestHandlerPauseResume covers the Pause and Resume handlers end-to-end.
func TestHandlerPauseResume(t *testing.T) {
	_, router, user, space, channel, token, _ := newScreenshareRouter(t)

	// Start a share first.
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

	// Pause.
	pauseBody, _ := json.Marshal(map[string]string{"channelId": channel.ID})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/screenshare/pause", bytes.NewReader(pauseBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected pause status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}

	// Resume.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/screenshare/resume", bytes.NewReader(pauseBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected resume status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
	_ = user
	_ = space
}

// TestHandlerPauseErrorPaths covers Pause handler error branches.
func TestHandlerPauseErrorPaths(t *testing.T) {
	t.Run("missing channelId returns 400", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		body, _ := json.Marshal(map[string]string{})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/pause", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusBadRequest, w.Code, w.Body.String())
		}
	})

	t.Run("no active session returns 404", func(t *testing.T) {
		_, router, _, _, channel, token, _ := newScreenshareRouter(t)

		body, _ := json.Marshal(map[string]string{"channelId": channel.ID})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/pause", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusNotFound, w.Code, w.Body.String())
		}
	})
}

// TestHandlerForceStopErrorPaths covers ForceStop handler error branches.
func TestHandlerForceStopErrorPaths(t *testing.T) {
	t.Run("non-admin member cannot force-stop", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		log := testutil.TestLogger()
		hub := realtime.NewHub(log)
		handler := NewHandler(db, cfg, hub)

		sharer, space, channel, sharerToken := setupTestSpace(t, db)

		router := gin.New()
		router.Use(middleware.AuthRequired(cfg, db))
		handler.RegisterRoutes(router.Group("/api"))

		// Start a share as the sharer.
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

		// Create another member (not admin) of the same space.
		otherPasswordHash, _ := crypto.HashPassword("Password123!")
		otherUser := &model.User{
			ID: idgen.NextString(), Username: "other", Email: "other@example.com",
			PasswordHash: otherPasswordHash, Role: middleware.RoleMember, IsActive: true,
		}
		if err := db.Create(otherUser).Error; err != nil {
			t.Fatalf("create other user: %v", err)
		}
		if err := db.Create(&model.Membership{
			ID: idgen.NextString(), UserID: otherUser.ID, SpaceID: space.ID,
			Role: middleware.RoleMember, JoinedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("create other membership: %v", err)
		}
		otherToken, _, err := middleware.GenerateTokenPair(otherUser.ID, otherUser.Username, otherUser.Email, middleware.RoleMember, 0, space.ID, "", cfg)
		if err != nil {
			t.Fatalf("generate other token: %v", err)
		}

		// Force-stop as non-admin member → forbidden.
		w = httptest.NewRecorder()
		req, _ = http.NewRequest("POST", "/api/screenshare/"+sessionID+"/kick", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+otherToken)
		router.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusForbidden, w.Code, w.Body.String())
		}
		if !bytes.Contains(w.Body.Bytes(), []byte("SCREENSHARE_FORCE_STOP_FORBIDDEN")) {
			t.Errorf("expected SCREENSHARE_FORCE_STOP_FORBIDDEN error, got: %s", w.Body.String())
		}
		_ = sharer
	})

	t.Run("non-existent session returns 404", func(t *testing.T) {
		_, router, _, _, _, token, _ := newScreenshareRouter(t)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/screenshare/nonexistent-session/kick", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusNotFound, w.Code, w.Body.String())
		}
	})
}

// TestHandlerGetChannelStatusInactive covers the success path when no active
// session exists on a channel.
func TestHandlerGetChannelStatusInactive(t *testing.T) {
	_, router, _, _, channel, token, _ := newScreenshareRouter(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/screenshare/status?channelId="+channel.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if active, _ := data["active"].(bool); active {
		t.Errorf("expected active=false for channel without active session, got %v", data["active"])
	}
}
