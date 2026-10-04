package sharedoc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/tests/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func init() {
	_ = idgen.Init(1, 1)
}

func setupTestHandler() (*Handler, *gin.Engine, *gorm.DB) {
	db := testutil.MustSetupTestDB()
	createTestSpaceAndMembership(db)
	cfg := &config.Config{}
	h := NewHandler(db, cfg, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	sd := api.Group("/sharedoc")
	{
		sd.GET("/list", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.List(c)
		})
		// FIX-20261003-01 NJ-13：单文档详情（含 content/version）
		sd.GET("/:id", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.Get(c)
		})
		sd.POST("/create", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.Create(c)
		})
		sd.PATCH("/:id", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.Update(c)
		})
		sd.DELETE("/:id", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.Delete(c)
		})
		// M25: 版本历史 API
		sd.GET("/:id/versions", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.ListVersions(c)
		})
		sd.GET("/:id/versions/:versionNo", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.GetVersion(c)
		})
		sd.POST("/:id/versions/:versionNo/restore", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.RestoreVersion(c)
		})
		// Phase 2: 版本对比
		sd.GET("/:id/versions/compare", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.CompareVersions(c)
		})
		// M23: 协同编辑 - 在线编辑者列表
		sd.GET("/:id/editors", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.GetEditors(c)
		})
		// Phase 2: 文档评论
		sd.GET("/:id/comments", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.ListComments(c)
		})
		sd.POST("/:id/comments", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.CreateComment(c)
		})
		sd.PATCH("/:id/comments/:commentId", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.UpdateComment(c)
		})
		sd.DELETE("/:id/comments/:commentId", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.DeleteComment(c)
		})
		sd.POST("/:id/comments/:commentId/resolve", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.ResolveComment(c)
		})
		sd.POST("/:id/comments/:commentId/reopen", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "test-user")
			c.Set("role", "MEMBER")
			h.ReopenComment(c)
		})
	}
	return h, r, db
}

func TestNewHandlerDoesNotOverrideOfflineCallback(t *testing.T) {
	db := testutil.MustSetupTestDB()
	hub := realtime.NewHub(nil)
	called := make(chan string, 1)
	hub.OnUserFullyOffline = func(userID string) {
		called <- userID
	}

	_ = NewHandler(db, &config.Config{}, hub)
	hub.OnUserFullyOffline("user-1")

	select {
	case userID := <-called:
		if userID != "user-1" {
			t.Fatalf("offline callback userID = %q, want %q", userID, "user-1")
		}
	default:
		t.Fatal("sharedoc handler replaced the existing offline callback")
	}
}

func createTestDoc(db *gorm.DB, title, content string) *model.SharedDocument {
	doc := &model.SharedDocument{
		ID:      idgen.NextString(),
		Title:   title,
		Content: content,
		SpaceID: "space-1",
		OwnerID: "user-1",
		Version: 1,
	}
	if err := db.Create(doc).Error; err != nil {
		panic(err)
	}
	return doc
}

