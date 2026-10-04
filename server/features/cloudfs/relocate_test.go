package cloudfs

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"ridgericetalk/internal/model"
)

func renameHTTP(t *testing.T, r http.Handler, itemPath, newName string) *httptest.ResponseRecorder {
	t.Helper()
	return jsonRequest(t, r, "PATCH", "/api/v1/cloudfs/rename",
		map[string]interface{}{"path": itemPath, "newName": newName})
}

func moveHTTP(t *testing.T, r http.Handler, itemPath, targetPath string) *httptest.ResponseRecorder {
	t.Helper()
	return jsonRequest(t, r, "POST", "/api/v1/cloudfs/move",
		map[string]interface{}{"path": itemPath, "targetPath": targetPath})
}

func downloadBody(t *testing.T, r http.Handler, itemPath string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cloudfs/download?path="+itemPath, nil))
	return w.Code, w.Body.String()
}

// countLive counts rows matching where using the given model, so GORM's
// soft-delete filter applies (deleted rows are not counted).
func countLive(t *testing.T, db *gorm.DB, dst interface{}, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.Model(dst).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", where, err)
	}
	return n
}

func mustLiveFolder(t *testing.T, db *gorm.DB, folderPath string) {
	t.Helper()
	if n := countLive(t, db, &model.SharedFolder{}, "path = ?", folderPath); n != 1 {
		t.Errorf("expected exactly 1 live folder at %s, got %d", folderPath, n)
	}
}

func mustLiveFile(t *testing.T, db *gorm.DB, filePath string) {
	t.Helper()
	if n := countLive(t, db, &model.SharedFileEntry{}, "file_path = ?", filePath); n != 1 {
		t.Errorf("expected exactly 1 live file at %s, got %d", filePath, n)
	}
}

// TestRenameFileAndContentStaysReachable: renaming only touches DB rows; the
// physical file (PhysicalName) is untouched and stays downloadable at the new
// logical path.
func TestRenameFileAndContentStaysReachable(t *testing.T) {
	_, r, _, db := setupTestHandler()
	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create folder: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs", "a.txt", "payload"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}

	var before model.SharedFileEntry
	if err := db.Where("file_name = ?", "a.txt").First(&before).Error; err != nil {
		t.Fatalf("entry not found: %v", err)
	}

	w := renameHTTP(t, r, "/docs/a.txt", "b.txt")
	if w.Code != http.StatusOK {
		t.Fatalf("rename failed: %d %s", w.Code, w.Body.String())
	}

	var after model.SharedFileEntry
	if err := db.Where("id = ?", before.ID).First(&after).Error; err != nil {
		t.Fatalf("reload entry: %v", err)
	}
	if after.FileName != "b.txt" || after.FilePath != "/docs/b.txt" {
		t.Errorf("expected name/path b.txt//docs/b.txt, got %s/%s", after.FileName, after.FilePath)
	}
	if after.PhysicalName != before.PhysicalName {
		t.Errorf("physical name must not change, %s -> %s", before.PhysicalName, after.PhysicalName)
	}

	if code, body := downloadBody(t, r, "/docs/b.txt"); code != http.StatusOK || body != "payload" {
		t.Errorf("expected content at the new path, got %d %q", code, body)
	}
	if code, _ := downloadBody(t, r, "/docs/a.txt"); code != http.StatusNotFound {
		t.Errorf("old path must be gone, got %d", code)
	}
}

// TestRenameFolderRewritesSubtreePaths covers the consistency requirement:
// SharedFolder.Path is the lookup key, so every descendant folder path and file
// file_path must move with the renamed folder.
func TestRenameFolderRewritesSubtreePaths(t *testing.T) {
	_, r, _, db := setupTestHandler()
	for _, f := range [][2]string{{"/", "docs"}, {"/docs", "sub"}} {
		if w := createFolderHTTP(t, r, f[0], f[1]); w.Code != http.StatusOK {
			t.Fatalf("create %s/%s: %d %s", f[0], f[1], w.Code, w.Body.String())
		}
	}
	if w := uploadFile(t, r, "/docs", "root.txt", "top"); w.Code != http.StatusOK {
		t.Fatalf("upload to /docs: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs/sub", "deep.txt", "bottom"); w.Code != http.StatusOK {
		t.Fatalf("upload to /docs/sub: %d %s", w.Code, w.Body.String())
	}

	if w := renameHTTP(t, r, "/docs", "media"); w.Code != http.StatusOK {
		t.Fatalf("rename folder failed: %d %s", w.Code, w.Body.String())
	}

	mustLiveFolder(t, db, "/media")
	mustLiveFolder(t, db, "/media/sub")
	mustLiveFile(t, db, "/media/root.txt")
	mustLiveFile(t, db, "/media/sub/deep.txt")

	if code, body := downloadBody(t, r, "/media/sub/deep.txt"); code != http.StatusOK || body != "bottom" {
		t.Errorf("expected deep content at new path, got %d %q", code, body)
	}
	if code, _ := downloadBody(t, r, "/docs/root.txt"); code != http.StatusNotFound {
		t.Errorf("old subtree path must be gone, got %d", code)
	}

	// The renamed subtree must still be listable through the API.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cloudfs/list?path=/media", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list /media failed: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "sub") || !strings.Contains(w.Body.String(), "root.txt") {
		t.Errorf("expected 'sub' and 'root.txt' under /media, got %s", w.Body.String())
	}
}

