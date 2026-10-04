package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/features/cloudfs"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// ─── E4：admin 全局分享列表 / 撤销 测试 ───

// seedShareFixture 种一个空间 + 一个文件 + 一条分享，返回关键 ID。
// TokenHash 用 cloudfs.GenerateShareToken 生成真实哈希（NOT NULL 约束）。
func seedShareFixture(t *testing.T, db *gorm.DB, spaceName, fileName string, createdAt time.Time, expiresAt time.Time) (shareID, fileID, spaceID string) {
	t.Helper()
	spaceID = idgen.NextString()
	if err := db.Create(&model.Space{ID: spaceID, Name: spaceName, OwnerID: idgen.NextString(), CreatedAt: createdAt, UpdatedAt: createdAt}).Error; err != nil {
		t.Fatalf("create space %s: %v", spaceName, err)
	}
	fileID = idgen.GenerateID(idgen.PrefixFile)
	if err := db.Create(&model.SharedFileEntry{
		ID: fileID, SpaceID: spaceID, FolderID: "root", FileName: fileName,
		FilePath: "/" + fileName, VersionNo: 1, UploadedBy: "usr_a", CreatedAt: createdAt,
	}).Error; err != nil {
		t.Fatalf("create file entry %s: %v", fileName, err)
	}
	_, tokenHash, _, err := cloudfs.GenerateShareToken()
	if err != nil {
		t.Fatalf("generate share token: %v", err)
	}
	shareID = idgen.GenerateID(idgen.PrefixShare)
	if err := db.Create(&model.CloudFileShare{
		ID: shareID, FileID: fileID, SpaceID: spaceID, OwnerID: "usr_a",
		TokenHash: tokenHash, ExpiresAt: expiresAt, CreatedAt: createdAt,
	}).Error; err != nil {
		t.Fatalf("create share: %v", err)
	}
	return shareID, fileID, spaceID
}

// adminSharesFixture 建库、admin 用户、handler、路由，返回便捷请求函数。
func adminSharesFixture(t *testing.T) (*gorm.DB, *gin.Engine, string) {
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
	return db, router, token
}

func doSharesGET(t *testing.T, router *gin.Engine, token, query string) (int, map[string]interface{}) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "/api/admin/cloudfs/shares"+query, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

func doSharesDELETE(t *testing.T, router *gin.Engine, token, shareID string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, "/api/admin/cloudfs/shares/"+shareID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// shareItems 从响应体提取 items 数组（元素为 map）。
func shareItems(t *testing.T, resp map[string]interface{}) []map[string]interface{} {
	t.Helper()
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing data object in response: %v", resp)
	}
	rawItems, ok := data["items"].([]interface{})
	if !ok {
		t.Fatalf("missing data.items array in response: %v", data)
	}
	items := make([]map[string]interface{}, 0, len(rawItems))
	for _, raw := range rawItems {
		if m, ok := raw.(map[string]interface{}); ok {
			items = append(items, m)
		}
	}
	return items
}

