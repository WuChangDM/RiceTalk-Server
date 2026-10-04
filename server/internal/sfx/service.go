package sfx

import (
	"bytes"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
)

const (
	// MaxSoundFileSize is the per-file upload cap, aligned with Discord soundboard (512KB).
	MaxSoundFileSize = 512 * 1024
	// MaxSoundDuration is the per-file duration cap in seconds, aligned with Discord soundboard.
	MaxSoundDuration = 5
)

// allowedSoundTypes narrows uploads to MP3/OGG/WAV (stricter than bot music uploads).
var allowedSoundTypes = map[string]bool{
	"audio/mpeg":    true,
	"audio/mp3":     true,
	"audio/ogg":     true,
	"audio/wav":     true,
	"audio/x-wav":   true,
	"audio/wave":    true,
	"audio/vnd.wav": true,
}

// Player broadcasts an audio file into a LiveKit room (cgo impl in player_cgo.go,
// no-op stub in player_nocgo.go). Abstracted so tests can observe playback attempts.
type Player interface {
	// PlayOnce converts srcAbsPath to opus and plays it into the room. volume is 0-200.
	PlayOnce(roomID, srcAbsPath string, volume int) error
	// Disconnect leaves the room (idempotent).
	Disconnect()
}

// Service manages the shared sound-effect library and per-user join/leave bindings.
type Service struct {
	db     *gorm.DB
	cfg    *config.Config
	hub    *realtime.Hub
	player Player
}