// createTestSpaceAndMembership creates a space and adds user-1/user-2 as members.
func createTestSpaceAndMembership(db *gorm.DB) {
	if err := db.Create(&model.Space{ID: "space-1", Name: "Test Space", OwnerID: "user-1"}).Error; err != nil {
		panic(err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: "user-1", SpaceID: "space-1", Role: "OWNER"}).Error; err != nil {
		panic(err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: "user-2", SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
		panic(err)
	}
}

// TestUpdateCreatesVersionHistory verifies M25: Update records version history and increments Version
func TestUpdateCreatesVersionHistory(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "原始标题", "原始内容")

	// 第一次更新
	body := map[string]interface{}{
		"title":   "更新标题1",
		"content": "更新内容1",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// 验证主表 Version 递增到 2
	var updated model.SharedDocument
	db.First(&updated, doc.ID)
	if updated.Version != 2 {
		t.Errorf("expected Version=2 after first update, got %d", updated.Version)
	}
	if updated.Title != "更新标题1" || updated.Content != "更新内容1" {
		t.Errorf("expected updated content, got title=%q content=%q", updated.Title, updated.Content)
	}

	// 验证版本历史表新增 1 条记录（版本号 1，原始内容）
	var versions []model.SharedDocumentVersion
	db.Where("document_id = ?", doc.ID).Order("version_no ASC").Find(&versions)
	if len(versions) != 1 {
		t.Fatalf("expected 1 version history record, got %d", len(versions))
	}
	if versions[0].VersionNo != 1 {
		t.Errorf("expected version_no=1, got %d", versions[0].VersionNo)
	}
	if versions[0].Title != "原始标题" || versions[0].Content != "原始内容" {
		t.Errorf("expected snapshot of original content, got title=%q content=%q", versions[0].Title, versions[0].Content)
	}

	// 第二次更新
	body2 := map[string]interface{}{
		"title":   "更新标题2",
		"content": "更新内容2",
	}
	jsonBody2, _ := json.Marshal(body2)
	req2 := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected status 200 on second update, got %d, body: %s", w2.Code, w2.Body.String())
	}

	// 验证 Version 递增到 3，版本历史有 2 条
	db.First(&updated, doc.ID)
	if updated.Version != 3 {
		t.Errorf("expected Version=3 after second update, got %d", updated.Version)
	}
	db.Where("document_id = ?", doc.ID).Order("version_no ASC").Find(&versions)
	if len(versions) != 2 {
		t.Errorf("expected 2 version history records, got %d", len(versions))
	}
}

// TestListVersions verifies M25: ListVersions returns version history (without content)
func TestListVersions(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "标题", "内容")

	// 更新 2 次以产生 2 条历史
	for i := 1; i <= 2; i++ {
		body := map[string]interface{}{
			"title":   "标题" + string(rune('A'+i)),
			"content": "内容" + string(rune('A'+i)),
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("update %d failed: %d %s", i, w.Code, w.Body.String())
		}
	}

	// 查询版本列表
	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/versions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].([]interface{})
	if len(data) != 2 {
		t.Errorf("expected 2 versions in list, got %d", len(data))
	}

	// 验证列表项不含 content 字段（仅元数据）
	if len(data) > 0 {
		first, _ := data[0].(map[string]interface{})
		if _, hasContent := first["content"]; hasContent {
			t.Errorf("version list should not include content field")
		}
		if _, hasVersionNo := first["versionNo"]; !hasVersionNo {
			t.Errorf("version list should include versionNo field")
		}
	}
}

// TestGetVersion verifies M25: GetVersion returns full content of a specific version
func TestGetVersion(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "原始标题", "原始内容")

	// 更新一次，产生版本 1 的历史
	body := map[string]interface{}{
		"title":   "新标题",
		"content": "新内容",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update failed: %d %s", w.Code, w.Body.String())
	}

	// 查询版本 1 的完整内容
	req = httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/versions/1", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatal("expected data object in response")
	}
	if data["title"] != "原始标题" {
		t.Errorf("expected title=原始标题, got %v", data["title"])
	}
	if data["content"] != "原始内容" {
		t.Errorf("expected content=原始内容, got %v", data["content"])
	}
	if data["versionNo"].(float64) != 1 {
		t.Errorf("expected versionNo=1, got %v", data["versionNo"])
	}
}

// TestGetVersionNotFound verifies M25: GetVersion returns 404 for non-existent version
func TestGetVersionNotFound(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "标题", "内容")

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/versions/999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404 for non-existent version, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestRestoreVersion verifies M25: RestoreVersion restores content and records current as new version
func TestRestoreVersion(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "原始标题", "原始内容")

	// 更新一次，版本 1 进入历史，主表 Version=2
	body := map[string]interface{}{
		"title":   "修改后标题",
		"content": "修改后内容",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update failed: %d %s", w.Code, w.Body.String())
	}

	// 恢复到版本 1
	req = httptest.NewRequest("POST", "/api/v1/sharedoc/"+doc.ID+"/versions/1/restore", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// 验证主表内容已恢复为版本 1 的内容，Version 递增到 3
	var restored model.SharedDocument
	db.First(&restored, doc.ID)
	if restored.Title != "原始标题" {
		t.Errorf("expected restored title=原始标题, got %q", restored.Title)
	}
	if restored.Content != "原始内容" {
		t.Errorf("expected restored content=原始内容, got %q", restored.Content)
	}
	if restored.Version != 3 {
		t.Errorf("expected Version=3 after restore, got %d", restored.Version)
	}

	// 验证版本历史表新增了一条记录（版本 2，即恢复前的"修改后"内容）
	var versions []model.SharedDocumentVersion
	db.Where("document_id = ? AND version_no = ?", doc.ID, 2).Find(&versions)
	if len(versions) != 1 {
		t.Fatalf("expected 1 history record for version 2, got %d", len(versions))
	}
	if versions[0].Title != "修改后标题" {
		t.Errorf("expected snapshot title=修改后标题, got %q", versions[0].Title)
	}
}