// TestRenameFolderEscapesLikeWildcards: folder names are user controlled, so a
// name containing LIKE wildcards ("a_b", "50%") must not widen the subtree
// rewrite to sibling folders that merely happen to match the pattern.
func TestRenameFolderEscapesLikeWildcards(t *testing.T) {
	_, r, _, db := setupTestHandler()
	for _, name := range []string{"a_b", "axb"} {
		if w := createFolderHTTP(t, r, "/", name); w.Code != http.StatusOK {
			t.Fatalf("create /%s: %d %s", name, w.Code, w.Body.String())
		}
		if w := uploadFile(t, r, "/"+name, "f.txt", name); w.Code != http.StatusOK {
			t.Fatalf("upload to /%s: %d %s", name, w.Code, w.Body.String())
		}
	}

	if w := renameHTTP(t, r, "/a_b", "renamed"); w.Code != http.StatusOK {
		t.Fatalf("rename failed: %d %s", w.Code, w.Body.String())
	}

	mustLiveFolder(t, db, "/renamed")
	mustLiveFile(t, db, "/renamed/f.txt")
	// The sibling must be untouched: LIKE 'a_b/%' would have matched /axb/f.txt.
	mustLiveFolder(t, db, "/axb")
	mustLiveFile(t, db, "/axb/f.txt")
}

// TestRenameConflictsAndValidation covers the 409/400 contract.
func TestRenameConflictsAndValidation(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")

	seedStoredFile(t, db, baseDir, space.ID, "", "a.txt", "/a.txt", "text/plain", "A")
	seedStoredFile(t, db, baseDir, space.ID, "", "b.txt", "/b.txt", "text/plain", "B")
	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create /docs: %d", w.Code)
	}

	// Same-parent name clash with a file.
	if w := renameHTTP(t, r, "/a.txt", "b.txt"); w.Code != http.StatusConflict {
		t.Errorf("expected 409 when renaming onto an existing file, got %d %s", w.Code, w.Body.String())
	}
	// Clash with a folder occupying the target path.
	if w := renameHTTP(t, r, "/a.txt", "docs"); w.Code != http.StatusConflict {
		t.Errorf("expected 409 when renaming onto an existing folder, got %d %s", w.Code, w.Body.String())
	}
	// Invalid names.
	for _, bad := range []string{"a/b", "..", ".", ""} {
		if w := renameHTTP(t, r, "/a.txt", bad); w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid name %q, got %d", bad, w.Code)
		}
	}
	// Over-long name (SharedFileEntry.FileName is 256).
	if w := renameHTTP(t, r, "/a.txt", strings.Repeat("x", 257)); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for over-long file name, got %d", w.Code)
	}
	// Over-long folder name (SharedFolder.Name is 128).
	if w := renameHTTP(t, r, "/docs", strings.Repeat("y", 129)); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for over-long folder name, got %d", w.Code)
	}
	// Renaming to the same name is an idempotent success.
	if w := renameHTTP(t, r, "/a.txt", "a.txt"); w.Code != http.StatusOK {
		t.Errorf("expected 200 for a no-op rename, got %d", w.Code)
	}
	// Unknown item.
	if w := renameHTTP(t, r, "/missing.txt", "x.txt"); w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for a missing item, got %d", w.Code)
	}
}

