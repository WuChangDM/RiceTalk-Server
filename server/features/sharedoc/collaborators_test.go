package sharedoc

// 1.9.4 A 方案：共享文档协作者（shared_document_collaborators）权限矩阵用例。
//
// 覆盖 coordinator 要求的核心语义：
//   - owner 全权；editor 可改内容、不可管协作者；viewer 只读；他人 403；
//   - 被移除的 editor 下一次 PATCH 立刻 403（角色判定必须查库，不得缓存）；
//   - 目标必须是同空间成员（否则 400），必须是存在的用户（否则 404）；
//   - 不能移除 owner；doc 不存在 404；
//   - GET /:id/editors 返回 owner + 协作者，带 role。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

const (
	collabOwner    = "user-1" // 文档 owner，也是 space owner
	collabEditor   = "user-2" // 空间成员，将被授为 editor
	collabViewer   = "user-3" // 空间成员，将被授为 viewer
	collabOutsider = "user-4" // 存在用户，但不是 space-1 成员
)

// setupCollabTestEngine 建一个可切换调用者的测试引擎：
// 请求头 X-Test-User 决定 c.Set("user_id", ...)，默认 user-1。
// 现有 setupTestHandler 固定 user-1，无法验证跨用户权限，故单独建。
func setupCollabTestEngine(t *testing.T) (*Handler, *gin.Engine, *gorm.DB) {
	t.Helper()

	db := testutil.MustSetupTestDB()
	createTestSpaceAndMembership(db)

	// 用户表：user-1/2/3 为空间成员，user-4 是空间外用户。
	for _, u := range []model.User{
		{ID: collabOwner, Username: "owner", Email: "owner@test.com", PasswordHash: "x", DisplayName: "Owner"},
		{ID: collabEditor, Username: "editor", Email: "editor@test.com", PasswordHash: "x", DisplayName: "Editor"},
		{ID: collabViewer, Username: "viewer", Email: "viewer@test.com", PasswordHash: "x", DisplayName: "Viewer"},
		{ID: collabOutsider, Username: "outsider", Email: "outsider@test.com", PasswordHash: "x", DisplayName: "Outsider"},
	} {
		user := u
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("failed to create user %s: %v", user.ID, err)
		}
	}
	// user-3 加入空间（createTestSpaceAndMembership 只建 user-1/user-2）。
	if err := db.Create(&model.Membership{
		ID: "membership-user-3", UserID: collabViewer, SpaceID: "space-1", Role: "MEMBER",
	}).Error; err != nil {
		t.Fatalf("failed to create membership: %v", err)
	}

	h := NewHandler(db, &config.Config{}, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	sd := api.Group("/sharedoc")

	asUser := func(c *gin.Context) {
		uid := c.GetHeader("X-Test-User")
		if uid == "" {
			uid = collabOwner
		}
		c.Set("user_id", uid)
		c.Set("username", uid)
		c.Set("role", "MEMBER")
		c.Next()
	}

	sd.GET("/:id", asUser, h.Get)
	sd.PATCH("/:id", asUser, h.Update)
	sd.DELETE("/:id", asUser, h.Delete)
	sd.GET("/:id/editors", asUser, h.GetEditors)
	sd.POST("/:id/editors", asUser, h.AddCollaborator)
	sd.DELETE("/:id/editors/:userId", asUser, h.RemoveCollaborator)

	return h, r, db
}

// doJSON 以指定用户身份发请求，返回 recorder。
func doJSON(r *gin.Engine, method, path, asUser string, body interface{}) *httptest.ResponseRecorder {
	var buf *bytes.Buffer
	if body != nil {
		raw, _ := json.Marshal(body)
		buf = bytes.NewBuffer(raw)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", asUser)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func parseData(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v, body: %s", err, w.Body.String())
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data object, body: %s", w.Body.String())
	}
	return data
}

// TestAddCollaboratorIdempotentAndRoleUpdate：重复邀请幂等，仅覆盖 role。
func TestAddCollaboratorIdempotentAndRoleUpdate(t *testing.T) {
	_, r, db := setupCollabTestEngine(t)
	doc := createTestDoc(db, "标题", "内容")

	w := doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabOwner,
		map[string]interface{}{"userId": collabEditor, "role": "editor"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on invite, got %d, body: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&model.SharedDocumentCollaborator{}).Where("doc_id = ?", doc.ID).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 collaborator row, got %d", count)
	}

	// 再次邀请同一个人、改成 viewer：应更新而不是插新行。
	w = doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabOwner,
		map[string]interface{}{"userId": collabEditor, "role": "viewer"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on re-invite, got %d, body: %s", w.Code, w.Body.String())
	}
	db.Model(&model.SharedDocumentCollaborator{}).Where("doc_id = ?", doc.ID).Count(&count)
	if count != 1 {
		t.Fatalf("expected still 1 collaborator row after re-invite, got %d", count)
	}
	var row model.SharedDocumentCollaborator
	if err := db.Where("doc_id = ? AND user_id = ?", doc.ID, collabEditor).First(&row).Error; err != nil {
		t.Fatalf("failed to load collaborator: %v", err)
	}
	if row.Role != "viewer" {
		t.Errorf("expected role updated to viewer, got %q", row.Role)
	}
}