// TestListVersionsPermissionDenied verifies M25: non-owner non-admin cannot list versions
func TestListVersionsPermissionDenied(t *testing.T) {
	db := testutil.MustSetupTestDB()
	createTestSpaceAndMembership(db)
	cfg := &config.Config{}
	h := NewHandler(db, cfg, nil)

	// 创建一个属于 user-2 但位于 space-1 的文档
	doc := &model.SharedDocument{
		ID:      idgen.NextString(),
		SpaceID: "space-1",
		Title:   "他人文档",
		Content: "内容",
		OwnerID: "user-2",
		Version: 1,
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatalf("failed to create doc: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	api.GET("/sharedoc/:id/versions", func(c *gin.Context) {
		c.Set("user_id", "user-1") // 当前用户是 user-1，非 owner 但同属 space-1
		c.Set("username", "test-user")
		c.Set("role", "MEMBER")
		h.ListVersions(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/versions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 同 Space 成员现在应该可以查看
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for same-space member, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestListVersionsDeniedForNonSpaceMember verifies that users outside the space cannot list versions.
func TestListVersionsDeniedForNonSpaceMember(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := &config.Config{}
	h := NewHandler(db, cfg, nil)

	// 创建 space 并只加入 user-2，不包含 user-1
	if err := db.Create(&model.Space{ID: "space-2", Name: "Other Space", OwnerID: "user-2"}).Error; err != nil {
		t.Fatalf("failed to create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: "user-2", SpaceID: "space-2", Role: "OWNER"}).Error; err != nil {
		t.Fatalf("failed to create membership: %v", err)
	}
	doc := &model.SharedDocument{
		ID:      idgen.NextString(),
		SpaceID: "space-2",
		Title:   "他人文档",
		Content: "内容",
		OwnerID: "user-2",
		Version: 1,
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatalf("failed to create doc: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	api.GET("/sharedoc/:id/versions", func(c *gin.Context) {
		c.Set("user_id", "user-1") // user-1 不是 space-2 成员
		c.Set("username", "test-user")
		c.Set("role", "MEMBER")
		h.ListVersions(c)
	})

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/versions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for non-space member, got %d, body: %s", w.Code, w.Body.String())
	}
}

// ===== M23 协同编辑测试 =====

// TestUpdateWithExpectedVersionSuccess verifies M23: Update succeeds when expectedVersion matches
func TestUpdateWithExpectedVersionSuccess(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "标题", "内容") // Version=1

	expectedVersion := 1
	body := map[string]interface{}{
		"title":           "新标题",
		"content":         "新内容",
		"expectedVersion": expectedVersion,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 when expectedVersion matches, got %d, body: %s", w.Code, w.Body.String())
	}

	// 验证 Version 递增到 2
	var updated model.SharedDocument
	db.First(&updated, doc.ID)
	if updated.Version != 2 {
		t.Errorf("expected Version=2, got %d", updated.Version)
	}
	if updated.Title != "新标题" {
		t.Errorf("expected title=新标题, got %q", updated.Title)
	}
}

// TestUpdateWithExpectedVersionConflict verifies M23: Update returns 409 when expectedVersion mismatches
func TestUpdateWithExpectedVersionConflict(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "标题", "内容") // Version=1

	// 客户端以为是版本 5，但实际是版本 1，应返回 409
	expectedVersion := 5
	body := map[string]interface{}{
		"title":           "新标题",
		"content":         "新内容",
		"expectedVersion": expectedVersion,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected status 409 when expectedVersion mismatches, got %d, body: %s", w.Code, w.Body.String())
	}

	// 验证文档未被修改
	var unchanged model.SharedDocument
	db.First(&unchanged, doc.ID)
	if unchanged.Title != "标题" {
		t.Errorf("document should be unchanged on conflict, got title=%q", unchanged.Title)
	}
	if unchanged.Version != 1 {
		t.Errorf("Version should remain 1 on conflict, got %d", unchanged.Version)
	}

	// 验证错误码为 SHAREDOC_CONFLICT
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
		if code, ok := resp["code"].(string); ok && code != "SHAREDOC_CONFLICT" {
			t.Errorf("expected error code=SHAREDOC_CONFLICT, got %q", code)
		}
	}
}

// TestUpdateWithoutExpectedVersion verifies M23: Update without expectedVersion skips check (backward compat)
func TestUpdateWithoutExpectedVersion(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "标题", "内容") // Version=1

	// 不传 expectedVersion，应跳过校验，向后兼容
	body := map[string]interface{}{
		"title":   "新标题",
		"content": "新内容",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 without expectedVersion, got %d, body: %s", w.Code, w.Body.String())
	}

	var updated model.SharedDocument
	db.First(&updated, doc.ID)
	if updated.Version != 2 {
		t.Errorf("expected Version=2, got %d", updated.Version)
	}
}

