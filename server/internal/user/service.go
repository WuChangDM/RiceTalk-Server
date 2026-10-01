package user

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/validator"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
)

// Service handles user business logic
type Service struct {
	db       *gorm.DB
	userRepo repositories.UserRepository
}

// userCacheInvalidator is an optional interface implemented by
// CachedUserRepository. It allows the service to invalidate cache entries when
// bypassing the Repository (e.g. direct s.db writes in UpdateProfile/UpdateAvatar).
type userCacheInvalidator interface {
	InvalidateUser(u *model.User)
	InvalidateUserByID(id string)
}

// invalidateUserCacheByID is a no-op when the repository is not cache-backed.
func (s *Service) invalidateUserCacheByID(userID string) {
	if inv, ok := s.userRepo.(userCacheInvalidator); ok {
		inv.InvalidateUserByID(userID)
	}
}

// NewService creates a new user service with a default GORM-backed
// UserRepository (no cache). Use NewServiceWithRepos to inject a cached or
// mock repository.
func NewService(db *gorm.DB) *Service {
	return &Service{
		db:       db,
		userRepo: gormrepo.NewGormUserRepository(db),
	}
}

// NewServiceWithRepos creates a new user service with the given UserRepository.
// Pass a cache-wrapped repository (cacheinfra.NewCachedUserRepository) to
// enable TTL caching per design doc §5B.3.
func NewServiceWithRepos(db *gorm.DB, userRepo repositories.UserRepository) *Service {
	if userRepo == nil {
		userRepo = gormrepo.NewGormUserRepository(db)
	}
	return &Service{db: db, userRepo: userRepo}
}

// GetUser retrieves a user by ID
func (s *Service) GetUser(userID string) (*model.User, error) {
	user, err := s.userRepo.GetByID(context.Background(), userID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, errors.ErrInternal
	}
	return user, nil
}

// GetUsers retrieves a paginated list of users
func (s *Service) GetUsers(page, pageSize int) ([]model.User, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	if err := s.db.Model(&model.User{}).Count(&total).Error; err != nil {
		return nil, 0, errors.ErrInternal
	}

	var users []model.User
	if err := s.db.Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		return nil, 0, errors.ErrInternal
	}

	return users, total, nil
}

// UpdateProfile updates user profile
func (s *Service) UpdateProfile(userID string, updates map[string]interface{}) error {
	// Sanitize inputs
	if displayName, ok := updates["display_name"]; ok {
		s, _ := displayName.(string)
		updates["display_name"] = validator.SanitizeString(s, 64)
	}

	// FIX-2026-0808-01: display name must be unique within every space the user
	// belongs to (excluding the user themself). Case-insensitive, trimmed.
	if newName, ok := updates["display_name"]; ok {
		nameStr := strings.TrimSpace(newName.(string))
		if nameStr == "" {
			return errors.ErrBadRequest.WithDetails("display name is required")
		}
		var spaceIDs []string
		if err := s.db.Table("memberships").Where("user_id = ?", userID).Pluck("space_id", &spaceIDs).Error; err != nil {
			return err
		}
		if len(spaceIDs) > 0 {
			var dup int64
			if err := s.db.Table("users").
				Joins("JOIN memberships ON memberships.user_id = users.id").
				Where("memberships.space_id IN ? AND users.id != ? AND LOWER(TRIM(users.display_name)) = ?", spaceIDs, userID, strings.ToLower(nameStr)).
				Count(&dup).Error; err != nil {
				return err
			}
			if dup > 0 {
				return errors.New(errors.AUTH_DISPLAY_NAME_EXISTS, "display name already used in this space")
			}
		}
	}

	if customStatus, ok := updates["custom_status"]; ok {
		s, _ := customStatus.(string)
		updates["custom_status"] = validator.SanitizeString(s, 128)
	}
	if theme, ok := updates["theme"]; ok {
		s, _ := theme.(string)
		if s != "dark" && s != "light" {
			return errors.ErrBadRequest.WithDetails("theme must be 'dark' or 'light'")
		}
		updates["theme"] = s
	}

	if err := s.db.Model(&model.User{}).Where("id = ?", userID).Updates(updates).Error; err != nil {
		return err
	}
	s.invalidateUserCacheByID(userID)
	return nil
}

// UpdateStatus updates user online status
func (s *Service) UpdateStatus(userID, status, customStatus string) error {
	var presence model.UserPresence
	err := s.db.Where("user_id = ?", userID).First(&presence).Error
	if err == gorm.ErrRecordNotFound {
		presence = model.UserPresence{
			ID:           userID,
			UserID:       userID,
			Status:       status,
			CustomStatus: validator.SanitizeString(customStatus, 128),
			LastSeenAt:   time.Now(),
		}
		return s.db.Create(&presence).Error
	}
	if err != nil {
		return errors.ErrInternal
	}

	presence.Status = status
	presence.CustomStatus = validator.SanitizeString(customStatus, 128)
	presence.LastSeenAt = time.Now()
	return s.db.Save(&presence).Error
}

