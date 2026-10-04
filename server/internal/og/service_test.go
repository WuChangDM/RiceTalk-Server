package og

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func TestNewService(t *testing.T) {
	db := testutil.MustSetupTestDB()
	s := NewService(db)
	if s == nil {
		t.Fatalf("NewService returned nil")
	}
	if s.db != db {
		t.Errorf("expected service db to match input db")
	}
}

func TestFetchPreviewInvalidURL(t *testing.T) {
	db := testutil.MustSetupTestDB()
	s := NewService(db)

	cases := []string{
		"",
		"not-a-url",
		"ftp://example.com/file",
		"javascript:alert(1)",
		"://missing-scheme",
	}
	for _, raw := range cases {
		if _, err := s.FetchPreview(raw); err == nil {
			t.Errorf("expected error for invalid URL %q, got nil", raw)
		}
	}
}

func TestFetchPreviewSSRFBlocked(t *testing.T) {
	db := testutil.MustSetupTestDB()
	s := NewService(db)

	cases := []string{
		"http://localhost/path",
		"http://127.0.0.1:8080/",
		"http://10.0.0.1/",
		"http://192.168.1.1/",
		"http://169.254.1.1/",
		"http://172.16.0.1/",
		"http://172.31.255.255/",
		"http://0.0.0.0/",
		"http://[::1]/",
		"http://fc00::1/",
		"http://fd00::1/",
		"http://fe80::1/",
		"http://ff00::1/",
	}
	for _, raw := range cases {
		preview, err := s.FetchPreview(raw)
		if err == nil {
			t.Errorf("expected SSRF error for %q, got preview %+v", raw, preview)
			continue
		}
		if err.Error() == "" {
			t.Errorf("expected non-empty error for %q", raw)
		}
	}
}

func TestFetchPreviewSSRFAllowedFor172OutsideRange(t *testing.T) {
	// 172.32.x.x is outside the 172.16.0.0/12 private range — it should NOT be
	// flagged as SSRF by isSSRFURL. We exercise this through fetchFromRemote
	// using a local mock server reached via 127.0.0.1 (which would normally be
	// blocked). Because we can't easily mock a 172.32 address, we instead
	// verify the helper directly by constructing a host string.
	if isSSRFURL("172.32.0.1") {
		t.Errorf("172.32.0.1 should not be flagged as SSRF (outside /12)")
	}
	if isSSRFURL("172.15.0.1") {
		t.Errorf("172.15.0.1 should not be flagged as SSRF (below /12)")
	}
	if !isSSRFURL("172.16.0.1") {
		t.Errorf("172.16.0.1 should be flagged as SSRF")
	}
	if !isSSRFURL("172.31.255.255") {
		t.Errorf("172.31.255.255 should be flagged as SSRF")
	}
}

