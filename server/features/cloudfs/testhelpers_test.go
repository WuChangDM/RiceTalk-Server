package cloudfs

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
)

// firstSpace returns the single space created by setupTestHandler*.
func firstSpace(t *testing.T, db *gorm.DB) model.Space {
	t.Helper()
	var space model.Space
	if err := db.First(&space).Error; err != nil {
		t.Fatalf("space not found: %v", err)
	}
	return space
}

// jsonRequest performs a JSON request against the test router.
func jsonRequest(t *testing.T, r http.Handler, method, target string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// createFolderHTTP creates a folder through the API.
func createFolderHTTP(t *testing.T, r http.Handler, parent, name string) *httptest.ResponseRecorder {
	t.Helper()
	return jsonRequest(t, r, "POST", "/api/v1/cloudfs/folder",
		map[string]interface{}{"path": parent, "name": name})
}

// seedStoredFile writes content into the cloudfs baseDir under a generated
// physical name and inserts a matching SharedFileEntry. MimeType is set
// explicitly so preview decisions are independent of the OS MIME database.
func seedStoredFile(t *testing.T, db *gorm.DB, baseDir, spaceID, folderID, name, filePath, mimeType, content string) *model.SharedFileEntry {
	t.Helper()
	physical := idgen.NextString() + "_" + name
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		t.Fatalf("mkdir baseDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, physical), []byte(content), 0644); err != nil {
		t.Fatalf("write physical file: %v", err)
	}
	entry := &model.SharedFileEntry{
		ID:           idgen.GenerateID(idgen.PrefixFile),
		SpaceID:      spaceID,
		FolderID:     folderID,
		FileName:     name,
		FilePath:     filePath,
		PhysicalName: physical,
		FileSize:     int64(len(content)),
		MimeType:     mimeType,
		FileExt:      filepath.Ext(name),
		VersionNo:    1,
		UploadedBy:   "user-1",
	}
	if err := db.Create(entry).Error; err != nil {
		t.Fatalf("create file entry: %v", err)
	}
	return entry
}

// uploadFile is a small helper that POSTs a multipart upload of name/path with
// the given content through the test router.
func uploadFile(t *testing.T, r http.Handler, uploadPath, name, content string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("path", uploadPath)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = part.Write([]byte(content))
	_ = writer.Close()

	req := httptest.NewRequest("POST", "/api/v1/cloudfs/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
