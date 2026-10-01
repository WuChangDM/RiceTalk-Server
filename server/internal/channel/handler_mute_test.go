package channel

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

// createMuteTestChannel 建一个可直接断言的测试频道。
func createMuteTestChannel(db *gorm.DB, spaceID, name, visibility string) *model.Channel {
	ch := &model.Channel{ID: idgen.NextString(), SpaceID: spaceID, Name: name, Type: "text", Position: 0, Visibility: visibility}
	_ = db.Create(ch)
	return ch
}

// setupAuthContext 注入已认证用户（与 handler_test.go 同款）。
func setupMuteAuthContext(c *gin.Context, userID, username, role string) {
	c.Set("user_id", userID)
	c.Set("username", username)
	c.Set("email", username+"@example.com")
	c.Set("role", role)
}

// doMuteRequest 发起 JSON 请求（body 可为 nil）。
func doMuteRequest(t *testing.T, router *gin.Engine, method, path string, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body failed: %v", err)
		}
		req, _ = http.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestSetChannelMute(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	hub := realtime.NewHub(testutil.TestLogger())
	handler := NewHandler(db, cfg, hub)

	space := createTestSpace(db)
	ch := createMuteTestChannel(db, space.ID, "mute-target", "public")

	// happy path 路由：auth 注入中间件 + 真实 ChannelPermission（与生产防线一致）
	router := gin.New()
	router.PUT("/api/channels/:id/mute", func(c *gin.Context) {
		setupMuteAuthContext(c, "user-1", "muteuser", middleware.RoleMember)
		c.Next()
	}, middleware.ChannelPermission(db), handler.SetChannelMute)
	router.GET("/api/channels/mutes", func(c *gin.Context) {
		setupMuteAuthContext(c, "user-1", "muteuser", middleware.RoleMember)
		c.Next()
	}, handler.ListChannelMutes)

	t.Run("mute forever then list", func(t *testing.T) {
		w := doMuteRequest(t, router, "PUT", "/api/channels/"+ch.ID+"/mute", map[string]interface{}{"muted": true})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}

		w = doMuteRequest(t, router, "GET", "/api/channels/mutes", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 on list, got %d, body: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Code string                  `json:"code"`
			Data []model.UserChannelMute `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal list failed: %v", err)
		}
		found := false
		for _, m := range resp.Data {
			if m.ChannelID == ch.ID {
				found = true
				if m.MutedUntil != nil {
					t.Errorf("expected permanent mute (mutedUntil nil), got %v", m.MutedUntil)
				}
			}
		}
		if !found {
			t.Errorf("expected mute row for channel %s in list, got %v", ch.ID, resp.Data)
		}
	})

	t.Run("mute with duration then expiry filter", func(t *testing.T) {
		w := doMuteRequest(t, router, "PUT", "/api/channels/"+ch.ID+"/mute",
			map[string]interface{}{"muted": true, "durationMinutes": 30})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}

		w = doMuteRequest(t, router, "GET", "/api/channels/mutes", nil)
		var resp struct {
			Data []model.UserChannelMute `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		found := false
		for _, m := range resp.Data {
			if m.ChannelID == ch.ID {
				found = true
				if m.MutedUntil == nil {
					t.Errorf("expected timed mute (mutedUntil set), got nil")
				}
			}
		}
		if !found {
			t.Errorf("expected timed mute row for channel %s, got %v", ch.ID, resp.Data)
		}

		// 直写过期时间：清单应不再返回该行（过期过滤生效）
		if err := db.Model(&model.UserChannelMute{}).
			Where("user_id = ? AND channel_id = ?", "user-1", ch.ID).
			Update("muted_until", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
			t.Fatalf("failed to backdate muted_until: %v", err)
		}
		w = doMuteRequest(t, router, "GET", "/api/channels/mutes", nil)
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		for _, m := range resp.Data {
			if m.ChannelID == ch.ID {
				t.Errorf("expected expired mute to be filtered out, still present")
			}
		}
	})

	t.Run("re-mute overwrites and unmute removes", func(t *testing.T) {
		// 重复开启（覆盖既有行，验证删除重建路径与唯一索引）
		w := doMuteRequest(t, router, "PUT", "/api/channels/"+ch.ID+"/mute", map[string]interface{}{"muted": true})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		w = doMuteRequest(t, router, "PUT", "/api/channels/"+ch.ID+"/mute",
			map[string]interface{}{"muted": true, "durationMinutes": 15})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 on re-mute, got %d, body: %s", w.Code, w.Body.String())
		}
		var count int64
		db.Model(&model.UserChannelMute{}).Where("user_id = ? AND channel_id = ?", "user-1", ch.ID).Count(&count)
		if count != 1 {
			t.Fatalf("expected exactly 1 mute row after re-mute, got %d", count)
		}

		w = doMuteRequest(t, router, "PUT", "/api/channels/"+ch.ID+"/mute", map[string]interface{}{"muted": false})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 on unmute, got %d, body: %s", w.Code, w.Body.String())
		}
		db.Model(&model.UserChannelMute{}).Where("user_id = ? AND channel_id = ?", "user-1", ch.ID).Count(&count)
		if count != 0 {
			t.Errorf("expected mute row removed after unmute, got %d", count)
		}
	})

	t.Run("negative duration gets 400", func(t *testing.T) {
		w := doMuteRequest(t, router, "PUT", "/api/channels/"+ch.ID+"/mute",
			map[string]interface{}{"muted": true, "durationMinutes": -5})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d, body: %s", w.Code, w.Body.String())
		}
	})

	t.Run("member denied on admin-only channel gets 403", func(t *testing.T) {
		adminCh := createMuteTestChannel(db, space.ID, "admin-only-mute", "admin-only")
		w := doMuteRequest(t, router, "PUT", "/api/channels/"+adminCh.ID+"/mute", map[string]interface{}{"muted": true})
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d, body: %s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown channel gets 404", func(t *testing.T) {
		w := doMuteRequest(t, router, "PUT", "/api/channels/ch_nonexistent/mute", map[string]interface{}{"muted": true})
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d, body: %s", w.Code, w.Body.String())
		}
	})
}

// TestSetChannelMuteUnauthenticated 验证未登录 401：挂生产同款 AuthRequired 中间件。
// 注意：checkServerInitialized 在库中有用户时视为已初始化，否则会先返回
// SERVER_NOT_INIT 而非 401，因此这里先落一个用户保证初始化检查通过。
func TestSetChannelMuteUnauthenticated(t *testing.T) {
	db := testutil.MustSetupTestDB()
	_ = db.Create(&model.User{ID: idgen.NextString(), Username: "mute-auth-user", Email: "mute-auth-user@example.com", PasswordHash: "x"}).Error

	cfg := config.DefaultConfig()
	hub := realtime.NewHub(testutil.TestLogger())
	handler := NewHandler(db, cfg, hub)
	router := gin.New()
	// 同时注册静态段 /channels/mutes 与参数段 /channels/:id，
	// 验证 gin ≥1.7 的静态优先路由共存不 panic。
	router.GET("/api/channels/mutes", middleware.AuthRequired(cfg, db), handler.ListChannelMutes)
	router.GET("/api/channels/:id", middleware.AuthRequired(cfg, db), handler.GetChannel)
	router.PUT("/api/channels/:id/mute", middleware.AuthRequired(cfg, db), handler.SetChannelMute)

	w := doMuteRequest(t, router, "PUT", "/api/channels/ch_x/mute", map[string]interface{}{"muted": true})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d, body: %s", w.Code, w.Body.String())
	}
}