// TestGetEditorsEmpty verifies M23: GetEditors returns empty list when no editors
func TestGetEditorsEmpty(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "标题", "内容")

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/editors", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatal("expected data object in response")
	}
	if data["count"].(float64) != 0 {
		t.Errorf("expected count=0, got %v", data["count"])
	}
	editors, _ := data["editors"].([]interface{})
	if len(editors) != 0 {
		t.Errorf("expected empty editors list, got %d items", len(editors))
	}
}

// TestGetEditorsWithEditors verifies M23: GetEditors returns list after AddEditor called
func TestGetEditorsWithEditors(t *testing.T) {
	h, r, db := setupTestHandler()

	doc := createTestDoc(db, "标题", "内容")

	// 模拟两个用户加入编辑
	h.AddEditor(doc.ID, "user-1")
	h.AddEditor(doc.ID, "user-2")
	// 重复调用 AddEditor 不应重复添加
	h.AddEditor(doc.ID, "user-1")

	// 创建 user-2 用户记录以便查询用户名
	if err := db.Create(&model.User{ID: "user-2", Username: "alice", Email: "alice@test.com", PasswordHash: "x"}).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/editors", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatal("expected data object in response")
	}
	if data["count"].(float64) != 2 {
		t.Errorf("expected count=2, got %v", data["count"])
	}
	editors, _ := data["editors"].([]interface{})
	if len(editors) != 2 {
		t.Errorf("expected 2 editors, got %d", len(editors))
	}

	// 验证 RemoveEditor 后数量减少
	h.RemoveEditor(doc.ID, "user-1")
	req2 := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/editors", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected status 200 after remove, got %d", w2.Code)
	}
	json.Unmarshal(w2.Body.Bytes(), &resp)
	data, _ = resp["data"].(map[string]interface{})
	if data["count"].(float64) != 1 {
		t.Errorf("expected count=1 after remove, got %v", data["count"])
	}
}

// TestGetEditorsDocNotFound verifies M23: GetEditors returns 404 for non-existent doc
func TestGetEditorsDocNotFound(t *testing.T) {
	_, r, _ := setupTestHandler()

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/non-existent-doc/editors", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404 for non-existent doc, got %d, body: %s", w.Code, w.Body.String())
	}
}

// createTestComment inserts a comment for testing.
func createTestComment(db *gorm.DB, docID, userID, content, anchor string, parentID *string) *model.SharedDocumentComment {
	c := &model.SharedDocumentComment{
		ID:         idgen.NextString(),
		DocumentID: docID,
		ParentID:   parentID,
		AnchorText: anchor,
		Content:    content,
		CreatedBy:  userID,
	}
	if err := db.Create(c).Error; err != nil {
		panic(err)
	}
	return c
}

