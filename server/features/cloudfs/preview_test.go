package cloudfs

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// previewRequest performs a GET /cloudfs/preview with the given query string.
func previewRequest(t *testing.T, r http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cloudfs/preview"+query, nil))
	return w
}

// TestPreviewInlineForWhitelistedTypes covers the design's inline whitelist
// (DES-2026-0912-02 §4.1): images, text, PDF and media render inline.
func TestPreviewInlineForWhitelistedTypes(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")

	cases := []struct {
		name     string
		mime     string
		content  string
		wantType string
	}{
		{"pic.png", "image/png", "PNGDATA", "image/png"},
		{"notes.txt", "text/plain; charset=utf-8", "hello", "text/plain; charset=utf-8"},
		{"doc.pdf", "application/pdf", "%PDF-1.4", "application/pdf"},
		{"data.json", "application/json", "{}", "application/json"},
		{"clip.mp4", "video/mp4", "MP4", "video/mp4"},
	}
	for _, tc := range cases {
		seedStoredFile(t, db, baseDir, space.ID, "", tc.name, "/"+tc.name, tc.mime, tc.content)

		w := previewRequest(t, r, "?path=/"+tc.name)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d (%s)", tc.name, w.Code, w.Body.String())
		}
		if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") {
			t.Errorf("%s: expected inline disposition, got %q", tc.name, cd)
		}
		if ct := w.Header().Get("Content-Type"); ct != tc.wantType {
			t.Errorf("%s: expected Content-Type %q, got %q", tc.name, tc.wantType, ct)
		}
		if got := w.Body.String(); got != tc.content {
			t.Errorf("%s: expected body %q, got %q", tc.name, tc.content, got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: expected nosniff, got %q", tc.name, got)
		}
	}
}

// TestPreviewAttachmentForNonWhitelistedTypes verifies everything outside the
// whitelist falls back to a forced download.
func TestPreviewAttachmentForNonWhitelistedTypes(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")

	for _, tc := range []struct{ name, mime string }{
		{"archive.zip", "application/zip"},
		{"binary.bin", "application/octet-stream"},
		{"office.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	} {
		seedStoredFile(t, db, baseDir, space.ID, "", tc.name, "/"+tc.name, tc.mime, "data")

		w := previewRequest(t, r, "?path=/"+tc.name)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", tc.name, w.Code)
		}
		if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
			t.Errorf("%s: expected attachment disposition, got %q", tc.name, cd)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("%s: expected octet-stream fallback, got %q", tc.name, ct)
		}
	}
}

// TestPreviewForcesActiveContentToDownload pins the two deliberate deviations
// from the design's literal whitelist: SVG (design) and HTML (safer than the
// design's broad text/* rule, which would allow inline HTML = stored XSS).
func TestPreviewForcesActiveContentToDownload(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")

	for _, tc := range []struct{ name, mime string }{
		{"vector.svg", "image/svg+xml"},
		{"page.html", "text/html; charset=utf-8"},
		{"xhtml.xhtml", "application/xhtml+xml"},
	} {
		seedStoredFile(t, db, baseDir, space.ID, "", tc.name, "/"+tc.name, tc.mime, "<svg/>")

		w := previewRequest(t, r, "?path=/"+tc.name)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", tc.name, w.Code)
		}
		if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
			t.Errorf("%s: active content must not be inlined, got %q", tc.name, cd)
		}
	}
}

// TestPreviewSupportsRangeRequests verifies byte-range serving (needed for video
// seeking / PDF chunking) comes for free from c.File -> http.ServeContent.
func TestPreviewSupportsRangeRequests(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")
	seedStoredFile(t, db, baseDir, space.ID, "", "range.txt", "/range.txt", "text/plain", "hello world")

	req := httptest.NewRequest("GET", "/api/v1/cloudfs/preview?path=/range.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusPartialContent {
		t.Fatalf("expected 206 for a range request, got %d (%s)", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "hello" {
		t.Errorf("expected partial body %q, got %q", "hello", got)
	}
	if cr := w.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 0-4/") {
		t.Errorf("expected Content-Range header, got %q", cr)
	}
}

// TestPreviewRejectsMissingAndTraversalPaths documents the path handling: an
// empty path is a 400, and a traversal-looking path is normalised by
// path.Clean and then simply not found (it can never escape the space, since
// lookups go through the DB and the physical path is prefix-checked).
func TestPreviewRejectsMissingAndTraversalPaths(t *testing.T) {
	_, r, _, _ := setupTestHandler()

	if w := previewRequest(t, r, ""); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a missing path, got %d", w.Code)
	}
	if w := previewRequest(t, r, "?path=/../../etc/passwd"); w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for a traversal path, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestPreviewRequiresMembership verifies the same authorisation as Download:
// a user outside the space cannot preview (the route itself sits behind
// AuthRequired, which the test router emulates by injecting an identity).
func TestPreviewRequiresMembership(t *testing.T) {
	_, r, cfg, db := setupTestHandlerFor("user-9", "") // role "" => no membership
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")
	seedStoredFile(t, db, baseDir, space.ID, "", "secret.txt", "/secret.txt", "text/plain", "secret")

	w := previewRequest(t, r, "?path=/secret.txt")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-member, got %d (%s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Error("response must not leak file content")
	}
}

// TestDownloadStillForcesAttachment guards the contract of the pre-existing
// download endpoint (unchanged by this batch).
func TestDownloadStillForcesAttachment(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")
	seedStoredFile(t, db, baseDir, space.ID, "", "pic.png", "/pic.png", "image/png", "PNGDATA")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cloudfs/download?path=/pic.png", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("download failed: %d", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("download must stay attachment, got %q", cd)
	}
}
