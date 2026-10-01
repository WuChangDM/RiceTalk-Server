package cloudfs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func init() {
	_ = idgen.Init(1, 1)
}

func setupTestHandler() (*Handler, *gin.Engine, *config.Config, *gorm.DB) {
	return setupTestHandlerFor("user-1", "MEMBER")
}

// setupTestHandlerFor is setupTestHandler with a configurable authenticated
// identity, used by permission-boundary tests. Both "user-1" and userID are
// members of the space (pass role "" to leave userID without membership, which
// exercises the non-member denial path).
func setupTestHandlerFor(userID, role string) (*Handler, *gin.Engine, *config.Config, *gorm.DB) {
	db := testutil.MustSetupTestDB()
	cfg := &config.Config{LocalDataPath: "/tmp/rrt_test_cloudfs"}
	h := NewHandler(db, cfg, nil)

	// Create a space and membership for user-1 so download permission check passes
	space := &model.Space{ID: idgen.NextString(), Name: "test-space", OwnerID: "user-1"}
	db.Create(space)
	db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: "user-1", Role: "MEMBER"})
	if userID != "user-1" && role != "" {
		db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: userID, Role: role})
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	cf := api.Group("/cloudfs")
	auth := func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("role", role)
		c.Set("space_id", space.ID)
	}
	{
		cf.GET("/list", auth, h.List)
		cf.POST("/folder", auth, h.CreateFolder)
		cf.POST("/upload", auth, h.Upload)
		cf.GET("/download", auth, h.Download)
		cf.GET("/preview", auth, h.Preview)
		cf.DELETE("/delete", auth, h.Delete)
		cf.PATCH("/rename", auth, h.Rename)
		cf.POST("/move", auth, h.Move)
		cf.GET("/trash", auth, h.ListTrash)
		cf.POST("/trash/restore", auth, h.RestoreTrash)
		cf.DELETE("/trash", auth, h.PurgeTrash)
		cf.DELETE("/trash/all", auth, h.EmptyTrash)
		cf.GET("/search", auth, h.Search)
		cf.GET("/usage", auth, h.GetUsage) // L17: 存储配额 API
	}
	return h, r, cfg, db
}

func TestCreateFolderWithParent(t *testing.T) {
	_, r, _, db := setupTestHandler()

	// Create root folder
	body1 := map[string]interface{}{"path": "/", "name": "docs"}
	jsonBody, _ := json.Marshal(body1)
	req := httptest.NewRequest("POST", "/api/v1/cloudfs/folder", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create root folder failed: %d, %s", w.Code, w.Body.String())
	}

	// Verify root folder has no parent
	var rootFolder model.SharedFolder
	if err := db.Where("name = ?", "docs").First(&rootFolder).Error; err != nil {
		t.Fatalf("root folder not found: %v", err)
	}
	if rootFolder.ParentID != nil {
		t.Error("expected root folder to have nil ParentID")
	}

	// Create subfolder
	body2 := map[string]interface{}{"path": "/docs", "name": "sub"}
	jsonBody, _ = json.Marshal(body2)
	req = httptest.NewRequest("POST", "/api/v1/cloudfs/folder", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create subfolder failed: %d, %s", w.Code, w.Body.String())
	}

	// Verify subfolder has correct parent
	var subFolder model.SharedFolder
	if err := db.Where("name = ?", "sub").First(&subFolder).Error; err != nil {
		t.Fatalf("sub folder not found: %v", err)
	}
	if subFolder.ParentID == nil {
		t.Fatal("expected subfolder to have ParentID")
	}
	if *subFolder.ParentID != rootFolder.ID {
		t.Errorf("expected ParentID=%s, got %s", rootFolder.ID, *subFolder.ParentID)
	}
	if subFolder.Path != "/docs/sub" {
		t.Errorf("expected Path=/docs/sub, got %s", subFolder.Path)
	}
}

func TestUploadToFolder(t *testing.T) {
	_, r, _, db := setupTestHandler()

	// Create folder
	body := map[string]interface{}{"path": "/", "name": "uploads"}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/cloudfs/folder", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var folder model.SharedFolder
	db.Where("name = ?", "uploads").First(&folder)

	// Upload file to folder
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("path", "/uploads")
	part, _ := writer.CreateFormFile("file", "test.txt")
	part.Write([]byte("hello world"))
	writer.Close()

	req = httptest.NewRequest("POST", "/api/v1/cloudfs/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("upload failed: %d, %s", w.Code, w.Body.String())
	}

	// Verify file is associated with folder
	var entry model.SharedFileEntry
	if err := db.Where("file_name = ?", "test.txt").First(&entry).Error; err != nil {
		t.Fatalf("file entry not found: %v", err)
	}
	if entry.FolderID != folder.ID {
		t.Errorf("expected FolderID=%s, got %s", folder.ID, entry.FolderID)
	}
	if entry.PhysicalName == "" {
		t.Error("expected PhysicalName to be set")
	}
	if entry.FilePath != "/uploads/test.txt" {
		t.Errorf("expected FilePath=/uploads/test.txt, got %s", entry.FilePath)
	}
}

