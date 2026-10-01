package bots

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/model"
)

const uploadDeleteMaxAttempts = 6

// markUploadPlayed marks an upload as eligible for automatic cleanup once no
// queue references remain.
func (s *Service) markUploadPlayed(uploadID string) {
	now := time.Now()
	if err := s.db.Model(&model.BotUploadAudio{}).
		Where("id = ?", uploadID).
		Update("last_played_at", now).Error; err != nil {
		log.Printf("[bot] mark upload played failed uploadID=%s err=%v", uploadID, err)
	}
}

func (s *Service) uploadQueueReferenceCount(uploadID string) (int64, error) {
	var count int64
	err := s.db.Model(&model.BotPlayQueue{}).
		Where("source = ? AND track_id = ?", "upload", uploadID).
		Count(&count).Error
	return count, err
}

func (s *Service) uploadAbsolutePath(upload *model.BotUploadAudio) string {
	if filepath.IsAbs(upload.FilePath) {
		return filepath.Clean(upload.FilePath)
	}
	return filepath.Clean(filepath.Join(s.cfg.LocalDataPath, upload.FilePath))
}

// deleteUploadFileAndRecord deletes the file first, then soft-deletes its
// database record. A locked file is marked pending and retried with backoff.
func (s *Service) deleteUploadFileAndRecord(upload *model.BotUploadAudio) error {
	filePath := s.uploadAbsolutePath(upload)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		if dbErr := s.db.Model(upload).Update("pending_delete", true).Error; dbErr != nil {
			return errors.ErrInternal
		}
		s.scheduleUploadDeleteRetry(upload.ID)
		log.Printf("[bot] upload file delete deferred uploadID=%s path=%s err=%v", upload.ID, filePath, err)
		return nil
	}
	if err := s.db.Delete(upload).Error; err != nil {
		return errors.ErrInternal
	}
	s.uploadCleanupMu.Lock()
	delete(s.uploadDeleteAttempts, upload.ID)
	s.uploadCleanupMu.Unlock()
	return nil
}

func (s *Service) scheduleUploadDeleteRetry(uploadID string) {
	s.uploadCleanupMu.Lock()
	if s.uploadDeleteAttempts == nil {
		s.uploadDeleteAttempts = make(map[string]int)
	}
	attempt := s.uploadDeleteAttempts[uploadID] + 1
	if attempt > uploadDeleteMaxAttempts {
		s.uploadCleanupMu.Unlock()
		log.Printf("[bot] upload delete retry exhausted uploadID=%s", uploadID)
		return
	}
	s.uploadDeleteAttempts[uploadID] = attempt
	s.uploadCleanupMu.Unlock()

	delay := time.Duration(1<<(attempt-1)) * 500 * time.Millisecond
	time.AfterFunc(delay, func() {
		var upload model.BotUploadAudio
		if err := s.db.Where("id = ?", uploadID).First(&upload).Error; err != nil {
			return
		}
		references, err := s.uploadQueueReferenceCount(uploadID)
		if err != nil || references > 0 {
			return
		}
		_ = s.deleteUploadFileAndRecord(&upload)
	})
}

// sweepUnreferencedUploads removes played uploads and pending user deletions
// only after their last queue reference disappears.
func (s *Service) sweepUnreferencedUploads() {
	var uploads []model.BotUploadAudio
	if err := s.db.
		Where("pending_delete = ? OR last_played_at IS NOT NULL", true).
		Order("created_at ASC").
		Find(&uploads).Error; err != nil {
		log.Printf("[bot] scan uploads for cleanup failed: %v", err)
		return
	}
	for i := range uploads {
		references, err := s.uploadQueueReferenceCount(uploads[i].ID)
		if err != nil || references > 0 {
			continue
		}
		_ = s.deleteUploadFileAndRecord(&uploads[i])
	}
}

// enforceUploadQuota evicts the oldest unreferenced uploads until both count
// and byte quotas are satisfied. The newly-created upload is protected from
// immediate eviction so an oversized single file remains visible to its user.
func (s *Service) enforceUploadQuota(protectedID string) {
	var uploads []model.BotUploadAudio
	if err := s.db.Order("created_at ASC").Find(&uploads).Error; err != nil {
		return
	}
	var totalBytes int64
	for i := range uploads {
		totalBytes += uploads[i].FileSize
	}
	count := len(uploads)
	for i := range uploads {
		if count <= s.cfg.BotUploadMaxFiles && totalBytes <= s.cfg.BotUploadMaxTotalBytes {
			break
		}
		upload := &uploads[i]
		if upload.ID == protectedID {
			continue
		}
		references, err := s.uploadQueueReferenceCount(upload.ID)
		if err != nil || references > 0 {
			continue
		}
		if err := s.deleteUploadFileAndRecord(upload); err != nil {
			continue
		}
		var remaining int64
		if err := s.db.Model(&model.BotUploadAudio{}).
			Where("id = ?", upload.ID).
			Count(&remaining).Error; err == nil && remaining == 0 {
			count--
			totalBytes -= upload.FileSize
		}
	}
}
