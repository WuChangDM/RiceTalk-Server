package bots

import (
	"path/filepath"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/model"
)

const neteaseResolvedURLTTL = 30 * time.Minute

// ResolveWorkerTrack resolves short-lived remote URLs and local upload paths at
// playback time. This keeps expired provider URLs out of the persisted queue.
func (s *Service) ResolveWorkerTrack(source, trackID string) (string, int64, error) {
	switch source {
	case "netease":
		audioURL, err := s.GetNeteaseURL(trackID)
		if err != nil {
			return "", 0, err
		}
		return audioURL, time.Now().Add(neteaseResolvedURLTTL).UnixMilli(), nil
	case "upload":
		var upload model.BotUploadAudio
		if err := s.db.Where("id = ?", trackID).First(&upload).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return "", 0, errors.ErrNotFound
			}
			return "", 0, errors.ErrInternal
		}
		s.markUploadPlayed(upload.ID)
		audioPath := upload.FilePath
		if !filepath.IsAbs(audioPath) {
			audioPath = filepath.Join(s.cfg.LocalDataPath, audioPath)
		}
		return filepath.Clean(audioPath), 0, nil
	default:
		return "", 0, errors.New(errors.SYSTEM_BAD_REQUEST, "unsupported music source")
	}
}
