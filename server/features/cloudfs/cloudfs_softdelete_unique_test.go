package cloudfs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ridgericetalk/internal/model"
)

// TestUploadSameNameAfterDelete is the regression test for DES-2026-0912-02
// §2.5 defect ①: the unique indexes idx_file_folder_name / idx_folder_parent_name
// were FULL unique indexes over soft-deleted rows, so re-uploading (or
// re-creating) an item whose previous incarnation was deleted failed with
// SYSTEM_CONFLICT even though the row is invisible to every query.
//
// Expected behaviour after the fix (partial unique indexes WHERE deleted_at IS
// NULL): the second upload of the same name succeeds and only one live row
// exists.
func TestUploadSameNameAfterDelete(t *testing.T) {
	_, r, _, db := setupTestHandler()

	// 1. Upload /report.txt at the root (folder_id = '').
	if w := uploadFile(t, r, "/", "report.txt", "v1"); w.Code != http.StatusOK {
		t.Fatalf("first upload failed: %d, %s", w.Code, w.Body.String())
	}

	// 2. Delete it. deleteFileEntry uses GORM soft delete on SharedFileEntry
	//    (the row survives with deleted_at set) while removing the physical file.
	req := httptest.NewRequest("DELETE", "/api/v1/cloudfs/delete?path=/report.txt", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete failed: %d, %s", w.Code, w.Body.String())
	}

	// The soft-deleted row must still be present (this is what triggers the bug).
	var total int64
	if err := db.Unscoped().Model(&model.SharedFileEntry{}).
		Where("file_name = ?", "report.txt").Count(&total).Error; err != nil {
		t.Fatalf("count unscoped entries: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected 1 soft-deleted row behind the scenes, got %d", total)
	}

	// 3. Re-upload the same name into the same folder.
	w = uploadFile(t, r, "/", "report.txt", "v2")
	if w.Code != http.StatusOK {
		var body map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		t.Fatalf("re-upload of a deleted name must succeed, got %d, body: %s (errorCode=%v)",
			w.Code, w.Body.String(), body["errorCode"])
	}

	// 4. Exactly one live entry, and it holds the new content.
	var live []model.SharedFileEntry
	if err := db.Where("file_name = ?", "report.txt").Find(&live).Error; err != nil {
		t.Fatalf("query live entries: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("expected 1 live entry after re-upload, got %d", len(live))
	}

	dw := httptest.NewRecorder()
	r.ServeHTTP(dw, httptest.NewRequest("GET", "/api/v1/cloudfs/download?path=/report.txt", nil))
	if dw.Code != http.StatusOK {
		t.Fatalf("download after re-upload failed: %d", dw.Code)
	}
	if got := dw.Body.String(); got != "v2" {
		t.Errorf("expected re-uploaded content 'v2', got %q", got)
	}
}

// TestCloudFSPartialUniqueIndexDefinition asserts the DB-level half of the fix:
// both CloudFS unique indexes must be PARTIAL (filtered on deleted_at IS NULL),
// not full indexes over every row. Without this, the behaviour tests above could
// pass for the wrong reason.
func TestCloudFSPartialUniqueIndexDefinition(t *testing.T) {
	_, _, _, db := setupTestHandler()

	for _, name := range []string{"idx_file_folder_name", "idx_folder_parent_name"} {
		var sql string
		if err := db.Raw("SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='index' AND name=?", name).
			Scan(&sql).Error; err != nil {
			t.Fatalf("read index %s: %v", name, err)
		}
		if sql == "" {
			t.Fatalf("index %s not found", name)
		}
		if !strings.Contains(strings.ToUpper(sql), "WHERE DELETED_AT IS NULL") {
			t.Errorf("index %s is not partial; got %q", name, sql)
		}
	}
}

// TestCreateFolderSameNameAfterDelete covers the folder half of defect ①
// (idx_folder_parent_name). Root folders keep parent_id NULL, which unique
// indexes treat as distinct, so this uses a sub-folder to exercise the index.
func TestCreateFolderSameNameAfterDelete(t *testing.T) {
	_, r, _, db := setupTestHandler()

	create := func(parent, name string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]interface{}{"path": parent, "name": name})
		req := httptest.NewRequest("POST", "/api/v1/cloudfs/folder", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := create("/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create /docs failed: %d, %s", w.Code, w.Body.String())
	}
	if w := create("/docs", "sub"); w.Code != http.StatusOK {
		t.Fatalf("create /docs/sub failed: %d, %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest("DELETE", "/api/v1/cloudfs/delete?path=/docs/sub", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete /docs/sub failed: %d, %s", w.Code, w.Body.String())
	}

	var softDeleted int64
	if err := db.Unscoped().Model(&model.SharedFolder{}).
		Where("name = ?", "sub").Count(&softDeleted).Error; err != nil {
		t.Fatalf("count unscoped folders: %v", err)
	}
	if softDeleted != 1 {
		t.Fatalf("expected 1 soft-deleted folder row behind the scenes, got %d", softDeleted)
	}

	if w := create("/docs", "sub"); w.Code != http.StatusOK {
		t.Fatalf("re-creating a deleted folder name must succeed, got %d, body: %s", w.Code, w.Body.String())
	}

	var live int64
	if err := db.Model(&model.SharedFolder{}).Where("name = ?", "sub").Count(&live).Error; err != nil {
		t.Fatalf("count live folders: %v", err)
	}
	if live != 1 {
		t.Fatalf("expected 1 live folder named 'sub', got %d", live)
	}
}