// TestEditorCanUpdateDocAndViewerCannot：editor 能 PATCH，viewer 403 且内容不变。
func TestEditorCanUpdateDocAndViewerCannot(t *testing.T) {
	_, r, db := setupCollabTestEngine(t)
	doc := createTestDoc(db, "标题", "原始内容")

	if w := doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabOwner,
		map[string]interface{}{"userId": collabEditor, "role": "editor"}); w.Code != http.StatusOK {
		t.Fatalf("invite editor failed: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabOwner,
		map[string]interface{}{"userId": collabViewer, "role": "viewer"}); w.Code != http.StatusOK {
		t.Fatalf("invite viewer failed: %d %s", w.Code, w.Body.String())
	}

	// editor 可改
	w := doJSON(r, "PATCH", "/api/v1/sharedoc/"+doc.ID, collabEditor,
		map[string]interface{}{"content": "editor 修改后"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected editor PATCH 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var after model.SharedDocument
	db.First(&after, "id = ?", doc.ID)
	if after.Content != "editor 修改后" {
		t.Errorf("expected content updated by editor, got %q", after.Content)
	}
	if after.Version != 2 {
		t.Errorf("expected Version=2 after editor update, got %d", after.Version)
	}

	// viewer 不可改，且内容不变
	w = doJSON(r, "PATCH", "/api/v1/sharedoc/"+doc.ID, collabViewer,
		map[string]interface{}{"content": "viewer 不该成功"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected viewer PATCH 403, got %d, body: %s", w.Code, w.Body.String())
	}
	var unchanged model.SharedDocument
	db.First(&unchanged, "id = ?", doc.ID)
	if unchanged.Content != "editor 修改后" {
		t.Errorf("expected content unchanged after viewer PATCH, got %q", unchanged.Content)
	}
	if unchanged.Version != 2 {
		t.Errorf("expected Version still 2 after rejected viewer PATCH, got %d", unchanged.Version)
	}
}

// TestRemovedEditorLosesEditImmediately：撤销授权后下一次 PATCH 立刻 403。
func TestRemovedEditorLosesEditImmediately(t *testing.T) {
	_, r, db := setupCollabTestEngine(t)
	doc := createTestDoc(db, "标题", "内容")

	if w := doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabOwner,
		map[string]interface{}{"userId": collabEditor, "role": "editor"}); w.Code != http.StatusOK {
		t.Fatalf("invite editor failed: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(r, "PATCH", "/api/v1/sharedoc/"+doc.ID, collabEditor,
		map[string]interface{}{"content": "第一次修改"}); w.Code != http.StatusOK {
		t.Fatalf("expected editor PATCH 200, got %d, body: %s", w.Code, w.Body.String())
	}

	if w := doJSON(r, "DELETE", "/api/v1/sharedoc/"+doc.ID+"/editors/"+collabEditor, collabOwner, nil); w.Code != http.StatusOK {
		t.Fatalf("expected remove collaborator 200, got %d, body: %s", w.Code, w.Body.String())
	}

	w := doJSON(r, "PATCH", "/api/v1/sharedoc/"+doc.ID, collabEditor,
		map[string]interface{}{"content": "被移除后不该成功"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 after removal, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestNonOwnerCannotManageCollaborators：editor 也不能增删协作者。
func TestNonOwnerCannotManageCollaborators(t *testing.T) {
	_, r, db := setupCollabTestEngine(t)
	doc := createTestDoc(db, "标题", "内容")

	if w := doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabOwner,
		map[string]interface{}{"userId": collabEditor, "role": "editor"}); w.Code != http.StatusOK {
		t.Fatalf("invite editor failed: %d %s", w.Code, w.Body.String())
	}

	if w := doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabEditor,
		map[string]interface{}{"userId": collabViewer, "role": "editor"}); w.Code != http.StatusForbidden {
		t.Errorf("expected editor invite 403, got %d, body: %s", w.Code, w.Body.String())
	}
	if w := doJSON(r, "DELETE", "/api/v1/sharedoc/"+doc.ID+"/editors/"+collabEditor, collabEditor, nil); w.Code != http.StatusForbidden {
		t.Errorf("expected editor remove 403, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestAddCollaboratorRejectsNonMember：目标不是同空间成员 → 400。
func TestAddCollaboratorRejectsNonMember(t *testing.T) {
	_, r, db := setupCollabTestEngine(t)
	doc := createTestDoc(db, "标题", "内容")

	w := doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabOwner,
		map[string]interface{}{"userId": collabOutsider, "role": "editor"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-member target, got %d, body: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&model.SharedDocumentCollaborator{}).Where("doc_id = ?", doc.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected no collaborator row for rejected invite, got %d", count)
	}
}

// TestAddCollaboratorRejectsUnknownUserAndBadRole / owner / doc 不存在。
func TestAddCollaboratorValidation(t *testing.T) {
	_, r, db := setupCollabTestEngine(t)
	doc := createTestDoc(db, "标题", "内容")

	cases := []struct {
		name   string
		docID  string
		user   string
		body   map[string]interface{}
		expect int
	}{
		{"unknown user", doc.ID, collabOwner, map[string]interface{}{"userId": "user-999", "role": "editor"}, http.StatusNotFound},
		{"bad role", doc.ID, collabOwner, map[string]interface{}{"userId": collabEditor, "role": "owner"}, http.StatusBadRequest},
		{"empty body", doc.ID, collabOwner, map[string]interface{}{"userId": ""}, http.StatusBadRequest},
		{"invite owner self", doc.ID, collabOwner, map[string]interface{}{"userId": collabOwner}, http.StatusBadRequest},
		{"doc not found", "no-such-doc", collabOwner, map[string]interface{}{"userId": collabEditor}, http.StatusNotFound},
	}
	for _, tc := range cases {
		w := doJSON(r, "POST", "/api/v1/sharedoc/"+tc.docID+"/editors", tc.user, tc.body)
		if w.Code != tc.expect {
			t.Errorf("%s: expected %d, got %d, body: %s", tc.name, tc.expect, w.Code, w.Body.String())
		}
	}
}

// TestRemoveOwnerRejectedAndRemoveMissing404。
func TestRemoveCollaboratorValidation(t *testing.T) {
	_, r, db := setupCollabTestEngine(t)
	doc := createTestDoc(db, "标题", "内容")

	if w := doJSON(r, "DELETE", "/api/v1/sharedoc/"+doc.ID+"/editors/"+collabOwner, collabOwner, nil); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when removing owner, got %d, body: %s", w.Code, w.Body.String())
	}

	// 未授权的用户：幂等删除语义 → 404（明确告知不存在，而不是静默成功）。
	if w := doJSON(r, "DELETE", "/api/v1/sharedoc/"+doc.ID+"/editors/"+collabEditor, collabOwner, nil); w.Code != http.StatusNotFound {
		t.Errorf("expected 404 removing non-collaborator, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestGetEditorsReturnsOwnerAndCollaboratorsWithRoles：owner 首位 + 角色。
func TestGetEditorsReturnsOwnerAndCollaboratorsWithRoles(t *testing.T) {
	_, r, db := setupCollabTestEngine(t)
	doc := createTestDoc(db, "标题", "内容")

	for _, inv := range []struct {
		user string
		role string
	}{
		{collabEditor, "editor"},
		{collabViewer, "viewer"},
	} {
		if w := doJSON(r, "POST", "/api/v1/sharedoc/"+doc.ID+"/editors", collabOwner,
			map[string]interface{}{"userId": inv.user, "role": inv.role}); w.Code != http.StatusOK {
			t.Fatalf("invite %s failed: %d %s", inv.user, w.Code, w.Body.String())
		}
	}

	w := doJSON(r, "GET", "/api/v1/sharedoc/"+doc.ID+"/editors", collabEditor, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	data := parseData(t, w)
	if data["count"].(float64) != 3 {
		t.Errorf("expected count=3, got %v", data["count"])
	}
	editors, _ := data["editors"].([]interface{})
	if len(editors) != 3 {
		t.Fatalf("expected 3 editors, got %d", len(editors))
	}
	roles := make(map[string]string, len(editors))
	for i, item := range editors {
		e, _ := item.(map[string]interface{})
		roles[e["userId"].(string)] = e["role"].(string)
		if i == 0 && e["role"] != "owner" {
			t.Errorf("expected owner first, got role=%v", e["role"])
		}
	}
	if roles[collabOwner] != "owner" || roles[collabEditor] != "editor" || roles[collabViewer] != "viewer" {
		t.Errorf("unexpected roles: %v", roles)
	}
}
