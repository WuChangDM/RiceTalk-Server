package files

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/storage"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// fakeStorage is an in-memory implementation of storage.Storage for testing.
type fakeStorage struct {
	files map[string][]byte
	types map[string]string
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		files: make(map[string][]byte),
		types: make(map[string]string),
	}
}

func (s *fakeStorage) Put(key string, reader io.Reader, size int64, contentType string) (string, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	s.files[key] = data
	s.types[key] = contentType
	return key, nil
}

func (s *fakeStorage) Get(key string) (io.ReadCloser, int64, string, error) {
	data, ok := s.files[key]
	if !ok {
		return nil, 0, "", &storageError{msg: "not found"}
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), s.types[key], nil
}

func (s *fakeStorage) Delete(key string) error {
	delete(s.files, key)
	delete(s.types, key)
	return nil
}

func (s *fakeStorage) Exists(key string) bool {
	_, ok := s.files[key]
	return ok
}

func (s *fakeStorage) URL(key string) string {
	return "http://localhost:8080/files/" + key
}

type storageError struct{ msg string }

func (e *storageError) Error() string { return e.msg }

// Compile-time interface check.
var _ storage.Storage = (*fakeStorage)(nil)

func setupAuthContext(c *gin.Context, userID, username, role string) {
	c.Set("user_id", userID)
	c.Set("username", username)
	c.Set("email", username+"@example.com")
	c.Set("role", role)
}

// minimalPNG is a 1x1 PNG file used for upload tests.
var minimalPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
	0x42, 0x60, 0x82,
}

func TestUploadFileSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	st := newFakeStorage()
	handler := NewHandler(db, st, cfg)

	router := gin.New()
	router.POST("/files/upload", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.Upload)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "test.png")
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	part.Write(minimalPNG)
	writer.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/files/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestUploadFileNoFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	st := newFakeStorage()
	handler := NewHandler(db, st, cfg)

	router := gin.New()
	router.POST("/files/upload", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.Upload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/files/upload", strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=---boundary")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for missing file, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestUploadFileUnsupportedType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	st := newFakeStorage()
	handler := NewHandler(db, st, cfg)

	router := gin.New()
	router.POST("/files/upload", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.Upload)

	// 二进制内容：既匹配不到任何已知魔数，又因含 NUL 字节无法被归类为
	// text/plain（text/plain 是 Upload 明确允许的类型之一，见 handler.go）。
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test.bin")
	part.Write([]byte{'M', 'Z', 0x00, 0x01, 0x02, 0x03, 0x04, 0x05})
	writer.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/files/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for unsupported file type, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestDownloadFileSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	st := newFakeStorage()
	// Pre-populate a file. The route /files/:filepath matches a single path
	// segment (no slashes), so use a flat key.
	st.files["test.png"] = minimalPNG
	st.types["test.png"] = "image/png"
	handler := NewHandler(db, st, cfg)

	router := gin.New()
	router.GET("/files/:filepath", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.Download)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/files/test.png", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), minimalPNG) {
		t.Errorf("downloaded content does not match uploaded file")
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("expected Content-Type image/png, got %q", ct)
	}
}

func TestDownloadFileNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	st := newFakeStorage()
	handler := NewHandler(db, st, cfg)

	router := gin.New()
	router.GET("/files/:filepath", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.Download)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/files/nonexistent.png", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status %d for missing file, got %d", http.StatusNotFound, w.Code)
	}
}

func TestDownloadFileEmptyPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	st := newFakeStorage()
	handler := NewHandler(db, st, cfg)

	router := gin.New()
	router.GET("/files/:filepath", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.Download)

	// Gin requires a non-empty path segment; this hits the route with a path
	// that resolves to an empty filepath param via the handler logic.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/files/", nil)
	router.ServeHTTP(w, req)

	// Expect 404 (route not matched) or handler returns 404 for empty path
	if w.Code != http.StatusNotFound && w.Code != http.StatusMovedPermanently {
		t.Errorf("expected status %d or redirect for empty path, got %d", http.StatusNotFound, w.Code)
	}
}
