package cloudfs

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ridgericetalk/internal/model"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestUploadRecordsChecksum: POST /cloudfs/upload must record the SHA-256 of the
// received bytes in FileMetadata.Checksum (DES-2026-0912-02 §4.6 — integrity
// only, the upload is never short-circuited by a checksum hit).
func TestUploadRecordsChecksum(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()

	content := "checksum me, please"
	if w := uploadFile(t, r, "/", "report.txt", content); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}

	var meta model.FileMetadata
	if err := db.Where("file_name = ?", "report.txt").First(&meta).Error; err != nil {
		t.Fatalf("metadata not found: %v", err)
	}
	if want := sha256Hex(content); meta.Checksum != want {
		t.Errorf("stored checksum = %q, want %q", meta.Checksum, want)
	}

	// The stored checksum must describe the bytes actually on disk.
	raw, err := os.ReadFile(meta.FilePath)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if got := sha256Hex(string(raw)); got != meta.Checksum {
		t.Errorf("checksum does not match file content: %s != %s", got, meta.Checksum)
	}
}

// TestDownloadServesChecksumHeader: the recorded checksum is exposed on the
// download response so a client can verify the transfer.
func TestDownloadServesChecksumHeader(t *testing.T) {
	_, r, cfg, _ := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()

	content := "verify me"
	if w := uploadFile(t, r, "/", "verify.txt", content); w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cloudfs/download?path=/verify.txt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("download: %d %s", w.Code, w.Body.String())
	}
	if got, want := w.Header().Get("X-File-Checksum"), "sha256="+sha256Hex(content); got != want {
		t.Errorf("X-File-Checksum = %q, want %q", got, want)
	}
	if w.Body.String() != content {
		t.Errorf("body = %q, want %q", w.Body.String(), content)
	}

	// Preview shares the resolution path and must advertise it too.
	pw := httptest.NewRecorder()
	r.ServeHTTP(pw, httptest.NewRequest("GET", "/api/v1/cloudfs/preview?path=/verify.txt", nil))
	if pw.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", pw.Code, pw.Body.String())
	}
	if got, want := pw.Header().Get("X-File-Checksum"), "sha256="+sha256Hex(content); got != want {
		t.Errorf("preview X-File-Checksum = %q, want %q", got, want)
	}
}

// TestChecksumHeaderAbsentWhenUnknown: legacy rows without a checksum must not
// emit an empty/misleading header (clients treat it as optional).
func TestChecksumHeaderAbsentWhenUnknown(t *testing.T) {
	_, r, cfg, db := setupTestHandler()
	baseDir := t.TempDir()
	cfg.LocalDataPath = baseDir
	space := firstSpace(t, db)

	// seedStoredFile writes content but no FileMetadata row -> no checksum.
	seedStoredFile(t, db, filepath.Join(baseDir, "cloudfs"),
		space.ID, "", "legacy.txt", "/legacy.txt", "text/plain", "legacy body")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cloudfs/download?path=/legacy.txt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("download: %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-File-Checksum"); got != "" {
		t.Errorf("expected no checksum header for a file without recorded checksum, got %q", got)
	}
}

// TestArchiveChannelFileRecordsChecksum: the message-attachment archive path
// (which also writes FileMetadata) records a checksum too.
func TestArchiveChannelFileRecordsChecksum(t *testing.T) {
	h, _, cfg, db := setupTestHandler()
	cfg.LocalDataPath = t.TempDir()
	space := firstSpace(t, db)

	content := "attachment payload"
	entry, err := h.Service().ArchiveChannelFile(space.ID, "chan-1", "general", "clip.bin",
		strings.NewReader(content), "user-1")
	if err != nil {
		t.Fatalf("ArchiveChannelFile: %v", err)
	}

	var meta model.FileMetadata
	if err := db.Where("file_name = ?", entry.FileName).First(&meta).Error; err != nil {
		t.Fatalf("metadata not found: %v", err)
	}
	if want := sha256Hex(content); meta.Checksum != want {
		t.Errorf("stored checksum = %q, want %q", meta.Checksum, want)
	}
}