func TestFetchPreviewCacheHit(t *testing.T) {
	db := testutil.MustSetupTestDB()
	s := NewService(db)

	// Seed a fresh cache entry
	cached := &model.OGCache{
		ID:          "cache-1",
		URL:         "https://example.com/cached",
		Title:       "Cached Title",
		Description: "Cached Description",
		Image:       "https://example.com/img.png",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	if err := db.Create(cached).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	preview, err := s.FetchPreview("https://example.com/cached")
	if err != nil {
		t.Fatalf("FetchPreview cache hit returned error: %v", err)
	}
	if preview.Title != "Cached Title" {
		t.Errorf("expected cached title, got %q", preview.Title)
	}
	if preview.Description != "Cached Description" {
		t.Errorf("expected cached description, got %q", preview.Description)
	}
	if preview.Image != "https://example.com/img.png" {
		t.Errorf("expected cached image, got %q", preview.Image)
	}
	if preview.URL != "https://example.com/cached" {
		t.Errorf("expected cached url, got %q", preview.URL)
	}
}

func TestFetchPreviewExpiredCacheRefreshes(t *testing.T) {
	db := testutil.MustSetupTestDB()
	s := NewService(db)

	// Seed an expired cache entry — should be ignored and re-fetched
	expired := &model.OGCache{
		ID:          "cache-expired",
		URL:         "https://example.com/expired",
		Title:       "Stale Title",
		Description: "Stale",
		Image:       "",
		ExpiresAt:   time.Now().Add(-1 * time.Hour), // expired
	}
	if err := db.Create(expired).Error; err != nil {
		t.Fatalf("seed expired cache: %v", err)
	}

	// Stand up a mock server returning OG tags. Use 127.0.0.1 directly — but
	// SSRF guard blocks 127.0.0.1. Instead, override the public IP URL by
	// using a server whose host we can resolve via httptest's automatic
	// 127.0.0.1 listener. Since the SSRF guard rejects loopback, we cannot
	// reach httptest.Server from FetchPreview. So we verify cache expiry
	// indirectly: the expired entry must not be returned.
	preview, err := s.FetchPreview("https://example.com/expired")
	// Either it errors (network/SSRF) or returns whatever it can fetch.
	// In both cases, the stale cached title must NOT come back.
	if err == nil && preview.Title == "Stale Title" {
		t.Errorf("expired cache entry should not be used, got stale title %q", preview.Title)
	}
}

func TestFetchPreviewFromRemoteOGTags(t *testing.T) {
	// We can't use httptest.Server (loopback blocked by SSRF guard), so test
	// fetchFromRemote directly with a mock server. fetchFromRemote is unexported
	// but accessible from within the package.
	db := testutil.MustSetupTestDB()
	s := NewService(db)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<meta property="og:title" content="Hello &amp; Goodbye" />
			<meta property="og:description" content="A &lt;test&gt; description" />
			<meta property="og:image" content="https://example.com/img.png" />
			</head><body>x</body></html>`))
	}))
	defer srv.Close()

	preview, err := s.fetchFromRemote(srv.URL)
	if err != nil {
		t.Fatalf("fetchFromRemote failed: %v", err)
	}
	if preview.Title != "Hello & Goodbye" {
		t.Errorf("expected unescaped title, got %q", preview.Title)
	}
	if preview.Description != "A <test> description" {
		t.Errorf("expected unescaped description, got %q", preview.Description)
	}
	if preview.Image != "https://example.com/img.png" {
		t.Errorf("expected image url, got %q", preview.Image)
	}
	if preview.URL != srv.URL {
		t.Errorf("expected URL %s, got %s", srv.URL, preview.URL)
	}
}

func TestFetchPreviewFromRemoteFallbackTags(t *testing.T) {
	db := testutil.MustSetupTestDB()
	s := NewService(db)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<title>Fallback Title</title>
			<meta name="description" content="Fallback description" />
			</head><body>no OG tags here</body></html>`))
	}))
	defer srv.Close()

	preview, err := s.fetchFromRemote(srv.URL)
	if err != nil {
		t.Fatalf("fetchFromRemote failed: %v", err)
	}
	if preview.Title != "Fallback Title" {
		t.Errorf("expected fallback title, got %q", preview.Title)
	}
	if preview.Description != "Fallback description" {
		t.Errorf("expected fallback description, got %q", preview.Description)
	}
	if preview.Image != "" {
		t.Errorf("expected empty image, got %q", preview.Image)
	}
}

func TestFetchPreviewFromRemoteNon200(t *testing.T) {
	db := testutil.MustSetupTestDB()
	s := NewService(db)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := s.fetchFromRemote(srv.URL); err == nil {
		t.Errorf("expected error for non-200 status, got nil")
	}
}

func TestUnescapeHTML(t *testing.T) {
	cases := map[string]string{
		"a &amp; b":          "a & b",
		"&lt;tag&gt;":        "<tag>",
		"&quot;quoted&quot;": `"quoted"`,
		"&#39;apos&#39;":     "'apos'",
		"plain text":         "plain text",
		"":                   "",
	}
	for in, want := range cases {
		if got := unescapeHTML(in); got != want {
			t.Errorf("unescapeHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGenerateIDNonEmpty(t *testing.T) {
	id := generateID()
	if id == "" {
		t.Errorf("generateID returned empty string")
	}
	// Each call should produce a non-empty ID
	id2 := generateID()
	if id2 == "" {
		t.Errorf("second generateID returned empty string")
	}
}
