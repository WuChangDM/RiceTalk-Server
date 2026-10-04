package sharedoc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// FIX-20261003-01 NJ-13：GET /sharedoc/:id 单文档详情。
// 列表接口（/sharedoc/list）出于轻量不返回 content/version，客户端打开文档、
// 冲突重载、WS 重连对齐都必须走本端点拿真实内容——否则他端打开恒为空白。

func TestGetReturnsFullDocContent(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "协作文档", "第一行内容\n第二行内容")

	req := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"?spaceId=space-1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Content string `json:"content"`
			Creator string `json:"creator"`
			OwnerID string `json:"ownerId"`
			Version int    `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Data.ID != doc.ID {
		t.Errorf("id = %q, want %q", resp.Data.ID, doc.ID)
	}
	if resp.Data.Content != "第一行内容\n第二行内容" {
		t.Errorf("content = %q, want full saved content", resp.Data.Content)
	}
	if resp.Data.Title != "协作文档" {
		t.Errorf("title = %q, want %q", resp.Data.Title, "协作文档")
	}
	if resp.Data.OwnerID != "user-1" {
		t.Errorf("ownerId = %q, want user-1", resp.Data.OwnerID)
	}
	if resp.Data.Version != 1 {
		t.Errorf("version = %d, want 1", resp.Data.Version)
	}
}

func TestGetNotFoundAndCrossSpaceHidden(t *testing.T) {
	_, r, db := setupTestHandler()

	doc := createTestDoc(db, "跨空间文档", "secret")

	// 不存在的文档 → NOT_FOUND
	req := httptest.NewRequest("GET", "/api/v1/sharedoc/doc_missing?spaceId=space-1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing doc: expected 404, got %d", w.Code)
	}

	// 文档存在但属于其他空间 → 同样 NOT_FOUND（不泄露存在性）
	doc.SpaceID = "space-other"
	if err := db.Save(doc).Error; err != nil {
		t.Fatalf("move doc to other space: %v", err)
	}
	req2 := httptest.NewRequest("GET", "/api/v1/sharedoc/"+doc.ID+"?spaceId=space-1", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("cross-space doc: expected 404, got %d", w2.Code)
	}
}
