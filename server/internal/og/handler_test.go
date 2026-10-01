package og

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/internal/config"
	"ridgericetalk/tests/testutil"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newTestHandler() (*Handler, *gin.Engine) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	h := NewHandler(db, cfg)
	r := gin.New()
	h.RegisterRoutes(r.Group("/api"))
	return h, r
}

func TestPreviewMissingURL(t *testing.T) {
	_, router := newTestHandler()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/preview", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for missing url, got %d, body: %s",
			http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestPreviewSSRFReturnsForbidden(t *testing.T) {
	_, router := newTestHandler()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/preview?url=http://127.0.0.1/", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status %d for SSRF url, got %d, body: %s",
			http.StatusForbidden, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SSRF") {
		t.Errorf("expected SSRF error in body, got: %s", w.Body.String())
	}
}

func TestPreviewFetchErrorReturnsEmptyPreview(t *testing.T) {
	_, router := newTestHandler()

	// A URL pointing to a closed port on a non-loopback host triggers a fetch
	// error but is not flagged as SSRF. Use a public-looking hostname that
	// won't resolve in tests — this produces a network error, gracefully
	// degraded to an empty preview.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/preview?url=https://nonexistent.invalid.example.com/page", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d on fetch error (graceful degradation), got %d, body: %s",
			http.StatusOK, w.Code, w.Body.String())
	}
	// Body should contain an empty data object (not an error code)
	if !strings.Contains(w.Body.String(), `"title":""`) {
		t.Errorf("expected empty title in graceful-degradation response, got: %s", w.Body.String())
	}
}

func TestPreviewRegisterRoutes(t *testing.T) {
	h, router := newTestHandler()
	if h == nil {
		t.Fatalf("handler is nil")
	}
	// Verify the route is registered
	routes := router.Routes()
	found := false
	for _, r := range routes {
		if r.Path == "/api/preview" && r.Method == "GET" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected /api/preview route registered, got routes: %v", routes)
	}
}