// TouchLastSeen 仅刷新 LastSeenAt，不改变状态，不触发广播。
// 供心跳端点调用，避免每次心跳都产生 presence_update 广播。
func (s *Service) TouchLastSeen(userID string) error {
	return s.db.Model(&model.UserPresence{}).
		Where("user_id = ?", userID).
		Update("last_seen_at", time.Now()).Error
}

// GetPresence retrieves a user's presence
func (s *Service) GetPresence(userID string) (*model.UserPresence, error) {
	var presence model.UserPresence
	if err := s.db.Where("user_id = ?", userID).First(&presence).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return &model.UserPresence{UserID: userID, Status: "offline"}, nil
		}
		return nil, errors.ErrInternal
	}
	return &presence, nil
}

// GetAllPresences retrieves all user presences
func (s *Service) GetAllPresences() ([]model.UserPresence, error) {
	var presences []model.UserPresence
	if err := s.db.Find(&presences).Error; err != nil {
		return nil, errors.ErrInternal
	}
	return presences, nil
}

// ResetAllToOffline sets every user_presences record to offline.
// Called on server startup so that the first page load after a restart
// does not show every user as online.
func (s *Service) ResetAllToOffline() error {
	return s.db.Model(&model.UserPresence{}).Where("status != ?", "offline").Update("status", "offline").Error
}

// UpdateAvatar updates user avatar
func (s *Service) UpdateAvatar(userID, avatarURL string) error {
	if err := s.db.Model(&model.User{}).Where("id = ?", userID).Update("avatar", avatarURL).Error; err != nil {
		return err
	}
	s.invalidateUserCacheByID(userID)
	return nil
}

// allowedAvatarTypes maps MIME types to file extensions
var allowedAvatarTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// maxAvatarSize is 5MB
const maxAvatarSize = 5 << 20

// SaveAvatar saves an uploaded avatar file and returns the public URL
func (s *Service) SaveAvatar(userID string, fileHeader *multipart.FileHeader, baseURL string) (string, error) {
	// Validate file size
	if fileHeader.Size > maxAvatarSize {
		return "", errors.ErrBadRequest.WithDetails("avatar file too large (max 5MB)")
	}

	// Open uploaded file
	file, err := fileHeader.Open()
	if err != nil {
		return "", errors.ErrInternal
	}
	defer file.Close()

	// Read first 512 bytes to detect MIME type
	header := make([]byte, 512)
	n, err := file.Read(header)
	if err != nil && err != io.EOF {
		return "", errors.ErrInternal
	}

	mimeType := ""
	if n > 0 {
		mimeType = http.DetectContentType(header[:n])
	}

	ext, ok := allowedAvatarTypes[mimeType]
	if !ok {
		return "", errors.ErrBadRequest.WithDetails("invalid image format (allowed: jpg, png, gif, webp)")
	}

	// Reset file read position
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", errors.ErrInternal
	}

	// Build safe file path
	safeName := fmt.Sprintf("%d%s", time.Now().Unix(), ext)
	avatarDir := filepath.Join("storage", "avatars", userID)
	if err := os.MkdirAll(avatarDir, 0755); err != nil {
		return "", errors.ErrInternal
	}

	// Clean filename to prevent path traversal
	safeName = filepath.Base(safeName)
	dstPath := filepath.Join(avatarDir, safeName)
	// Ensure final path is within avatarDir
	if !strings.HasPrefix(filepath.Clean(dstPath), filepath.Clean(avatarDir)+string(filepath.Separator)) {
		return "", errors.ErrForbidden
	}

	// Save file
	dst, err := os.Create(dstPath)
	if err != nil {
		return "", errors.ErrInternal
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		return "", errors.ErrInternal
	}

	// Return public URL
	return fmt.Sprintf("%s/api/avatars/%s/%s", strings.TrimSuffix(baseURL, "/"), userID, safeName), nil
}

// UpdateEmail updates user email with validation
func (s *Service) UpdateEmail(userID, email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return errors.ErrBadRequest.WithDetails("email cannot be empty")
	}
	if !validator.ValidateEmail(email) {
		return errors.ErrBadRequest.WithDetails("invalid email format")
	}

	// Check uniqueness (excluding self)
	var existing model.User
	if err := s.db.Where("email = ? AND id != ?", email, userID).First(&existing).Error; err == nil {
		return errors.ErrBadRequest.WithDetails("email already in use")
	}

	if err := s.db.Model(&model.User{}).Where("id = ?", userID).Update("email", email).Error; err != nil {
		return err
	}
	s.invalidateUserCacheByID(userID)
	return nil
}
