package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/channel"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// adminSortGroupToken 为 admin 用户签发真实 JWT（走完整 AuthRequired + RequireAdmin 链）。
func adminSortGroupToken(t *testing.T, cfg *config.Config, userID string) string {
	t.Helper()
	token, _, err := middleware.GenerateTokenPair(userID, "admin", "admin@example.com", middleware.RoleAdmin, 0, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

// seedAdminSortGroupChannel 建空间与带初始分组的频道。
func seedAdminSortGroupChannel(t *testing.T, db *gorm.DB) *model.Channel {
	t.Helper()
	space := &model.Space{ID: idgen.NextString(), Name: "sg-space", OwnerID: idgen.NextString()}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	ch := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "admin-target", Type: "text", SortGroup: "初始组", Position: 0}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return ch
}

// TestAdminUpdateChannelSortGroup 覆盖 admin PATCH /admin/channels/:channelId 的
// sortGroup 放行：生效（含 trim）、空串移出分组、超长 400 且不改写。
func TestAdminUpdateChannelSortGroup(t *testing.T) {
	newFixture := func(t *testing.T) (*gorm.DB, *gin.Engine, *model.Channel, string) {
		t.Helper()
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		cfg.JWTSecret = "test-secret"

		adminUser := &model.User{ID: idgen.NextString(), Username: "admin", Email: "admin@example.com", PasswordHash: "x", Role: middleware.RoleAdmin}
		if err := db.Create(adminUser).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		token := adminSortGroupToken(t, cfg, adminUser.ID)

		handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		ch := seedAdminSortGroupChannel(t, db)
		return db, router, ch, token
	}

	patch := func(t *testing.T, router *gin.Engine, token, channelID string, body map[string]interface{}) *httptest.ResponseRecorder {
		t.Helper()
		payload, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPatch, "/api/admin/channels/"+channelID, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("sort group persisted with trim", func(t *testing.T) {
		db, router, ch, token := newFixture(t)
		w := patch(t, router, token, ch.ID, map[string]interface{}{"sortGroup": "  游戏区  "})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.SortGroup != "游戏区" {
			t.Errorf("admin PATCH sortGroup = %q, want %q（trim 后落库）", stored.SortGroup, "游戏区")
		}
	})

	t.Run("empty string clears group", func(t *testing.T) {
		db, router, ch, token := newFixture(t)
		w := patch(t, router, token, ch.ID, map[string]interface{}{"sortGroup": ""})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.SortGroup != "" {
			t.Errorf("传空串应移出分组，got %q", stored.SortGroup)
		}
	})

	t.Run("over-long rejected with 400 and unchanged", func(t *testing.T) {
		db, router, ch, token := newFixture(t)
		tooLong := strings.Repeat("组", channel.SortGroupMaxLen+1)
		w := patch(t, router, token, ch.ID, map[string]interface{}{"sortGroup": tooLong})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.SortGroup != "初始组" {
			t.Errorf("校验失败时原值不应被改写，got %q", stored.SortGroup)
		}
	})
}