func TestListShowsFolderContents(t *testing.T) {
	_, r, _, db := setupTestHandler()

	var space model.Space
	if err := db.First(&space).Error; err != nil {
		t.Fatalf("space not found: %v", err)
	}

	// Create folder and subfolder
	if err := db.Create(&model.SharedFolder{ID: idgen.NextString(), SpaceID: space.ID, Name: "docs", Path: "/docs", OwnerID: "user-1"}).Error; err != nil {
		t.Fatalf("failed to create docs folder: %v", err)
	}
	var docsFolder model.SharedFolder
	if err := db.Where("name = ?", "docs").First(&docsFolder).Error; err != nil {
		t.Fatalf("docs folder not found: %v", err)
	}
	t.Logf("docs folder ID: %s, ParentID: %v", docsFolder.ID, docsFolder.ParentID)

	// Manual query to verify DB state
	var manualFolders []model.SharedFolder
	if err := db.Where("parent_id IS NULL").Find(&manualFolders).Error; err != nil {
		t.Logf("manual query error: %v", err)
	}
	t.Logf("manual query folders: %d", len(manualFolders))

	if err := db.Create(&model.SharedFolder{ID: idgen.NextString(), SpaceID: space.ID, Name: "sub", Path: "/docs/sub", ParentID: &docsFolder.ID, OwnerID: "user-1"}).Error; err != nil {
		t.Fatalf("failed to create sub folder: %v", err)
	}
	if err := db.Create(&model.SharedFileEntry{ID: idgen.NextString(), SpaceID: space.ID, FolderID: docsFolder.ID, FileName: "readme.txt", FilePath: "/docs/readme.txt", FileSize: 100, UploadedBy: "user-1"}).Error; err != nil {
		t.Fatalf("failed to create file entry: %v", err)
	}

	// List root
	req := httptest.NewRequest("GET", "/api/v1/cloudfs/list?path=/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	t.Logf("list root response: %s", w.Body.String())
	if w.Code != http.StatusOK {
		t.Fatalf("list root failed: %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	items, _ := data["items"].([]interface{})
	if len(items) != 1 {
		t.Errorf("expected 1 item in root, got %d", len(items))
	}

	// List /docs
	req = httptest.NewRequest("GET", "/api/v1/cloudfs/list?path=/docs", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list docs failed: %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ = resp["data"].(map[string]interface{})
	items, _ = data["items"].([]interface{})
	if len(items) != 2 {
		t.Errorf("expected 2 items in /docs (1 subfolder + 1 file), got %d", len(items))
	}
}

func TestDownloadWithPhysicalName(t *testing.T) {
	_, r, cfg, db := setupTestHandler()

	var space model.Space
	if err := db.First(&space).Error; err != nil {
		t.Fatalf("space not found: %v", err)
	}

	// Create a file entry with PhysicalName
	entry := model.SharedFileEntry{
		ID:           idgen.NextString(),
		SpaceID:      space.ID,
		FolderID:     "",
		FileName:     "test.txt",
		FilePath:     "/test.txt",
		PhysicalName: "phys_123_test.txt",
		FileSize:     11,
		MimeType:     "text/plain",
		UploadedBy:   "user-1",
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("failed to create file entry: %v", err)
	}

	// Create physical file in the correct baseDir/cloudfs path
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")
	os.MkdirAll(baseDir, 0755)
	if err := os.WriteFile(filepath.Join(baseDir, "phys_123_test.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatalf("failed to write physical file: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/cloudfs/download?path=/test.txt", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("download failed: %d, %s", w.Code, w.Body.String())
	}
	body, _ := io.ReadAll(w.Result().Body)
	if string(body) != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", string(body))
	}
}

func TestDeleteFolder(t *testing.T) {
	_, r, _, db := setupTestHandler()

	var space model.Space
	if err := db.First(&space).Error; err != nil {
		t.Fatalf("space not found: %v", err)
	}

	// Create folder hierarchy
	root := model.SharedFolder{ID: idgen.NextString(), SpaceID: space.ID, Name: "root", Path: "/root", OwnerID: "user-1"}
	db.Create(&root)
	sub := model.SharedFolder{ID: idgen.NextString(), SpaceID: space.ID, Name: "sub", Path: "/root/sub", ParentID: &root.ID, OwnerID: "user-1"}
	db.Create(&sub)
	db.Create(&model.SharedFileEntry{ID: idgen.NextString(), SpaceID: space.ID, FolderID: sub.ID, FileName: "file.txt", FilePath: "/root/sub/file.txt", UploadedBy: "user-1"})

	req := httptest.NewRequest("DELETE", "/api/v1/cloudfs/delete?path=/root", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete failed: %d", w.Code)
	}

	// Verify all deleted
	var count int64
	db.Model(&model.SharedFolder{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 folders, got %d", count)
	}
	db.Model(&model.SharedFileEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 files, got %d", count)
	}
}

func TestConcurrentCloudFSOperations(t *testing.T) {
	_, r, _, db := setupTestHandler()

	// Create folder first
	body := map[string]interface{}{"path": "/", "name": "concurrent"}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/cloudfs/folder", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var wg sync.WaitGroup
	errors := make(chan error, 200)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			var buf bytes.Buffer
			writer := multipart.NewWriter(&buf)
			_ = writer.WriteField("path", "/concurrent")
			part, _ := writer.CreateFormFile("file", fmt.Sprintf("file%d.txt", idx))
			part.Write([]byte(strings.Repeat("a", 50)))
			writer.Close()

			req := httptest.NewRequest("POST", "/api/v1/cloudfs/upload", &buf)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				errors <- fmt.Errorf("upload %d: status %d", idx, w.Code)
			}
		}(i)
	}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/v1/cloudfs/list?path=/concurrent", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				errors <- fmt.Errorf("list %d: status %d", idx, w.Code)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	errCount := 0
	for err := range errors {
		t.Logf("concurrent error: %v", err)
		errCount++
	}
	if errCount > 0 {
		t.Errorf("got %d errors during concurrent cloudfs operations", errCount)
	}

	// Verify all files have correct folder association
	var files []model.SharedFileEntry
	db.Where("file_name LIKE ?", "file%.txt").Find(&files)
	var folder model.SharedFolder
	db.Where("name = ?", "concurrent").First(&folder)

	for _, f := range files {
		if f.FolderID != folder.ID {
			t.Errorf("file %s has wrong FolderID: expected %s, got %s", f.FileName, folder.ID, f.FolderID)
		}
	}
}

// TestGetUsage verifies L17: GET /cloudfs/usage returns used/quota/usedPercent
func TestGetUsage(t *testing.T) {
	_, r, _, db := setupTestHandler()

	var space model.Space
	if err := db.First(&space).Error; err != nil {
		t.Fatalf("space not found: %v", err)
	}

	// Seed some file entries to compute used bytes
	db.Create(&model.SharedFileEntry{
		ID:         idgen.NextString(),
		SpaceID:    space.ID,
		FileName:   "a.txt",
		FilePath:   "/a.txt",
		FileSize:   100,
		UploadedBy: "user-1",
	})
	db.Create(&model.SharedFileEntry{
		ID:         idgen.NextString(),
		SpaceID:    space.ID,
		FileName:   "b.txt",
		FilePath:   "/b.txt",
		FileSize:   200,
		UploadedBy: "user-1",
	})

	req := httptest.NewRequest("GET", "/api/v1/cloudfs/usage", nil)
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
	used, ok := data["used"]
	if !ok {
		t.Fatal("expected 'used' field in response")
	}
	if _, ok := data["quota"]; !ok {
		t.Error("expected 'quota' field in response")
	}
	if _, ok := data["usedPercent"]; !ok {
		t.Error("expected 'usedPercent' field in response")
	}
	// used should be at least 300 (100 + 200 from seeded entries)
	usedFloat, _ := used.(float64)
	if usedFloat < 300 {
		t.Errorf("expected used >= 300, got %v", used)
	}
}

// TestUploadFillsFileExtAndVersionNo verifies L16: Upload fills FileExt and VersionNo
func TestUploadFillsFileExtAndVersionNo(t *testing.T) {
	_, r, _, db := setupTestHandler()

	// Upload a .txt file
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("path", "/")
	part, _ := writer.CreateFormFile("file", "report.pdf")
	part.Write([]byte("fake pdf content"))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/v1/cloudfs/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("upload failed: %d, %s", w.Code, w.Body.String())
	}

	var entry model.SharedFileEntry
	if err := db.Where("file_name = ?", "report.pdf").First(&entry).Error; err != nil {
		t.Fatalf("file entry not found: %v", err)
	}
	// L16: FileExt should be ".pdf"
	if entry.FileExt != ".pdf" {
		t.Errorf("expected FileExt='.pdf', got '%s'", entry.FileExt)
	}
	// L16: VersionNo should default to 1
	if entry.VersionNo != 1 {
		t.Errorf("expected VersionNo=1, got %d", entry.VersionNo)
	}
}
