package channel

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
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func setupAuthContext(c *gin.Context, userID, username, role string) {
	c.Set("user_id", userID)
	c.Set("username", username)
	c.Set("email", username+"@example.com")
	c.Set("role", role)
}

func TestHandlerGetChannel(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		hub := realtime.NewHub(testutil.TestLogger())
		handler := NewHandler(db, cfg, hub)

		space := createTestSpace(db)
		ch := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "general", Type: "text", Position: 0}
		_ = db.Create(ch)

		router := gin.New()
		router.GET("/channels/:id", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.GetChannel)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/channels/"+ch.ID, nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp["code"] != "OK" {
			t.Errorf("expected code OK, got %v", resp["code"])
		}
		data, ok := resp["data"].(map[string]interface{})
		if !ok {
			t.Fatal("expected data object")
		}
		if data["id"] != ch.ID {
			t.Errorf("expected channel ID %s, got %v", ch.ID, data["id"])
		}
	})

	t.Run("not found", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		hub := realtime.NewHub(testutil.TestLogger())
		handler := NewHandler(db, cfg, hub)

		router := gin.New()
		router.GET("/channels/:id", func(c *gin.Context) {
			setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
			c.Next()
		}, handler.GetChannel)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/channels/999999999", nil)
		router.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			t.Errorf("expected non-OK status, got %d, body: %s", w.Code, w.Body.String())
		}
	})
}

// P0-3 回归：UpdateChannel 此前根本没有 audioQuality 字段，选择器静默无效。
// 合法值必须落库到 Channel.VoiceQuality。
func TestHandlerUpdateChannelAudioQuality(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		hub := realtime.NewHub(testutil.TestLogger())
		handler := NewHandler(db, cfg, hub)

		space := createTestSpace(db)
		ch := &model.Channel{
			ID: idgen.NextString(), SpaceID: space.ID, Name: "voice-1",
			Type: "voice", Position: 0, VoiceQuality: "standard",
		}
		if err := db.Create(ch).Error; err != nil {
			t.Fatalf("create channel: %v", err)
		}

		router := gin.New()
		router.PATCH("/channels/:id", func(c *gin.Context) {
			setupAuthContext(c, "admin-1", "adminuser", middleware.RoleAdmin)
			c.Next()
		}, handler.UpdateChannel)

		body, _ := json.Marshal(map[string]string{"audioQuality": "high"})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("PATCH", "/channels/"+ch.ID, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}

		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload channel: %v", err)
		}
		if stored.VoiceQuality != "high" {
			t.Errorf("VoiceQuality = %q, want %q", stored.VoiceQuality, "high")
		}
	})

	t.Run("invalid rejected", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		hub := realtime.NewHub(testutil.TestLogger())
		handler := NewHandler(db, cfg, hub)

		space := createTestSpace(db)
		ch := &model.Channel{
			ID: idgen.NextString(), SpaceID: space.ID, Name: "voice-2",
			Type: "voice", Position: 0, VoiceQuality: "standard",
		}
		if err := db.Create(ch).Error; err != nil {
			t.Fatalf("create channel: %v", err)
		}

		router := gin.New()
		router.PATCH("/channels/:id", func(c *gin.Context) {
			setupAuthContext(c, "admin-1", "adminuser", middleware.RoleAdmin)
			c.Next()
		}, handler.UpdateChannel)

		// 旧的错误取值域（如 "256k"）必须被明确拒绝，而不是静默接受。
		body, _ := json.Marshal(map[string]string{"audioQuality": "256k"})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("PATCH", "/channels/"+ch.ID, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d for invalid audioQuality, got %d, body: %s",
				http.StatusBadRequest, w.Code, w.Body.String())
		}

		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload channel: %v", err)
		}
		if stored.VoiceQuality != "standard" {
			t.Errorf("VoiceQuality changed to %q on invalid input, want unchanged %q", stored.VoiceQuality, "standard")
		}
	})
}