// ===== Phase 2 评论测试 =====

func TestListCommentsEmpty(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "内容")

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/comments", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].([]interface{})
	if len(data) != 0 {
		t.Errorf("expected 0 comments, got %d", len(data))
	}
}

func TestCreateComment(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "内容")

	body := map[string]interface{}{
		"anchorText": "内容",
		"content":    "这条评论提到 @user-2",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/sharedoc/"+doc.ID+"/comments", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	if data["content"] != "这条评论提到 @user-2" {
		t.Errorf("unexpected content: %v", data["content"])
	}
	if data["anchorText"] != "内容" {
		t.Errorf("unexpected anchorText: %v", data["anchorText"])
	}
}

func TestCreateReplyComment(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "内容")
	parent := createTestComment(db, doc.ID, "user-1", "父评论", "锚定", nil)

	body := map[string]interface{}{
		"parentId": parent.ID,
		"content":  "回复内容",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/sharedoc/"+doc.ID+"/comments", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var listResp map[string]interface{}
	req2 := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/comments", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	json.Unmarshal(w2.Body.Bytes(), &listResp)
	roots, _ := listResp["data"].([]interface{})
	if len(roots) != 1 {
		t.Fatalf("expected 1 root comment, got %d", len(roots))
	}
	root, _ := roots[0].(map[string]interface{})
	replies, _ := root["replies"].([]interface{})
	if len(replies) != 1 {
		t.Errorf("expected 1 reply, got %d", len(replies))
	}
}

func TestUpdateComment(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "内容")
	comment := createTestComment(db, doc.ID, "user-1", "旧内容", "锚定", nil)

	body := map[string]interface{}{"content": "新内容"}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID+"/comments/"+comment.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	if data["content"] != "新内容" {
		t.Errorf("expected updated content, got %v", data["content"])
	}
}

func TestDeleteComment(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "内容")
	comment := createTestComment(db, doc.ID, "user-1", "待删除", "锚定", nil)

	req := httptest.NewRequest("DELETE", "/api/v1/sharedoc/"+doc.ID+"/comments/"+comment.ID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var c model.SharedDocumentComment
	if err := db.Where("id = ?", comment.ID).First(&c).Error; err == nil {
		t.Errorf("comment should be deleted")
	}
}

func TestResolveReopenComment(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "内容")
	comment := createTestComment(db, doc.ID, "user-1", "待解决", "锚定", nil)

	req := httptest.NewRequest("POST", "/api/v1/sharedoc/"+doc.ID+"/comments/"+comment.ID+"/resolve", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve failed: %d %s", w.Code, w.Body.String())
	}

	var resolved model.SharedDocumentComment
	db.First(&resolved, comment.ID)
	if !resolved.Resolved || resolved.ResolvedBy == nil || *resolved.ResolvedBy != "user-1" {
		t.Errorf("comment should be resolved by user-1")
	}

	req2 := httptest.NewRequest("POST", "/api/v1/sharedoc/"+doc.ID+"/comments/"+comment.ID+"/reopen", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("reopen failed: %d %s", w2.Code, w2.Body.String())
	}
	db.First(&resolved, comment.ID)
	if resolved.Resolved {
		t.Errorf("comment should be reopened")
	}
}

