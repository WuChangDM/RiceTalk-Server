package sfx

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

// Handler exposes the sfx (entrance/exit sound) HTTP API.
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
}

// NewHandler constructs the sfx handler around a shared service.
func NewHandler(db *gorm.DB, cfg *config.Config, svc *Service) *Handler {
	return &Handler{service: svc, db: db, cfg: cfg}
}

// RegisterRoutes registers the sfx routes under /sounds and /users/me/sound-settings.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	sounds := r.Group("/sounds")
	sounds.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		sounds.GET("", h.ListSounds)
		sounds.POST("", h.UploadSound)
		sounds.DELETE("/:id", h.DeleteSound)
		sounds.GET("/:id/file", h.GetSoundFile)
		sounds.POST("/:id/favorite", h.FavoriteSound)
		sounds.DELETE("/:id/favorite", h.UnfavoriteSound)
	}

	users := r.Group("/users/me")
	users.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		users.GET("/sound-settings", h.GetSoundSettings)
		users.PUT("/sound-settings", h.UpdateSoundSettings)
	}
}

// UploadSound handles sound upload (multipart "file").
func (h *Handler) UploadSound(c *gin.Context) {
	userID := middleware.GetUserID(c)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "file required"))
		return
	}
	defer file.Close()

	sound, err := h.service.UploadSound(userID, header.Filename, header.Header.Get("Content-Type"), header.Size, file)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, soundJSON(sound))
}

// ListSounds returns the full shared sound library, enriched with uploader names
// and the caller's favorite flags.
func (h *Handler) ListSounds(c *gin.Context) {
	userID := middleware.GetUserID(c)
	items, err := h.service.ListSounds(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for i := range items {
		it := &items[i]
		out = append(out, gin.H{
			"id":           it.ID,
			"userId":       it.UserID,
			"title":        it.Title,
			"fileSize":     it.FileSize,
			"mimeType":     it.MimeType,
			"duration":     it.Duration,
			"isPreset":     it.IsPreset,
			"uploaderName": it.UploaderName,
			"favorited":    it.Favorited,
			"createdAt":    it.CreatedAt,
		})
	}
	errors.Success(c, gin.H{"items": out})
}

// DeleteSound deletes a sound (uploader only).
func (h *Handler) DeleteSound(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.service.DeleteSound(userID, c.Param("id")); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"deleted": true})
}

// GetSoundFile streams a sound file for in-app preview (authenticated).
func (h *Handler) GetSoundFile(c *gin.Context) {
	absPath, mimeType, err := h.service.SoundFileAbsPath(c.Param("id"))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if mimeType != "" {
		c.Header("Content-Type", mimeType)
	}
	c.Header("Cache-Control", "private, max-age=3600")
	c.File(absPath)
}

// GetSoundSettings returns the caller's join/leave sound settings.
func (h *Handler) GetSoundSettings(c *gin.Context) {
	userID := middleware.GetUserID(c)
	st, err := h.service.GetSettings(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, st)
}

// UpdateSoundSettings upserts the caller's join/leave sound settings.
func (h *Handler) UpdateSoundSettings(c *gin.Context) {
	userID := middleware.GetUserID(c)
	var in SettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "invalid body"))
		return
	}
	st, err := h.service.UpdateSettings(userID, in)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, st)
}

// FavoriteSound marks a sound as favorited by the caller.
func (h *Handler) FavoriteSound(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.service.FavoriteSound(userID, c.Param("id")); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"favorited": true})
}

// UnfavoriteSound removes a favorite for the caller.
func (h *Handler) UnfavoriteSound(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.service.UnfavoriteSound(userID, c.Param("id")); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"favorited": false})
}

func soundJSON(s *model.UserSound) gin.H {
	return gin.H{
		"id":        s.ID,
		"userId":    s.UserID,
		"title":     s.Title,
		"fileSize":  s.FileSize,
		"mimeType":  s.MimeType,
		"duration":  s.Duration,
		"isPreset":  s.IsPreset,
		"createdAt": s.CreatedAt,
	}
}
