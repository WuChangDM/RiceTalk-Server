package cloudfs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
)

// ── helpers ────────────────────────────────────────────────────────────────

type trashResponse struct {
	Data struct {
		Items      []TrashItem `json:"items"`
		RetainDays int         `json:"retainDays"`
	} `json:"data"`
}

func listTrashHTTP(t *testing.T, r http.Handler) []TrashItem {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cloudfs/trash", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list trash: %d %s", w.Code, w.Body.String())
	}
	var resp trashResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode trash: %v (%s)", err, w.Body.String())
	}
	return resp.Data.Items
}

func deleteHTTP(t *testing.T, r http.Handler, itemPath string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/cloudfs/delete?path="+itemPath, nil))
	return w
}

func mustDeleteHTTP(t *testing.T, r http.Handler, itemPath string) {
	t.Helper()
	if w := deleteHTTP(t, r, itemPath); w.Code != http.StatusOK {
		t.Fatalf("delete %s: %d %s", itemPath, w.Code, w.Body.String())
	}
}

func restoreTrashHTTP(t *testing.T, r http.Handler, id, itemPath string) *httptest.ResponseRecorder {
	t.Helper()
	return jsonRequest(t, r, "POST", "/api/v1/cloudfs/trash/restore",
		map[string]interface{}{"id": id, "path": itemPath})
}

func purgeTrashHTTP(t *testing.T, r http.Handler, id, itemPath string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/cloudfs/trash?"
	if id != "" {
		target += "id=" + id
	} else {
		target += "path=" + itemPath
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", target, nil))
	return w
}

func emptyTrashHTTP(t *testing.T, r http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/cloudfs/trash/all", nil))
	return w
}

// trashEntries returns every physical file parked in the trash directory.
func trashEntries(t *testing.T, localDataPath string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(localDataPath, "cloudfs", ".trash"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read trash dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func unscopedCount(t *testing.T, db *gorm.DB, dst interface{}, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.Unscoped().Model(dst).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("unscoped count %s: %v", where, err)
	}
	return n
}

// mustService builds a Service over the test DB/config (the handler keeps its
// own instance; both share the same database and data path).
func mustService(t *testing.T, db *gorm.DB, cfg *config.Config) *Service {
	t.Helper()
	return NewService(db, cfg)
}

// ── tests ──────────────────────────────────────────────────────────────────

// TestDeleteParksContentAndHidesFromList: an ordinary delete must move rows to
// the trash state (invisible to List/GetUsage/Download) while keeping the bytes
// recoverable in .trash.
func TestDeleteParksContentAndHidesFromList(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create folder: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs", "a.txt", "payload"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 0 {
		t.Fatalf("fresh upload must not be in trash, found %d", got)
	}

	mustDeleteHTTP(t, r, "/docs/a.txt")

	// Invisible to the normal listing (the row is soft-deleted).
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, httptest.NewRequest("GET", "/api/v1/cloudfs/list?path=/docs", nil))
	if strings.Contains(lw.Body.String(), "a.txt") {
		t.Errorf("deleted file must not be listed, got %s", lw.Body.String())
	}

	// Not counted against the quota (design §4.3 documents this explicitly).
	used, _, err := mustService(t, db, cfg).GetUsage(firstSpace(t, db).ID)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if used != 0 {
		t.Errorf("trashed content must not count against usage, got %d", used)
	}

	// Content is parked, not deleted.
	trashed := trashEntries(t, cfg.LocalDataPath)
	if len(trashed) != 1 {
		t.Fatalf("expected 1 parked file, got %v", trashed)
	}

	// Download of a deleted path must 404.
	dw := httptest.NewRecorder()
	r.ServeHTTP(dw, httptest.NewRequest("GET", "/api/v1/cloudfs/download?path=/docs/a.txt", nil))
	if dw.Code != http.StatusNotFound {
		t.Errorf("download of trashed file: got %d, want 404", dw.Code)
	}

	items := listTrashHTTP(t, r)
	if len(items) != 1 {
		t.Fatalf("expected 1 trash item, got %d (%v)", len(items), items)
	}
	if items[0].Name != "a.txt" || items[0].Path != "/docs/a.txt" || items[0].Type != "file" {
		t.Errorf("unexpected trash item: %+v", items[0])
	}
	if items[0].ExpiresAt == "" {
		t.Errorf("trash item must advertise its expiry, got %+v", items[0])
	}
}