// TestRenameForbiddenForNonOwner: rename follows the same ownership rule as
// delete (uploader/owner or space admin).
func TestRenameForbiddenForNonOwner(t *testing.T) {
	_, r, cfg, db := setupTestHandlerFor("user-2", "MEMBER")
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")
	seedStoredFile(t, db, baseDir, space.ID, "", "owned.txt", "/owned.txt", "text/plain", "X")

	w := renameHTTP(t, r, "/owned.txt", "stolen.txt")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-owner member, got %d %s", w.Code, w.Body.String())
	}
	if n := countLive(t, db, &model.SharedFileEntry{}, "file_name = ?", "owned.txt"); n != 1 {
		t.Errorf("expected the original file to stay in place, got %d", n)
	}
}

// TestRenameNonMemberDenied: a user outside the space cannot rename anything.
func TestRenameNonMemberDenied(t *testing.T) {
	_, r, cfg, db := setupTestHandlerFor("user-9", "")
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")
	seedStoredFile(t, db, baseDir, space.ID, "", "memberonly.txt", "/memberonly.txt", "text/plain", "X")

	if w := renameHTTP(t, r, "/memberonly.txt", "x.txt"); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-member, got %d %s", w.Code, w.Body.String())
	}
}

// TestMoveFileUpdatesFolderAndPath.
func TestMoveFileUpdatesFolderAndPath(t *testing.T) {
	_, r, _, db := setupTestHandler()
	for _, name := range []string{"src", "dst"} {
		if w := createFolderHTTP(t, r, "/", name); w.Code != http.StatusOK {
			t.Fatalf("create /%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := uploadFile(t, r, "/src", "f.txt", "moved"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}

	var dst model.SharedFolder
	if err := db.Where("path = ?", "/dst").First(&dst).Error; err != nil {
		t.Fatalf("dst folder not found: %v", err)
	}

	if w := moveHTTP(t, r, "/src/f.txt", "/dst"); w.Code != http.StatusOK {
		t.Fatalf("move failed: %d %s", w.Code, w.Body.String())
	}

	var entry model.SharedFileEntry
	if err := db.Where("file_name = ?", "f.txt").First(&entry).Error; err != nil {
		t.Fatalf("reload entry: %v", err)
	}
	if entry.FilePath != "/dst/f.txt" {
		t.Errorf("expected file_path /dst/f.txt, got %s", entry.FilePath)
	}
	if entry.FolderID != dst.ID {
		t.Errorf("expected folder_id %s, got %s", dst.ID, entry.FolderID)
	}
	if code, body := downloadBody(t, r, "/dst/f.txt"); code != http.StatusOK || body != "moved" {
		t.Errorf("expected content at the new path, got %d %q", code, body)
	}
	// Moving back to the root clears folder_id.
	if w := moveHTTP(t, r, "/dst/f.txt", "/"); w.Code != http.StatusOK {
		t.Fatalf("move to root failed: %d %s", w.Code, w.Body.String())
	}
	if err := db.Where("file_name = ?", "f.txt").First(&entry).Error; err != nil {
		t.Fatalf("reload entry: %v", err)
	}
	if entry.FilePath != "/f.txt" || entry.FolderID != "" {
		t.Errorf("expected root placement, got path=%s folder=%q", entry.FilePath, entry.FolderID)
	}
}

// TestMoveFolderRewritesSubtreeAndParent covers moving a whole folder (with
// descendants) to a different parent.
func TestMoveFolderRewritesSubtreeAndParent(t *testing.T) {
	_, r, _, db := setupTestHandler()
	for _, f := range [][2]string{{"/", "a"}, {"/a", "sub"}, {"/a/sub", "deep"}, {"/", "target"}} {
		if w := createFolderHTTP(t, r, f[0], f[1]); w.Code != http.StatusOK {
			t.Fatalf("create %s/%s: %d %s", f[0], f[1], w.Code, w.Body.String())
		}
	}
	if w := uploadFile(t, r, "/a/sub/deep", "leaf.txt", "leaf"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}

	var target model.SharedFolder
	if err := db.Where("path = ?", "/target").First(&target).Error; err != nil {
		t.Fatalf("target folder not found: %v", err)
	}

	if w := moveHTTP(t, r, "/a", "/target"); w.Code != http.StatusOK {
		t.Fatalf("move folder failed: %d %s", w.Code, w.Body.String())
	}

	mustLiveFolder(t, db, "/target/a")
	mustLiveFolder(t, db, "/target/a/sub")
	mustLiveFolder(t, db, "/target/a/sub/deep")
	mustLiveFile(t, db, "/target/a/sub/deep/leaf.txt")

	var moved model.SharedFolder
	if err := db.Where("path = ?", "/target/a").First(&moved).Error; err != nil {
		t.Fatalf("moved folder not found: %v", err)
	}
	if moved.ParentID == nil || *moved.ParentID != target.ID {
		t.Errorf("expected parent_id %s, got %v", target.ID, moved.ParentID)
	}

	if code, body := downloadBody(t, r, "/target/a/sub/deep/leaf.txt"); code != http.StatusOK || body != "leaf" {
		t.Errorf("expected leaf content at the new path, got %d %q", code, body)
	}
}

// TestMoveGuardsAndConflicts covers the rejection paths.
func TestMoveGuardsAndConflicts(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")

	for _, f := range [][2]string{{"/", "a"}, {"/a", "b"}, {"/", "other"}} {
		if w := createFolderHTTP(t, r, f[0], f[1]); w.Code != http.StatusOK {
			t.Fatalf("create %s/%s: %d %s", f[0], f[1], w.Code, w.Body.String())
		}
	}
	var other model.SharedFolder
	if err := db.Where("path = ?", "/other").First(&other).Error; err != nil {
		t.Fatalf("other folder not found: %v", err)
	}
	seedStoredFile(t, db, baseDir, space.ID, "", "x.txt", "/x.txt", "text/plain", "X")
	// /other already holds x.txt, so moving /x.txt there must collide.
	seedStoredFile(t, db, baseDir, space.ID, other.ID, "x.txt", "/other/x.txt", "text/plain", "X2")

	// A folder cannot be moved into itself or a descendant.
	if w := moveHTTP(t, r, "/a", "/a"); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 moving a folder onto itself, got %d %s", w.Code, w.Body.String())
	}
	if w := moveHTTP(t, r, "/a", "/a/b"); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 moving a folder into its own child, got %d %s", w.Code, w.Body.String())
	}
	// Missing target folder.
	if w := moveHTTP(t, r, "/x.txt", "/nope"); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a missing target folder, got %d %s", w.Code, w.Body.String())
	}
	// Destination name clash.
	if w := moveHTTP(t, r, "/x.txt", "/other"); w.Code != http.StatusConflict {
		t.Errorf("expected 409 for a destination name clash, got %d %s", w.Code, w.Body.String())
	}
	// Moving a folder to its own parent is a no-op success (/a/b -> /a).
	if w := moveHTTP(t, r, "/a/b", "/a"); w.Code != http.StatusOK {
		t.Errorf("expected 200 for a no-op move, got %d %s", w.Code, w.Body.String())
	}
	// Unknown item.
	if w := moveHTTP(t, r, "/missing.txt", "/other"); w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for a missing item, got %d", w.Code)
	}
}

