package cloudfs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
)

type searchResponse struct {
	Data struct {
		Items []CloudFileItem `json:"items"`
		Query string          `json:"query"`
	} `json:"data"`
}

func searchHTTP(t *testing.T, r http.Handler, q, scope string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/cloudfs/search?q=" + url.QueryEscape(q)
	if scope != "" {
		target += "&path=" + url.QueryEscape(scope)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
	return w
}

func mustSearch(t *testing.T, r http.Handler, q, scope string) []CloudFileItem {
	t.Helper()
	w := searchHTTP(t, r, q, scope)
	if w.Code != http.StatusOK {
		t.Fatalf("search %q: %d %s", q, w.Code, w.Body.String())
	}
	var resp searchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode search: %v (%s)", err, w.Body.String())
	}
	return resp.Data.Items
}

func names(items []CloudFileItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

// seedSearchFixture uploads a small tree:
//
//	/report-2026.pdf        (root)
//	/docs/report-draft.txt  (docs)
//	/docs/notes.md          (docs)
//	/docs/sub/report-final.txt (docs/sub)
//
// plus an unrelated file in another space.
func seedSearchFixture(t *testing.T, r http.Handler) {
	t.Helper()
	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create docs: %d %s", w.Code, w.Body.String())
	}
	if w := createFolderHTTP(t, r, "/docs", "sub"); w.Code != http.StatusOK {
		t.Fatalf("create sub: %d %s", w.Code, w.Body.String())
	}
	for _, f := range []struct{ dir, name string }{
		{"/", "report-2026.pdf"},
		{"/docs", "report-draft.txt"},
		{"/docs", "notes.md"},
		{"/docs/sub", "report-final.txt"},
	} {
		if w := uploadFile(t, r, f.dir, f.name, "body of "+f.name); w.Code != http.StatusOK {
			t.Fatalf("upload %s: %d %s", f.name, w.Code, w.Body.String())
		}
	}
}

// TestSearchMatchesByNameAcrossTheSpace: default scope is the whole space, and
// every hit carries its full path so the client can open it.
func TestSearchMatchesByNameAcrossTheSpace(t *testing.T) {
	_, r, cfg, _ := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	seedSearchFixture(t, r)

	items := mustSearch(t, r, "report", "")
	if len(items) != 3 {
		t.Fatalf("expected 3 hits, got %v", names(items))
	}
	byName := map[string]string{}
	for _, it := range items {
		byName[it.Name] = it.Path
		if it.Type != "file" {
			t.Errorf("search must return files, got type %q", it.Type)
		}
	}
	for name, wantPath := range map[string]string{
		"report-2026.pdf":  "/report-2026.pdf",
		"report-draft.txt": "/docs/report-draft.txt",
		"report-final.txt": "/docs/sub/report-final.txt",
	} {
		if byName[name] != wantPath {
			t.Errorf("%s: path = %q, want %q", name, byName[name], wantPath)
		}
	}

	if got := mustSearch(t, r, "does-not-exist", ""); len(got) != 0 {
		t.Errorf("expected no hits, got %v", names(got))
	}
}

// TestSearchIsScopedToTheSubtree: a `path` parameter limits the match to that
// folder and everything below it.
func TestSearchIsScopedToTheSubtree(t *testing.T) {
	_, r, cfg, _ := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	seedSearchFixture(t, r)

	docs := mustSearch(t, r, "report", "/docs")
	if len(docs) != 2 {
		t.Fatalf("scoped search in /docs: got %v, want 2", names(docs))
	}
	for _, it := range docs {
		if it.Path != "/docs/report-draft.txt" && it.Path != "/docs/sub/report-final.txt" {
			t.Errorf("hit outside scope: %s", it.Path)
		}
	}

	sub := mustSearch(t, r, "report", "/docs/sub")
	if len(sub) != 1 || sub[0].Name != "report-final.txt" {
		t.Fatalf("scoped search in /docs/sub: got %v", names(sub))
	}

	// Root scope behaves like "the whole space".
	if got := mustSearch(t, r, "report", "/"); len(got) != 3 {
		t.Errorf("root scope: got %v, want 3", names(got))
	}
}

