package channel

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
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// ─── A1-S1 频道分组（sortGroup）测试 ───
// 覆盖：创建落库/trim/超长 400/空串默认、更新指针语义（未传不改/空串移出/超长 400）、
// 列表排序（未分组段最前 → 组名字典序 → 组内 position）。admin PATCH 生效用例见
// internal/admin 包。

// sortGroupTestRouter 构造一个挂好鉴权 context 的测试路由（与 handler_test.go
// 的既有模式一致：中间件里注入 user_id/role 后放行到目标 handler）。
func sortGroupTestRouter(handler gin.HandlerFunc, method, path string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, path, func(c *gin.Context) {
		setupAuthContext(c, "admin-1", "adminuser", middleware.RoleAdmin)
		c.Next()
	}, handler)
	return router
}

// postChannelJSON 向测试路由 POST 一个 JSON 请求。
func postChannelJSON(t *testing.T, router *gin.Engine, target string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, target, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// patchChannelJSON 向测试路由 PATCH 一个 JSON 请求。
func patchChannelJSON(t *testing.T, router *gin.Engine, target string, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPatch, target, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// TestCreateChannelWithSortGroup 覆盖 A1-S1 创建路径。
func TestCreateChannelWithSortGroup(t *testing.T) {
	t.Run("stores trimmed sort group via HTTP", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		handler := NewHandler(db, config.DefaultConfig(), realtime.NewHub(testutil.TestLogger()))
		router := sortGroupTestRouter(handler.CreateChannel, http.MethodPost, "/channels")

		w := postChannelJSON(t, router, "/channels", map[string]string{"name": "games", "type": "text", "sortGroup": "  游戏  "})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.Channel
		if err := db.First(&stored, "name = ?", "games").Error; err != nil {
			t.Fatalf("reload channel: %v", err)
		}
		if stored.SortGroup != "游戏" {
			t.Errorf("SortGroup = %q, want %q（trim 应去掉首尾空白）", stored.SortGroup, "游戏")
		}
	})

	t.Run("empty sort group defaults to ungrouped", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		ch, err := svc.CreateChannel("no-group", "text", false, "", "", "")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if ch.SortGroup != "" {
			t.Errorf("SortGroup = %q, want 空串（未分组）", ch.SortGroup)
		}
	})

	t.Run("over-long sort group rejected with 400", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		handler := NewHandler(db, config.DefaultConfig(), realtime.NewHub(testutil.TestLogger()))
		router := sortGroupTestRouter(handler.CreateChannel, http.MethodPost, "/channels")

		tooLong := strings.Repeat("组", SortGroupMaxLen+1) // 33 rune > 32
		w := postChannelJSON(t, router, "/channels", map[string]string{"name": "over-long", "type": "text", "sortGroup": tooLong})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d, body: %s", w.Code, w.Body.String())
		}
		var count int64
		db.Model(&model.Channel{}).Where("name = ?", "over-long").Count(&count)
		if count != 0 {
			t.Errorf("超长分组名的频道不应落库")
		}
	})

	t.Run("boundary 32 runes accepted", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		createTestSpace(db)
		svc := NewService(db)

		exact := strings.Repeat("组", SortGroupMaxLen)
		ch, err := svc.CreateChannel("boundary", "text", false, "", "", exact)
		if err != nil {
			t.Fatalf("expected 32 rune 分组名可接受, got %v", err)
		}
		if ch.SortGroup != exact {
			t.Errorf("SortGroup = %q, want 原样保留", ch.SortGroup)
		}
	})
}

