package cloudfs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/internal/config"
	"ridgericetalk/tests/testutil"
)

// TestRegisterRoutesWiresCloudFSEndpoints exercises the production registration
// path. The convenience test router injects an identity directly, so it would
// not catch a typo or a routing conflict in RegisterRoutes — nor a route that
// accidentally escapes AuthRequired.
func TestRegisterRoutesWiresCloudFSEndpoints(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := &config.Config{LocalDataPath: t.TempDir()}
	h := NewHandler(db, cfg, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.RegisterRoutes(r.Group("/api/v1"))

	want := map[string]string{
		"GET /api/v1/cloudfs/list":           "",
		"POST /api/v1/cloudfs/folder":        "",
		"POST /api/v1/cloudfs/upload":        "",
		"GET /api/v1/cloudfs/download":       "",
		"GET /api/v1/cloudfs/preview":        "",
		"DELETE /api/v1/cloudfs/delete":      "",
		"PATCH /api/v1/cloudfs/rename":       "",
		"POST /api/v1/cloudfs/move":          "",
		"GET /api/v1/cloudfs/trash":          "",
		"POST /api/v1/cloudfs/trash/restore": "",
		"DELETE /api/v1/cloudfs/trash":       "",
		"DELETE /api/v1/cloudfs/trash/all":   "",
		"GET /api/v1/cloudfs/search":         "",
		"GET /api/v1/cloudfs/usage":          "",
	}
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			delete(want, key)
		}
	}
	for missing := range want {
		t.Errorf("route not registered: %s", missing)
	}

	// Every new endpoint must stay behind AuthRequired: an unauthenticated call
	// must never reach the handler. (The concrete status is 401 or 503 depending
	// on whether the test DB looks initialised — both mean "blocked".)
	for _, req := range []struct {
		method string
		target string
	}{
		{"GET", "/api/v1/cloudfs/preview?path=/x"},
		{"PATCH", "/api/v1/cloudfs/rename"},
		{"POST", "/api/v1/cloudfs/move"},
		{"GET", "/api/v1/cloudfs/trash"},
		{"POST", "/api/v1/cloudfs/trash/restore"},
		{"DELETE", "/api/v1/cloudfs/trash"},
		{"DELETE", "/api/v1/cloudfs/trash/all"},
		{"GET", "/api/v1/cloudfs/search?q=x"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(req.method, req.target, nil))
		if w.Code == http.StatusOK {
			t.Errorf("%s %s must not be reachable without auth, got 200", req.method, req.target)
		}
	}
}