// TestSearchIsCaseInsensitiveAndEscapesWildcards: matching folds case on both
// sides and treats %/_ as literal characters.
func TestSearchIsCaseInsensitiveAndEscapesWildcards(t *testing.T) {
	_, r, cfg, _ := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	if w := uploadFile(t, r, "/", "Report.PDF", "x"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/", "100%done.txt", "x"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/", "a_b.txt", "x"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}

	if got := mustSearch(t, r, "report", ""); len(got) != 1 || got[0].Name != "Report.PDF" {
		t.Errorf("lowercase query must match mixed-case name, got %v", names(got))
	}
	// "%" is a literal: it must match only the file whose name contains one.
	if got := mustSearch(t, r, "%", ""); len(got) != 1 || got[0].Name != "100%done.txt" {
		t.Errorf(`"%%" must be literal, got %v`, names(got))
	}
	// "_" is a literal too.
	if got := mustSearch(t, r, "_", ""); len(got) != 1 || got[0].Name != "a_b.txt" {
		t.Errorf(`"_" must be literal, got %v`, names(got))
	}
}

// TestSearchExcludesTrashedAndOtherSpaces: soft-deleted rows and other spaces
// must never leak into results.
func TestSearchExcludesTrashedAndOtherSpaces(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	seedSearchFixture(t, r)

	// A file in a different space, with the same name pattern.
	otherSpace := model.Space{ID: idgen.NextString(), Name: "other-space", OwnerID: "user-9"}
	if err := db.Create(&otherSpace).Error; err != nil {
		t.Fatalf("create other space: %v", err)
	}
	foreign := model.SharedFileEntry{
		ID:           idgen.GenerateID(idgen.PrefixFile),
		SpaceID:      otherSpace.ID,
		FolderID:     "",
		FileName:     "report-foreign.txt",
		FilePath:     "/report-foreign.txt",
		PhysicalName: idgen.NextString() + "_report-foreign.txt",
		FileSize:     1,
		MimeType:     "text/plain",
		VersionNo:    1,
		UploadedBy:   "user-9",
	}
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatalf("create foreign entry: %v", err)
	}

	if got := mustSearch(t, r, "report", ""); len(got) != 3 {
		t.Fatalf("foreign space leaked into results: %v", names(got))
	}

	// Delete one hit: it must leave the search index immediately.
	mustDeleteHTTP(t, r, "/report-2026.pdf")
	got := mustSearch(t, r, "report", "")
	if len(got) != 2 {
		t.Fatalf("trashed file still searchable: %v", names(got))
	}
	// ...and come back after a restore.
	items := listTrashHTTP(t, r)
	if w := restoreTrashHTTP(t, r, items[0].ID, ""); w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	if got := mustSearch(t, r, "report", ""); len(got) != 3 {
		t.Errorf("restored file not searchable again: %v", names(got))
	}
}

// TestSearchEmptyQueryAndPermissions: an empty query is a 400 and non-members
// are denied.
func TestSearchEmptyQueryAndPermissions(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()

	for _, q := range []string{"", "   "} {
		w := searchHTTP(t, r, q, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("empty query %q: got %d, want 400 (%s)", q, w.Code, w.Body.String())
		}
	}

	space := firstSpace(t, db)
	if err := db.Where("space_id = ? AND user_id = ?", space.ID, "user-1").
		Delete(&model.Membership{}).Error; err != nil {
		t.Fatalf("drop membership: %v", err)
	}
	if w := searchHTTP(t, r, "report", ""); w.Code != http.StatusForbidden {
		t.Errorf("non-member search: got %d, want 403", w.Code)
	}
}

// TestSearchRespectsLimit keeps the LIMIT honest: 150 matching files must yield
// exactly cloudFSSearchLimit hits rather than an unbounded response.
func TestSearchRespectsLimit(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	baseDir := t.TempDir()
	cfg.LocalDataPath = baseDir
	space := firstSpace(t, db)

	for i := 0; i < cloudFSSearchLimit+50; i++ {
		seedStoredFile(t, db, filepath.Join(baseDir, "cloudfs"), space.ID, "",
			fmt.Sprintf("limit-%03d.txt", i),
			fmt.Sprintf("/limit-%03d.txt", i), "text/plain", "x")
	}

	if got := mustSearch(t, r, "limit-", ""); len(got) != cloudFSSearchLimit {
		t.Errorf("expected %d hits, got %d", cloudFSSearchLimit, len(got))
	}
}