func TestCommentPermissionDenied(t *testing.T) {
	db := testutil.MustSetupTestDB()
	createTestSpaceAndMembership(db)
	cfg := &config.Config{}
	h := NewHandler(db, cfg, nil)

	doc := createTestDoc(db, "他人文档", "内容")
	comment := createTestComment(db, doc.ID, "user-2", "他人评论", "", nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	api.PATCH("/sharedoc/:id/comments/:commentId", func(c *gin.Context) {
		c.Set("user_id", "user-1") // user-1 是 space 成员但不是评论作者
		c.Set("username", "test-user")
		c.Set("role", "MEMBER")
		h.UpdateComment(c)
	})

	body := map[string]interface{}{"content": "篡改"}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID+"/comments/"+comment.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestCreateCommentAsSpaceMember verifies that any space member can create a comment.
func TestCreateCommentAsSpaceMember(t *testing.T) {
	db := testutil.MustSetupTestDB()
	createTestSpaceAndMembership(db)
	cfg := &config.Config{}
	h := NewHandler(db, cfg, nil)
	doc := createTestDoc(db, "标题", "内容")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	api.POST("/sharedoc/:id/comments", func(c *gin.Context) {
		c.Set("user_id", "user-2") // 非 owner，但是 space 成员
		c.Set("username", "alice")
		c.Set("role", "MEMBER")
		h.CreateComment(c)
	})

	body := map[string]interface{}{"content": "我是 space 成员，可以评论"}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/sharedoc/"+doc.ID+"/comments", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 for space member comment, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	if data["createdBy"] != "user-2" {
		t.Errorf("expected createdBy=user-2, got %v", data["createdBy"])
	}
}

// TestCreateCommentDeniedForNonSpaceMember verifies that non-space members cannot comment.
func TestCreateCommentDeniedForNonSpaceMember(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := &config.Config{}
	h := NewHandler(db, cfg, nil)
	if err := db.Create(&model.Space{ID: "space-2", Name: "Other Space", OwnerID: "user-2"}).Error; err != nil {
		t.Fatalf("failed to create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), UserID: "user-2", SpaceID: "space-2", Role: "OWNER"}).Error; err != nil {
		t.Fatalf("failed to create membership: %v", err)
	}
	doc := &model.SharedDocument{
		ID:      idgen.NextString(),
		SpaceID: "space-2",
		Title:   "标题",
		Content: "内容",
		OwnerID: "user-2",
		Version: 1,
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatalf("failed to create doc: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	api.POST("/sharedoc/:id/comments", func(c *gin.Context) {
		c.Set("user_id", "user-1") // user-1 不是 space-2 成员
		c.Set("username", "test-user")
		c.Set("role", "MEMBER")
		h.CreateComment(c)
	})

	body := map[string]interface{}{"content": "我不能评论"}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/sharedoc/"+doc.ID+"/comments", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestEditorCleanupOnUserOffline verifies that editor sessions are cleaned when user goes fully offline.
func TestEditorCleanupOnUserOffline(t *testing.T) {
	h, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "内容")

	h.AddEditor(doc.ID, "user-1")
	h.AddEditor(doc.ID, "user-2")

	// Simulate user-1's last connection disconnecting
	h.OnUserFullyOffline("user-1")

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/editors", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	if data["count"].(float64) != 1 {
		t.Errorf("expected 1 editor after cleanup, got %v", data["count"])
	}
}

// TestUpdateVersionRecordsActualEditor verifies that version snapshot records the actual editor.
func TestUpdateVersionRecordsActualEditor(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "原始标题", "原始内容")

	body := map[string]interface{}{"content": "新内容"}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var version model.SharedDocumentVersion
	if err := db.Where("document_id = ?", doc.ID).First(&version).Error; err != nil {
		t.Fatalf("expected version record, got %v", err)
	}
	if version.EditedBy != "user-1" {
		t.Errorf("expected EditedBy=user-1, got %s", version.EditedBy)
	}
}

// ===== Phase 2 版本对比测试 =====

func TestCompareVersions(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "原始内容")

	// 更新两次，产生版本 1 和 2 的历史
	for _, content := range []string{"修改后内容", "最终内容"} {
		body := map[string]interface{}{"content": content}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest("PATCH", "/api/v1/sharedoc/"+doc.ID, bytes.NewBuffer(jsonBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("update failed: %d %s", w.Code, w.Body.String())
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/versions/compare?a=1&b=2", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	if data["a"].(float64) != 1 || data["b"].(float64) != 2 {
		t.Errorf("unexpected version numbers: %v", data)
	}
	diff, _ := data["diff"].([]interface{})
	if len(diff) == 0 {
		t.Errorf("expected non-empty diff")
	}
}

func TestCompareVersionsMissingParams(t *testing.T) {
	_, r, db := setupTestHandler()
	doc := createTestDoc(db, "标题", "内容")

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"/versions/compare", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing params, got %d", w.Code)
	}
}