// NewService constructs the sfx service.
func NewService(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Service {
	return &Service{
		db:     db,
		cfg:    cfg,
		hub:    hub,
		player: NewSfxPlayer(cfg),
	}
}

// ---- Library management ----

// UploadSound validates and stores an uploaded sound effect.
func (s *Service) UploadSound(userID, filename, mimeType string, fileSize int64, reader io.Reader) (*model.UserSound, error) {
	if fileSize > MaxSoundFileSize {
		return nil, errors.New(errors.FILE_TOO_LARGE, "sound file too large (max 512KB)")
	}
	if !allowedSoundTypes[mimeType] {
		return nil, errors.New(errors.FILE_INVALID_TYPE, "only MP3/OGG/WAV allowed")
	}

	// Per-user quota
	maxPerUser := s.cfg.SfxMaxPerUser
	if maxPerUser <= 0 {
		maxPerUser = 20
	}
	var count int64
	if err := s.db.Model(&model.UserSound{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return nil, errors.ErrInternal
	}
	if count >= int64(maxPerUser) {
		return nil, errors.New(errors.SYSTEM_BAD_REQUEST, "sound quota reached (max "+strconv.Itoa(maxPerUser)+" per user)")
	}

	// Verify magic number (first 512 bytes)
	header := make([]byte, 512)
	n, err := io.ReadFull(reader, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, errors.ErrInternal
	}
	if !isValidSoundMagic(header[:n]) {
		return nil, errors.New(errors.FILE_INVALID_TYPE, "invalid audio file content")
	}
	reader = io.MultiReader(bytes.NewReader(header[:n]), reader)

	uploadDir := filepath.Join(s.cfg.LocalDataPath, "uploads/sounds")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, errors.ErrInternal
	}
	fileID := idgen.GenerateID(idgen.PrefixFile)
	filePath := filepath.Join(uploadDir, fileID+filepath.Ext(filename))
	filePath = filepath.Clean(filePath)
	// Ensure path stays within uploadDir
	absDir, _ := filepath.Abs(uploadDir)
	absPath, _ := filepath.Abs(filePath)
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
		return nil, errors.ErrForbidden
	}

	out, err := os.Create(filePath)
	if err != nil {
		return nil, errors.ErrInternal
	}
	written, err := io.Copy(out, reader)
	out.Close()
	if err != nil {
		os.Remove(filePath)
		return nil, errors.ErrInternal
	}

	// Probe duration; hard cap at MaxSoundDuration. ffprobe failure returns 0 and is
	// tolerated (consistent with bot uploads) so environments without ffprobe still work.
	duration := audioDuration(s.ffprobe(), filePath)
	if duration > MaxSoundDuration {
		os.Remove(filePath)
		return nil, errors.New(errors.FILE_INVALID_TYPE, "sound too long (max 5s)")
	}

	// Store path relative to LocalDataPath so playback can resolve it consistently.
	relPath, _ := filepath.Rel(s.cfg.LocalDataPath, filePath)
	if relPath == "" || strings.HasPrefix(relPath, "..") {
		relPath = filePath
	}
	sound := &model.UserSound{
		ID:        fileID,
		UserID:    userID,
		Title:     filename,
		FilePath:  relPath,
		FileSize:  written,
		MimeType:  mimeType,
		Duration:  duration,
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(sound).Error; err != nil {
		os.Remove(filePath)
		return nil, errors.ErrInternal
	}
	return sound, nil
}

// ffprobe resolves the ffprobe binary path (test seam overridable via cfg).
func (s *Service) ffprobe() string { return ffprobePath(s.cfg) }

// SoundItem is a library sound enriched with uploader display info and the
// requesting user's favorite flag.
type SoundItem struct {
	model.UserSound
	UploaderName string `json:"uploaderName"`
	Favorited    bool   `json:"favorited"`
}

// ListSounds returns the full shared library for the requesting user, enriched with
// uploader names and that user's favorite flags. Ordered: presets first, then newest.
func (s *Service) ListSounds(requesterID string) ([]SoundItem, error) {
	var sounds []model.UserSound
	if err := s.db.Order("is_preset DESC, created_at DESC").Find(&sounds).Error; err != nil {
		return nil, errors.ErrInternal
	}

	// Batch-load uploader usernames/display names.
	uploaderIDs := make([]string, 0, len(sounds))
	seen := map[string]bool{}
	for _, sd := range sounds {
		if sd.UserID != presetUserID && !seen[sd.UserID] {
			seen[sd.UserID] = true
			uploaderIDs = append(uploaderIDs, sd.UserID)
		}
	}
	names := map[string]string{}
	if len(uploaderIDs) > 0 {
		var users []model.User
		if err := s.db.Select("id, username, display_name").Where("id IN ?", uploaderIDs).Find(&users).Error; err == nil {
			for _, u := range users {
				if u.DisplayName != "" {
					names[u.ID] = u.DisplayName
				} else {
					names[u.ID] = u.Username
				}
			}
		}
	}

	// Load the requester's favorites.
	faved := map[string]bool{}
	var favs []model.UserSoundFavorite
	if err := s.db.Where("user_id = ?", requesterID).Find(&favs).Error; err == nil {
		for _, f := range favs {
			faved[f.SoundID] = true
		}
	}

	items := make([]SoundItem, 0, len(sounds))
	for _, sd := range sounds {
		item := SoundItem{UserSound: sd, Favorited: faved[sd.ID]}
		if sd.IsPreset {
			item.UploaderName = "软件自带"
		} else {
			item.UploaderName = names[sd.UserID]
		}
		items = append(items, item)
	}
	return items, nil
}

// DeleteSound soft-deletes a sound (uploader only), clears all bindings and favorites
// that reference it, and removes the file asynchronously. Preset sounds cannot be deleted.
func (s *Service) DeleteSound(userID, soundID string) error {
	// Preset sounds are built-in and can never be deleted.
	var probe model.UserSound
	if err := s.db.Select("id, is_preset").Where("id = ?", soundID).First(&probe).Error; err == nil && probe.IsPreset {
		return errors.New(errors.AUTH_FORBIDDEN, "preset sounds cannot be deleted")
	}

	var sound model.UserSound
	if err := s.db.Where("id = ? AND user_id = ?", soundID, userID).First(&sound).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New(errors.FILE_NOT_FOUND, "sound not found")
		}
		return errors.ErrInternal
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&sound).Error; err != nil {
			return err
		}
		// Clear every binding that references this sound (auto-clear references).
		if err := tx.Model(&model.UserSoundSettings{}).
			Where("join_sound_id = ?", soundID).
			Update("join_sound_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.UserSoundSettings{}).
			Where("leave_sound_id = ?", soundID).
			Update("leave_sound_id", nil).Error; err != nil {
			return err
		}
		// Remove everyone's favorites of this sound.
		if err := tx.Where("sound_id = ?", soundID).Delete(&model.UserSoundFavorite{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return errors.ErrInternal
	}

	// Best-effort file removal; on Windows an in-flight play may hold the file open,
	// so a failure here is only logged.
	absPath := s.absPath(sound.FilePath)
	go func() {
		if err := os.Remove(absPath); err != nil {
			log.Printf("[sfx] remove sound file %s failed: %v", absPath, err)
		}
	}()
	return nil
}

// SoundFileAbsPath resolves a sound's absolute file path with traversal protection.
func (s *Service) SoundFileAbsPath(soundID string) (absPath, mimeType string, err error) {
	var sound model.UserSound
	if dbErr := s.db.Where("id = ?", soundID).First(&sound).Error; dbErr != nil {
		if dbErr == gorm.ErrRecordNotFound {
			return "", "", errors.New(errors.FILE_NOT_FOUND, "sound not found")
		}
		return "", "", errors.ErrInternal
	}
	abs := s.absPath(sound.FilePath)
	// Traversal protection: resolved path must stay inside LocalDataPath.
	base, _ := filepath.Abs(s.cfg.LocalDataPath)
	resolved, _ := filepath.Abs(abs)
	if resolved != base && !strings.HasPrefix(resolved, base+string(filepath.Separator)) {
		return "", "", errors.ErrForbidden
	}
	return resolved, sound.MimeType, nil
}

// absPath joins a stored (relative) path onto LocalDataPath.
func (s *Service) absPath(relPath string) string {
	if filepath.IsAbs(relPath) {
		return filepath.Clean(relPath)
	}
	return filepath.Join(s.cfg.LocalDataPath, relPath)
}

// ---- Per-user bindings ----

// SettingsInput is a partial update to a user's sound settings.
type SettingsInput struct {
	JoinSoundID  *string `json:"joinSoundId"`
	LeaveSoundID *string `json:"leaveSoundId"`
	JoinVolume   *int    `json:"joinVolume"`
	LeaveVolume  *int    `json:"leaveVolume"`
	Enabled      *bool   `json:"enabled"`
}

// GetSettings returns the user's settings, or defaults if none exist (not persisted).
func (s *Service) GetSettings(userID string) (*model.UserSoundSettings, error) {
	var st model.UserSoundSettings
	if err := s.db.Where("user_id = ?", userID).First(&st).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return &model.UserSoundSettings{
				UserID:      userID,
				JoinVolume:  100,
				LeaveVolume: 100,
				Enabled:     true,
			}, nil
		}
		return nil, errors.ErrInternal
	}
	return &st, nil
}