// TestAdminGlobalShares 覆盖：跨空间可见、fileName/spaceName 联表、
// created_at DESC 排序、limit/offset 分页、软删文件名留空。
func TestAdminGlobalShares(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	newWorld := func(t *testing.T) (*gorm.DB, *gin.Engine, string, string, string) {
		t.Helper()
		db, router, token := adminSharesFixture(t)
		// 两个空间各一条分享；旧分享在前（列表应倒序：新 → 旧）。
		oldID, _, _ := seedShareFixture(t, db, "空间甲", "report.pdf", base, base.AddDate(0, 0, 7))
		newID, _, _ := seedShareFixture(t, db, "空间乙", "demo.zip", base.Add(time.Minute), base.AddDate(0, 0, 7))
		return db, router, token, oldID, newID
	}

	t.Run("cross-space visibility with joined names", func(t *testing.T) {
		_, router, token, _, _ := newWorld(t)
		code, resp := doSharesGET(t, router, token, "")
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %v", code, resp)
		}
		items := shareItems(t, resp)
		if len(items) != 2 {
			t.Fatalf("expected 2 shares across spaces, got %d", len(items))
		}
		// created_at DESC：新分享在前。
		if items[0]["fileName"] != "demo.zip" || items[0]["spaceName"] != "空间乙" {
			t.Errorf("items[0] = %v/%v, want demo.zip/空间乙", items[0]["fileName"], items[0]["spaceName"])
		}
		if items[1]["fileName"] != "report.pdf" || items[1]["spaceName"] != "空间甲" {
			t.Errorf("items[1] = %v/%v, want report.pdf/空间甲", items[1]["fileName"], items[1]["spaceName"])
		}
		if items[0]["userId"] != "usr_a" {
			t.Errorf("userId = %v, want usr_a（分享属主）", items[0]["userId"])
		}
		for _, it := range items {
			if _, ok := it["requiresPassword"].(bool); !ok {
				t.Errorf("requiresPassword 缺失或非布尔: %v", it)
			}
			if _, ok := it["revoked"].(bool); !ok {
				t.Errorf("revoked 缺失或非布尔: %v", it)
			}
		}
	})

	t.Run("pagination limit and offset", func(t *testing.T) {
		_, router, token, _, _ := newWorld(t)
		code, resp := doSharesGET(t, router, token, "?limit=1&offset=0")
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d", code)
		}
		items := shareItems(t, resp)
		if len(items) != 1 || items[0]["fileName"] != "demo.zip" {
			t.Fatalf("limit=1 应返回最新一条 demo.zip, got %v", items)
		}

		code, resp = doSharesGET(t, router, token, "?limit=1&offset=1")
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d", code)
		}
		items = shareItems(t, resp)
		if len(items) != 1 || items[0]["fileName"] != "report.pdf" {
			t.Fatalf("offset=1 应跳到次新一条 report.pdf, got %v", items)
		}
	})

	t.Run("limit capped at 200", func(t *testing.T) {
		_, router, token, _, _ := newWorld(t)
		// 超大 limit 不报错（截断到 200）；非法 limit 回落默认 50。
		if code, _ := doSharesGET(t, router, token, "?limit=99999"); code != http.StatusOK {
			t.Errorf("limit=99999 expected 200, got %d", code)
		}
		if code, _ := doSharesGET(t, router, token, "?limit=abc"); code != http.StatusOK {
			t.Errorf("limit=abc expected 200（回落默认）, got %d", code)
		}
	})

	t.Run("soft-deleted file shows empty name", func(t *testing.T) {
		db, router, token, _, newID := newWorld(t)
		// 软删空间乙的文件：fileName 应显示空串（GORM 默认软删作用域）。
		if err := db.Delete(&model.SharedFileEntry{}, "file_name = ?", "demo.zip").Error; err != nil {
			t.Fatalf("soft delete file: %v", err)
		}
		code, resp := doSharesGET(t, router, token, "")
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d", code)
		}
		items := shareItems(t, resp)
		var target map[string]interface{}
		for _, it := range items {
			if it["id"] == newID {
				target = it
			}
		}
		if target == nil {
			t.Fatalf("share %s not in list", newID)
		}
		if target["fileName"] != "" {
			t.Errorf("软删文件的 fileName = %v, want 空串", target["fileName"])
		}
	})
}

// TestAdminRevokeShare 覆盖：撤销成功、幂等二次撤销、审计只在首次落一条、
// 不存在的 id 返回 404。
func TestAdminRevokeShare(t *testing.T) {
	newWorld := func(t *testing.T) (*gorm.DB, *gin.Engine, string, string) {
		t.Helper()
		db, router, token := adminSharesFixture(t)
		shareID, _, _ := seedShareFixture(t, db, "空间丙", "secret.txt", time.Now().UTC().Add(-time.Minute), time.Now().UTC().AddDate(0, 0, 7))
		return db, router, token, shareID
	}

	countAudit := func(t *testing.T, db *gorm.DB) int64 {
		t.Helper()
		var n int64
		if err := db.Model(&model.SecurityAuditLog{}).Where("action = ?", "admin.cloudfs.share_revoked").Count(&n).Error; err != nil {
			t.Fatalf("count audit: %v", err)
		}
		return n
	}

	t.Run("revoke succeeds and persists", func(t *testing.T) {
		db, router, token, shareID := newWorld(t)
		w := doSharesDELETE(t, router, token, shareID)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		var stored model.CloudFileShare
		if err := db.First(&stored, "id = ?", shareID).Error; err != nil {
			t.Fatalf("reload share: %v", err)
		}
		if stored.RevokedAt == nil {
			t.Errorf("revoked_at 未落库")
		}
		if n := countAudit(t, db); n != 1 {
			t.Errorf("首次撤销应落 1 条 admin.cloudfs.share_revoked 审计, got %d", n)
		}
	})

	t.Run("second revoke is idempotent without new audit", func(t *testing.T) {
		db, router, token, shareID := newWorld(t)
		if w := doSharesDELETE(t, router, token, shareID); w.Code != http.StatusOK {
			t.Fatalf("first revoke expected 200, got %d", w.Code)
		}
		w := doSharesDELETE(t, router, token, shareID)
		if w.Code != http.StatusOK {
			t.Fatalf("幂等二次撤销 expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		if n := countAudit(t, db); n != 1 {
			t.Errorf("幂等重复撤销不应再写审计, got %d 条", n)
		}
		var stored model.CloudFileShare
		if err := db.First(&stored, "id = ?", shareID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.RevokedAt == nil {
			t.Errorf("revoked_at 不应被清除")
		}
	})

	t.Run("unknown id returns 404", func(t *testing.T) {
		_, router, token, _ := newWorld(t)
		w := doSharesDELETE(t, router, token, "share_nonexistent")
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d, body: %s", w.Code, w.Body.String())
		}
	})

	t.Run("empty id param returns 400", func(t *testing.T) {
		_, router, token, _ := newWorld(t)
		w := doSharesDELETE(t, router, token, "%20") // 仅空白 → BAD_REQUEST
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d, body: %s", w.Code, w.Body.String())
		}
	})
}