// TestRestoreBringsBackContent: restore clears deleted_at and un-parks the bytes.
func TestRestoreBringsBackContent(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create folder: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs", "a.txt", "hello trash"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	mustDeleteHTTP(t, r, "/docs/a.txt")

	items := listTrashHTTP(t, r)
	if len(items) != 1 {
		t.Fatalf("expected 1 trash item, got %d", len(items))
	}

	if w := restoreTrashHTTP(t, r, "", "/docs/a.txt"); w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}

	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 0 {
		t.Errorf("restore must un-park the content, trash still holds %d", got)
	}
	if n := countLive(t, db, &model.SharedFileEntry{}, "file_path = ?", "/docs/a.txt"); n != 1 {
		t.Errorf("expected 1 live row after restore, got %d", n)
	}
	code, body := downloadBody(t, r, "/docs/a.txt")
	if code != http.StatusOK || body != "hello trash" {
		t.Errorf("download after restore: %d %q", code, body)
	}
	if got := len(listTrashHTTP(t, r)); got != 0 {
		t.Errorf("trash must be empty after restore, got %d", got)
	}
}

// TestRestoreConflictWhenPathOccupied: restoring into an occupied path must fail
// like a rename/move collision — and leaving it in the trash must not break the
// re-uploaded file.
func TestRestoreConflictWhenPathOccupied(t *testing.T) {
	_, r, cfg, _ := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	if w := uploadFile(t, r, "/", "a.txt", "first"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	mustDeleteHTTP(t, r, "/a.txt")
	// Re-uploading the same name must succeed (partial unique index, batch 1).
	if w := uploadFile(t, r, "/", "a.txt", "second"); w.Code != http.StatusOK {
		t.Fatalf("re-upload after delete: %d %s", w.Code, w.Body.String())
	}

	items := listTrashHTTP(t, r)
	if len(items) != 1 {
		t.Fatalf("expected the old incarnation in trash, got %d items", len(items))
	}
	w := restoreTrashHTTP(t, r, items[0].ID, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("restore into occupied path: got %d, want 409 (%s)", w.Code, w.Body.String())
	}

	// The live file is undisturbed.
	if code, body := downloadBody(t, r, "/a.txt"); code != http.StatusOK || body != "second" {
		t.Errorf("live file damaged by failed restore: %d %q", code, body)
	}
}

// TestDeleteWorksRepeatedlyWithTrashAccumulating is the regression guard for the
// batch-1 defect: soft-deleted tombstones — now also parked in the trash — must
// never block a later delete/create of the same name.
func TestDeleteWorksRepeatedlyWithTrashAccumulating(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()

	for i := 0; i < 3; i++ {
		if w := uploadFile(t, r, "/", "a.txt", "v1"); w.Code != http.StatusOK {
			t.Fatalf("upload #%d: %d %s", i+1, w.Code, w.Body.String())
		}
		mustDeleteHTTP(t, r, "/a.txt")
	}

	if got := len(listTrashHTTP(t, r)); got != 3 {
		t.Errorf("expected 3 tombstones in trash, got %d", got)
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 3 {
		t.Errorf("expected 3 parked contents, got %d", got)
	}

	// Emptying the trash leaves only the newest tombstone's name useless — the
	// name itself must stay usable afterwards.
	if w := emptyTrashHTTP(t, r); w.Code != http.StatusOK {
		t.Fatalf("empty trash: %d %s", w.Code, w.Body.String())
	}
	if n := unscopedCount(t, db, &model.SharedFileEntry{}, "file_name = ?", "a.txt"); n != 0 {
		t.Errorf("expected no rows after empty trash, got %d", n)
	}
	if w := uploadFile(t, r, "/", "a.txt", "v2"); w.Code != http.StatusOK {
		t.Fatalf("upload after empty: %d %s", w.Code, w.Body.String())
	}
	mustDeleteHTTP(t, r, "/a.txt")
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 1 {
		t.Errorf("expected the new tombstone parked, got %d", got)
	}
}

// TestFolderTrashRestoresWholeSubtree: a deleted folder is one trash entry and
// restoring it revives the entire subtree with its contents.
func TestFolderTrashRestoresWholeSubtree(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create docs: %d %s", w.Code, w.Body.String())
	}
	if w := createFolderHTTP(t, r, "/docs", "sub"); w.Code != http.StatusOK {
		t.Fatalf("create sub: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs", "top.txt", "top"); w.Code != http.StatusOK {
		t.Fatalf("upload top: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs/sub", "deep.txt", "deep"); w.Code != http.StatusOK {
		t.Fatalf("upload deep: %d %s", w.Code, w.Body.String())
	}

	mustDeleteHTTP(t, r, "/docs")

	items := listTrashHTTP(t, r)
	if len(items) != 1 || items[0].Type != "folder" || items[0].Path != "/docs" {
		t.Fatalf("expected only the top folder in trash, got %+v", items)
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 2 {
		t.Errorf("expected both files parked, got %d", got)
	}

	if w := restoreTrashHTTP(t, r, items[0].ID, ""); w.Code != http.StatusOK {
		t.Fatalf("restore folder: %d %s", w.Code, w.Body.String())
	}
	if n := countLive(t, db, &model.SharedFolder{}, "path = ?", "/docs/sub"); n != 1 {
		t.Errorf("sub folder not restored")
	}
	if code, body := downloadBody(t, r, "/docs/sub/deep.txt"); code != http.StatusOK || body != "deep" {
		t.Errorf("deep file after restore: %d %q", code, body)
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 0 {
		t.Errorf("trash should be empty after folder restore, has %d", got)
	}
}

// TestFolderPurgeRemovesWholeSubtree: a permanent delete of a folder clears the
// rows and the parked content of everything inside it.
func TestFolderPurgeRemovesWholeSubtree(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create docs: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs", "top.txt", "top"); w.Code != http.StatusOK {
		t.Fatalf("upload top: %d %s", w.Code, w.Body.String())
	}
	mustDeleteHTTP(t, r, "/docs")

	items := listTrashHTTP(t, r)
	if w := purgeTrashHTTP(t, r, "", items[0].Path); w.Code != http.StatusOK {
		t.Fatalf("purge folder: %d %s", w.Code, w.Body.String())
	}
	if got := unscopedCount(t, db, &model.SharedFileEntry{}, "space_id = ?", firstSpace(t, db).ID); got != 0 {
		t.Errorf("file rows survived folder purge: %d", got)
	}
	if got := unscopedCount(t, db, &model.SharedFolder{}, "space_id = ?", firstSpace(t, db).ID); got != 0 {
		t.Errorf("folder rows survived folder purge: %d", got)
	}
	if got := unscopedCount(t, db, &model.FileMetadata{}, "file_name = ?", "top.txt"); got != 0 {
		t.Errorf("FileMetadata survived folder purge: %d", got)
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 0 {
		t.Errorf("parked content survived folder purge: %v", trashEntries(t, cfg.LocalDataPath))
	}
}

// TestPurgeTrashRemovesRowContentAndMetadata: a permanent delete must leave
// nothing behind.
func TestPurgeTrashRemovesRowContentAndMetadata(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	if w := uploadFile(t, r, "/", "gone.txt", "bye"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var meta model.FileMetadata
	if err := db.Where("file_name = ?", "gone.txt").First(&meta).Error; err != nil {
		t.Fatalf("metadata: %v", err)
	}
	mustDeleteHTTP(t, r, "/gone.txt")
	if len(trashEntries(t, cfg.LocalDataPath)) != 1 {
		t.Fatalf("expected parked content")
	}

	items := listTrashHTTP(t, r)
	if w := purgeTrashHTTP(t, r, items[0].ID, ""); w.Code != http.StatusOK {
		t.Fatalf("purge: %d %s", w.Code, w.Body.String())
	}

	if n := unscopedCount(t, db, &model.SharedFileEntry{}, "id = ?", items[0].ID); n != 0 {
		t.Errorf("row survived purge")
	}
	if n := unscopedCount(t, db, &model.FileMetadata{}, "id = ?", meta.ID); n != 0 {
		t.Errorf("FileMetadata row survived purge")
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 0 {
		t.Errorf("parked content survived purge: %v", trashEntries(t, cfg.LocalDataPath))
	}
	if _, err := os.Stat(meta.FilePath); !os.IsNotExist(err) {
		t.Errorf("physical file survived purge at %s", meta.FilePath)
	}
	if got := len(listTrashHTTP(t, r)); got != 0 {
		t.Errorf("trash still lists %d items", got)
	}
}

// TestEmptyTrashPurgesWholeSpace: DELETE /cloudfs/trash/all clears everything.
func TestEmptyTrashPurgesWholeSpace(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create folder: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs", "a.txt", "a"); w.Code != http.StatusOK {
		t.Fatalf("upload a: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/", "b.txt", "b"); w.Code != http.StatusOK {
		t.Fatalf("upload b: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/", "live.txt", "live"); w.Code != http.StatusOK {
		t.Fatalf("upload live: %d %s", w.Code, w.Body.String())
	}

	mustDeleteHTTP(t, r, "/docs")
	mustDeleteHTTP(t, r, "/b.txt")

	if w := emptyTrashHTTP(t, r); w.Code != http.StatusOK {
		t.Fatalf("empty trash: %d %s", w.Code, w.Body.String())
	}
	if got := len(listTrashHTTP(t, r)); got != 0 {
		t.Errorf("trash not empty: %d", got)
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 0 {
		t.Errorf("parked content not removed: %v", trashEntries(t, cfg.LocalDataPath))
	}
	// The live file and its content must be untouched.
	if code, body := downloadBody(t, r, "/live.txt"); code != http.StatusOK || body != "live" {
		t.Errorf("live file damaged by empty trash: %d %q", code, body)
	}
	if n := unscopedCount(t, db, &model.SharedFolder{}, "space_id = ?", firstSpace(t, db).ID); n != 0 {
		t.Errorf("folder rows survived empty trash: %d", n)
	}
	if n := countLive(t, db, &model.SharedFileEntry{}, "file_name = ?", "live.txt"); n != 1 {
		t.Errorf("live file row damaged by empty trash: %d", n)
	}
}

// TestPurgeExpiredTrashHonorsRetention: only entries past the retention window
// are swept, and the sweep is idempotent.
func TestPurgeExpiredTrashHonorsRetention(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	svc := mustService(t, db, cfg)
	if svc.trashRetentionDays() != 30 {
		t.Fatalf("default retention must be 30 days, got %d", svc.trashRetentionDays())
	}

	if w := uploadFile(t, r, "/", "old.txt", "old"); w.Code != http.StatusOK {
		t.Fatalf("upload old: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/", "fresh.txt", "fresh"); w.Code != http.StatusOK {
		t.Fatalf("upload fresh: %d %s", w.Code, w.Body.String())
	}
	mustDeleteHTTP(t, r, "/old.txt")
	mustDeleteHTTP(t, r, "/fresh.txt")

	// Backdate one tombstone past the window.
	if err := db.Unscoped().Model(&model.SharedFileEntry{}).
		Where("file_name = ?", "old.txt").
		Update("deleted_at", time.Now().AddDate(0, 0, -31)).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := svc.PurgeExpiredTrash(time.Now())
	if err != nil {
		t.Fatalf("PurgeExpiredTrash: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 purged entry, got %d", n)
	}
	if got := unscopedCount(t, db, &model.SharedFileEntry{}, "file_name = ?", "old.txt"); got != 0 {
		t.Errorf("expired entry survived: %d", got)
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 1 {
		t.Errorf("expected the fresh entry still parked, got %d", got)
	}
	items := listTrashHTTP(t, r)
	if len(items) != 1 || items[0].Name != "fresh.txt" {
		t.Errorf("fresh entry must stay in trash, got %+v", items)
	}

	// Idempotent: a second sweep finds nothing more.
	if n, err := svc.PurgeExpiredTrash(time.Now()); err != nil || n != 0 {
		t.Errorf("second sweep: n=%d err=%v", n, err)
	}
}

// TestPurgeExpiredTrashSweepsWholeFolder: a folder past retention takes its
// (same-age) subtree and parked contents with it.
func TestPurgeExpiredTrashSweepsWholeFolder(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	svc := mustService(t, db, cfg)

	if w := createFolderHTTP(t, r, "/", "docs"); w.Code != http.StatusOK {
		t.Fatalf("create folder: %d %s", w.Code, w.Body.String())
	}
	if w := uploadFile(t, r, "/docs", "a.txt", "a"); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	mustDeleteHTTP(t, r, "/docs")

	backdate := time.Now().AddDate(0, 0, -40)
	for _, m := range []interface{}{&model.SharedFolder{}, &model.SharedFileEntry{}} {
		if err := db.Unscoped().Model(m).Where("space_id = ?", firstSpace(t, db).ID).
			Update("deleted_at", backdate).Error; err != nil {
			t.Fatalf("backdate: %v", err)
		}
	}

	n, err := svc.PurgeExpiredTrash(time.Now())
	if err != nil {
		t.Fatalf("PurgeExpiredTrash: %v", err)
	}
	if n != 1 {
		t.Errorf("expected the folder counted once, got %d", n)
	}
	if got := unscopedCount(t, db, &model.SharedFolder{}, "space_id = ?", firstSpace(t, db).ID); got != 0 {
		t.Errorf("folder rows survived: %d", got)
	}
	if got := unscopedCount(t, db, &model.SharedFileEntry{}, "space_id = ?", firstSpace(t, db).ID); got != 0 {
		t.Errorf("file rows survived: %d", got)
	}
	if got := len(trashEntries(t, cfg.LocalDataPath)); got != 0 {
		t.Errorf("parked content survived: %v", trashEntries(t, cfg.LocalDataPath))
	}
}

// TestTrashRequiresMembership: non-members are locked out of every trash action.
func TestTrashRequiresMembership(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	space := firstSpace(t, db)

	// Drop the membership created by the fixture so the fixture identity is no
	// longer a member of the space.
	if err := db.Where("space_id = ? AND user_id = ?", space.ID, "user-1").
		Delete(&model.Membership{}).Error; err != nil {
		t.Fatalf("drop membership: %v", err)
	}

	for _, req := range []struct {
		method string
		target string
		body   interface{}
	}{
		{"GET", "/api/v1/cloudfs/trash", nil},
		{"POST", "/api/v1/cloudfs/trash/restore", map[string]interface{}{"path": "/a.txt"}},
	} {
		w := jsonRequest(t, r, req.method, req.target, req.body)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403 (%s)", req.method, req.target, w.Code, w.Body.String())
		}
	}

	pw := httptest.NewRecorder()
	r.ServeHTTP(pw, httptest.NewRequest("DELETE", "/api/v1/cloudfs/trash/all", nil))
	if pw.Code != http.StatusForbidden {
		t.Errorf("DELETE trash/all: got %d, want 403", pw.Code)
	}
}