// TestMoveFolderConflictRollsBackAtomically: if any destination path inside the
// subtree collides, nothing must change (no half-renamed tree).
func TestMoveFolderConflictRollsBackAtomically(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	space := firstSpace(t, db)
	baseDir := filepath.Join(cfg.LocalDataPath, "cloudfs")

	// /a/sub carries a file; /b already contains a folder called "a", so moving
	// /a under /b tries to occupy /b/a and must be rejected wholesale.
	for _, f := range [][2]string{{"/", "a"}, {"/a", "sub"}, {"/", "b"}, {"/b", "a"}} {
		if w := createFolderHTTP(t, r, f[0], f[1]); w.Code != http.StatusOK {
			t.Fatalf("create %s/%s: %d %s", f[0], f[1], w.Code, w.Body.String())
		}
	}
	seedStoredFile(t, db, baseDir, space.ID, "", "keep.txt", "/a/sub/keep.txt", "text/plain", "K")

	if w := moveHTTP(t, r, "/a", "/b"); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a subtree collision, got %d %s", w.Code, w.Body.String())
	}

	mustLiveFolder(t, db, "/a")
	mustLiveFolder(t, db, "/a/sub")
	mustLiveFile(t, db, "/a/sub/keep.txt")
	mustLiveFolder(t, db, "/b/a")
	if n := countLive(t, db, &model.SharedFolder{}, "path LIKE ?", "/b/a/%"); n != 0 {
		t.Errorf("transaction must roll back completely, found %d descendants under /b/a", n)
	}
	if n := countLive(t, db, &model.SharedFileEntry{}, "file_path LIKE ?", "/b/a/%"); n != 0 {
		t.Errorf("transaction must roll back completely, found %d files under /b/a", n)
	}
}