// UpdateSettings upserts the user's settings with validation and clamping.
func (s *Service) UpdateSettings(userID string, in SettingsInput) (*model.UserSoundSettings, error) {
	st, err := s.GetSettings(userID)
	if err != nil {
		return nil, err
	}

	// Collect explicit updates. Use column-based Updates (not Save) because Save skips
	// zero values — Enabled=false would silently not persist (GORM zero-value pitfall).
	updates := map[string]interface{}{}
	if in.JoinSoundID != nil {
		if *in.JoinSoundID == "" {
			st.JoinSoundID = nil
			updates["join_sound_id"] = nil
		} else {
			if dbErr := s.ensureSoundExists(*in.JoinSoundID); dbErr != nil {
				return nil, dbErr
			}
			v := *in.JoinSoundID
			st.JoinSoundID = &v
			updates["join_sound_id"] = v
		}
	}
	if in.LeaveSoundID != nil {
		if *in.LeaveSoundID == "" {
			st.LeaveSoundID = nil
			updates["leave_sound_id"] = nil
		} else {
			if dbErr := s.ensureSoundExists(*in.LeaveSoundID); dbErr != nil {
				return nil, dbErr
			}
			v := *in.LeaveSoundID
			st.LeaveSoundID = &v
			updates["leave_sound_id"] = v
		}
	}
	if in.JoinVolume != nil {
		st.JoinVolume = clampVolume(*in.JoinVolume)
		updates["join_volume"] = st.JoinVolume
	}
	if in.LeaveVolume != nil {
		st.LeaveVolume = clampVolume(*in.LeaveVolume)
		updates["leave_volume"] = st.LeaveVolume
	}
	if in.Enabled != nil {
		st.Enabled = *in.Enabled
		updates["enabled"] = *in.Enabled
	}
	st.UpdatedAt = time.Now()
	updates["updated_at"] = st.UpdatedAt

	if len(updates) == 0 {
		// No-op update; return current state without touching the DB.
		return st, nil
	}

	var result *gorm.DB
	notFound := s.db.Where("user_id = ?", userID).First(&model.UserSoundSettings{}).Error
	if notFound != nil {
		// Insert a fresh row. Use a map (not a struct) so zero values like
		// Enabled=false are written instead of being skipped by GORM.
		create := map[string]interface{}{
			"user_id":      userID,
			"join_volume":  st.JoinVolume,
			"leave_volume": st.LeaveVolume,
			"enabled":      st.Enabled,
			"updated_at":   st.UpdatedAt,
		}
		if st.JoinSoundID != nil {
			create["join_sound_id"] = *st.JoinSoundID
		}
		if st.LeaveSoundID != nil {
			create["leave_sound_id"] = *st.LeaveSoundID
		}
		result = s.db.Model(&model.UserSoundSettings{}).Create(create)
	} else {
		result = s.db.Model(&model.UserSoundSettings{}).
			Where("user_id = ?", userID).
			Updates(updates)
	}
	if result.Error != nil {
		return nil, errors.ErrInternal
	}
	return st, nil
}

