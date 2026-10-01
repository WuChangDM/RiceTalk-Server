package bots

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func newUploadCleanupTestService(t *testing.T) (*Service, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.LocalDataPath = dataDir
	return &Service{
		db:                   testutil.MustSetupTestDB(),
		cfg:                  cfg,
		uploadDeleteAttempts: make(map[string]int),
	}, dataDir
}

func createTestUploadFile(t *testing.T, s *Service, dataDir, uploadID string) model.BotUploadAudio {
	t.Helper()
	relativePath := filepath.Join("uploads", "audio", uploadID+".mp3")
	absolutePath := filepath.Join(dataDir, relativePath)
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolutePath, []byte("ID3-test"), 0o644); err != nil {
		t.Fatal(err)
	}
	upload := model.BotUploadAudio{
		ID:        uploadID,
		UserID:    "user-1",
		Title:     "测试音频",
		FilePath:  relativePath,
		FileSize:  8,
		MimeType:  "audio/mpeg",
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(&upload).Error; err != nil {
		t.Fatal(err)
	}
	return upload
}

func TestDeleteUploadDefersWhileQueueReferencesRemain(t *testing.T) {
	s, dataDir := newUploadCleanupTestService(t)
	upload := createTestUploadFile(t, s, dataDir, "upload-referenced")
	queue := model.BotPlayQueue{
		ID:      "queue-1",
		BotID:   "bot-1",
		TrackID: upload.ID,
		Source:  "upload",
	}
	if err := s.db.Create(&queue).Error; err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUpload("user-1", upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.uploadAbsolutePath(&upload)); err != nil {
		t.Fatalf("队列仍引用时文件不应删除：%v", err)
	}
	var stored model.BotUploadAudio
	if err := s.db.First(&stored, "id = ?", upload.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.PendingDelete {
		t.Fatal("应标记为待删除")
	}

	if err := s.db.Delete(&queue).Error; err != nil {
		t.Fatal(err)
	}
	s.sweepUnreferencedUploads()
	if _, err := os.Stat(s.uploadAbsolutePath(&upload)); !os.IsNotExist(err) {
		t.Fatalf("最后一个引用移除后应删除文件，实际错误：%v", err)
	}
}

func TestPlayedUploadIsCollectedAfterLeavingQueue(t *testing.T) {
	s, dataDir := newUploadCleanupTestService(t)
	upload := createTestUploadFile(t, s, dataDir, "upload-played")
	s.markUploadPlayed(upload.ID)
	s.sweepUnreferencedUploads()

	if _, err := os.Stat(s.uploadAbsolutePath(&upload)); !os.IsNotExist(err) {
		t.Fatalf("已播放且无引用的文件应被回收，实际错误：%v", err)
	}
	var count int64
	if err := s.db.Unscoped().Model(&model.BotUploadAudio{}).
		Where("id = ? AND deleted_at IS NOT NULL", upload.ID).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应保留软删除审计记录，实际 %d", count)
	}
}
