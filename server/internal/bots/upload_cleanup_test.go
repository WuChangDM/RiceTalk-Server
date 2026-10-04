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

func TestPlayedUploadIsCollectedAfterRetention(t *testing.T) {
	s, dataDir := newUploadCleanupTestService(t)
	upload := createTestUploadFile(t, s, dataDir, "upload-played")
	s.markUploadPlayed(upload.ID)

	// 保留窗口内（默认 24h）：播放过且无引用也不回收
	s.sweepUnreferencedUploads()
	if _, err := os.Stat(s.uploadAbsolutePath(&upload)); err != nil {
		t.Fatalf("保留窗口内不应回收已播上传，实际错误：%v", err)
	}

	// 窗口过后：回收
	stale := time.Now().Add(-25 * time.Hour)
	if err := s.db.Model(&model.BotUploadAudio{}).
		Where("id = ?", upload.ID).
		Update("last_played_at", stale).Error; err != nil {
		t.Fatal(err)
	}
	s.sweepUnreferencedUploads()
	if _, err := os.Stat(s.uploadAbsolutePath(&upload)); !os.IsNotExist(err) {
		t.Fatalf("保留窗口过后应回收文件，实际错误：%v", err)
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

func TestRetentionZeroKeepsLegacyImmediateSweep(t *testing.T) {
	s, dataDir := newUploadCleanupTestService(t)
	s.cfg.BotUploadRetentionHours = 0 // 旧行为：播完即回收
	upload := createTestUploadFile(t, s, dataDir, "upload-legacy")
	s.markUploadPlayed(upload.ID)
	s.sweepUnreferencedUploads()

	if _, err := os.Stat(s.uploadAbsolutePath(&upload)); !os.IsNotExist(err) {
		t.Fatalf("retention=0 应立即回收已播上传，实际错误：%v", err)
	}
}

func TestPendingDeleteSweptImmediatelyRegardlessOfRetention(t *testing.T) {
	s, dataDir := newUploadCleanupTestService(t)
	upload := createTestUploadFile(t, s, dataDir, "upload-user-deleted")
	if err := s.DeleteUpload("user-1", upload.ID); err != nil {
		t.Fatal(err)
	}
	// 未播放过、无队列引用：用户显式删除应立即回收（不受保留窗口影响）
	s.sweepUnreferencedUploads()
	if _, err := os.Stat(s.uploadAbsolutePath(&upload)); !os.IsNotExist(err) {
		t.Fatalf("用户显式删除应立即回收，实际错误：%v", err)
	}
}