// ensureSoundExists verifies a sound exists and is not soft-deleted.
func (s *Service) ensureSoundExists(soundID string) error {
	var count int64
	if err := s.db.Model(&model.UserSound{}).Where("id = ?", soundID).Count(&count).Error; err != nil {
		return errors.ErrInternal
	}
	if count == 0 {
		return errors.New(errors.FILE_NOT_FOUND, "sound not found")
	}
	return nil
}

// FavoriteSound marks a sound as favorited by the user (idempotent).
func (s *Service) FavoriteSound(userID, soundID string) error {
	if err := s.ensureSoundExists(soundID); err != nil {
		return err
	}
	var count int64
	if err := s.db.Model(&model.UserSoundFavorite{}).
		Where("user_id = ? AND sound_id = ?", userID, soundID).Count(&count).Error; err != nil {
		return errors.ErrInternal
	}
	if count > 0 {
		return nil // already favorited
	}
	fav := &model.UserSoundFavorite{
		ID:        idgen.GenerateID(idgen.PrefixFile),
		UserID:    userID,
		SoundID:   soundID,
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(fav).Error; err != nil {
		return errors.ErrInternal
	}
	return nil
}

// UnfavoriteSound removes a favorite (idempotent).
func (s *Service) UnfavoriteSound(userID, soundID string) error {
	if err := s.db.Where("user_id = ? AND sound_id = ?", userID, soundID).
		Delete(&model.UserSoundFavorite{}).Error; err != nil {
		return errors.ErrInternal
	}
	return nil
}

func clampVolume(v int) int {
	if v < 0 {
		return 0
	}
	if v > 200 {
		return 200
	}
	return v
}

// ---- Helpers ----

// isValidSoundMagic checks the file header against MP3/WAV/OGG magic numbers.
func isValidSoundMagic(header []byte) bool {
	if len(header) < 4 {
		return false
	}
	// MP3: ID3 tag or MPEG sync word (0xFFE0)
	if string(header[:3]) == "ID3" || (header[0] == 0xFF && (header[1]&0xE0) == 0xE0) {
		return true
	}
	// WAV: RIFF....WAVE
	if string(header[:4]) == "RIFF" && len(header) >= 12 && string(header[8:12]) == "WAVE" {
		return true
	}
	// OGG / OPUS: OggS
	if string(header[:4]) == "OggS" {
		return true
	}
	return false
}

// audioDuration uses ffprobe to get the duration of an audio file in seconds.
// If ffprobe is unavailable or fails, it returns 0 so uploads still succeed.
func audioDuration(ffprobeBin, filePath string) int {
	cmd := exec.Command(ffprobeBin,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[sfx] ffprobe failed for %s: %v", filePath, err)
		return 0
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return int(secs)
}