// TestUpdateChannelSortGroupPointer 覆盖 A1-S1 更新路径的指针语义。
func TestUpdateChannelSortGroupPointer(t *testing.T) {
	// newFixture 建库 + 种一个 sortGroup="原始组" 的频道，返回更新路由。
	newFixture := func(t *testing.T) (*gorm.DB, *gin.Engine, *model.Channel) {
		t.Helper()
		db := testutil.MustSetupTestDB()
		handler := NewHandler(db, config.DefaultConfig(), realtime.NewHub(testutil.TestLogger()))
		router := sortGroupTestRouter(handler.UpdateChannel, http.MethodPatch, "/channels/:id")
		space := createTestSpace(db)
		ch := &model.Channel{ID: idgen.NextString(), SpaceID: space.ID, Name: "target", Type: "text", SortGroup: "原始组", Position: 0}
		if err := db.Create(ch).Error; err != nil {
			t.Fatalf("create channel: %v", err)
		}
		return db, router, ch
	}

	t.Run("omitted sort group leaves value unchanged", func(t *testing.T) {
		db, router, ch := newFixture(t)
		w := patchChannelJSON(t, router, "/channels/"+ch.ID, map[string]interface{}{"position": 7})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.SortGroup != "原始组" {
			t.Errorf("未传 sortGroup 时被改成了 %q，应保持 %q", stored.SortGroup, "原始组")
		}
		if stored.Position != 7 {
			t.Errorf("position = %d, want 7（确认本次请求本身生效）", stored.Position)
		}
	})

	t.Run("empty string removes channel from group", func(t *testing.T) {
		db, router, ch := newFixture(t)
		w := patchChannelJSON(t, router, "/channels/"+ch.ID, map[string]interface{}{"sortGroup": ""})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.SortGroup != "" {
			t.Errorf("传空串应移出分组（sort_group=''），got %q", stored.SortGroup)
		}
	})

	t.Run("over-long sort group rejected and value unchanged", func(t *testing.T) {
		db, router, ch := newFixture(t)
		tooLong := strings.Repeat("组", SortGroupMaxLen+1)
		w := patchChannelJSON(t, router, "/channels/"+ch.ID, map[string]interface{}{"sortGroup": tooLong})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.SortGroup != "原始组" {
			t.Errorf("校验失败时原值不应被改写，got %q", stored.SortGroup)
		}
	})

	t.Run("trimmed value persisted", func(t *testing.T) {
		db, router, ch := newFixture(t)
		w := patchChannelJSON(t, router, "/channels/"+ch.ID, map[string]interface{}{"sortGroup": "  新分组  "})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.Channel
		if err := db.First(&stored, "id = ?", ch.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.SortGroup != "新分组" {
			t.Errorf("SortGroup = %q, want %q", stored.SortGroup, "新分组")
		}
	})
}

// TestGetChannelsOrdering 覆盖 A1-S1 列表排序：
// 未分组段最前 → 组名字典序 → 组内按 position。
func TestGetChannelsOrdering(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)
	space := createTestSpace(db)

	// 故意用乱序的插入顺序与 position，验证顺序完全由 ORDER BY 决定。
	rows := []model.Channel{
		{ID: idgen.NextString(), SpaceID: space.ID, Name: "beta-high", Type: "text", SortGroup: "beta", Position: 0},
		{ID: idgen.NextString(), SpaceID: space.ID, Name: "ungrouped", Type: "text", SortGroup: "", Position: 5},
		{ID: idgen.NextString(), SpaceID: space.ID, Name: "alpha-low", Type: "text", SortGroup: "alpha", Position: 1},
		{ID: idgen.NextString(), SpaceID: space.ID, Name: "alpha-high", Type: "text", SortGroup: "alpha", Position: 9},
		{ID: idgen.NextString(), SpaceID: space.ID, Name: "beta-late", Type: "text", SortGroup: "beta", Position: 3},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed channel %s: %v", rows[i].Name, err)
		}
	}

	got, err := svc.GetChannels("user-1", middleware.RoleOwner)
	if err != nil {
		t.Fatalf("GetChannels: %v", err)
	}

	want := []string{"ungrouped", "alpha-low", "alpha-high", "beta-high", "beta-late"}
	if len(got) != len(want) {
		t.Fatalf("got %d channels, want %d: %+v", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("顺序 %d: got %q (sortGroup=%q, pos=%d), want %q",
				i, got[i].Name, got[i].SortGroup, got[i].Position, name)
		}
	}
}
