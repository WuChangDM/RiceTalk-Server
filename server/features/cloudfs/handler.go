package cloudfs

import (
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles cloud filesystem HTTP requests
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
	// DES-2026-0912-05：分享链接专用的限流器与爆破防护器。两者都是**独立实例**，
	// 不复用全局 limiter，也不与登录爆破防护共享计数。
	shareLimiter   *middleware.RateLimiter
	shareProtector *middleware.BruteForceProtector
}

// NewHandler creates a new cloudfs handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{
		service:        NewService(db, cfg),
		db:             db,
		cfg:            cfg,
		hub:            hub,
		shareLimiter:   middleware.NewRateLimiter(shareRateLimitRequests, shareRateLimitBurst),
		shareProtector: middleware.NewBruteForceProtector(),
	}
}

// Service returns the underlying cloudfs service (used by other handlers e.g. message attachments).
func (h *Handler) Service() *Service {
	return h.service
}

// RegisterRoutes registers cloudfs routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	cf := r.Group("/cloudfs")
	cf.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		cf.GET("/list", h.List)
		cf.POST("/folder", h.CreateFolder)
		cf.POST("/upload", h.Upload)
		cf.GET("/download", h.Download)
		cf.GET("/preview", h.Preview)
		cf.DELETE("/delete", h.Delete)
		cf.PATCH("/rename", h.Rename)
		cf.POST("/move", h.Move)
		cf.GET("/trash", h.ListTrash)
		cf.POST("/trash/restore", h.RestoreTrash)
		cf.DELETE("/trash", h.PurgeTrash)
		cf.DELETE("/trash/all", h.EmptyTrash)
		cf.GET("/search", h.Search)
		cf.GET("/usage", h.GetUsage) // L17: 暴露存储配额 API
		// DES-2026-0912-05：分享链接的管理面（创建/列出/撤销）。这三条与文件接口
		// 同属鉴权路由；真正的免鉴权入口只有 RegisterShareRoutes 注册的三条。
		cf.POST("/share", h.CreateShare)
		cf.GET("/shares", h.ListShares)
		cf.DELETE("/share/:shareId", h.RevokeShare)
	}
}

// List returns items at a path
func (h *Handler) List(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	userID := middleware.GetUserID(c)
	listPath := c.Query("path")
	if listPath == "" {
		listPath = "/"
	}

	items, path, err := h.service.List(spaceID, listPath, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"items": items,
		"path":  path,
	})
}

// CreateFolder creates a new folder
func (h *Handler) CreateFolder(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	userID := middleware.GetUserID(c)
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if err := h.service.CreateFolder(spaceID, body.Path, body.Name, userID); err != nil {
		// Propagate the service error so contract-relevant codes survive
		// (e.g. CLOUDFS_ALREADY_EXISTS -> 409 instead of a blanket 500).
		errors.JSONError(c, err)
		return
	}

	// C37: Broadcast cloudfs_folder_created event to all clients
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "cloudfs_folder_created",
			"payload": map[string]interface{}{
				"path":      body.Path,
				"name":      body.Name,
				"createdBy": userID,
			},
		})
		h.hub.Broadcast(data)
	}

	errors.Success(c, gin.H{"message": "folder created"})
}

// Upload handles file upload
func (h *Handler) Upload(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	userID := middleware.GetUserID(c)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("no file provided"))
		return
	}
	defer file.Close()

	// 文件大小校验：优先读取 AdminConfig.MaxFileSize（单位 MB，0=不限），
	// 若未配置则回退到 cfg.CloudFSMaxFileSize（字节）。
	maxSize := int64(0)
	var adminCfg model.AdminConfig
	if err := h.db.First(&adminCfg).Error; err == nil {
		if adminCfg.MaxFileSize > 0 {
			maxSize = int64(adminCfg.MaxFileSize) * 1024 * 1024
		}
	}
	if maxSize <= 0 {
		maxSize = h.cfg.CloudFSMaxFileSize
		if maxSize <= 0 {
			maxSize = 100 << 20
		}
	}
	if maxSize > 0 && header.Size > maxSize {
		errors.JSONError(c, errors.New(errors.FILE_TOO_LARGE, fmt.Sprintf("file exceeds %d bytes", maxSize)))
		return
	}

	uploadPath := c.PostForm("path")
	if uploadPath == "" {
		uploadPath = "/"
	}

	if err := h.service.Upload(spaceID, uploadPath, filepath.Base(header.Filename), file, userID); err != nil {
		errors.JSONError(c, err)
		return
	}

	// C37: Broadcast cloudfs_file_uploaded event to all clients
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "cloudfs_file_uploaded",
			"payload": map[string]interface{}{
				"path":       uploadPath,
				"name":       filepath.Base(header.Filename),
				"size":       header.Size,
				"uploadedBy": userID,
			},
		})
		h.hub.Broadcast(data)
	}

	errors.Success(c, gin.H{"message": "uploaded"})
}

// Download serves a file
func (h *Handler) Download(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	filePath := c.Query("path")
	if filePath == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	info, err := h.service.DownloadInfo(spaceID, filePath, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", strconv.Quote(info.FileName)))
	c.Header("Content-Type", info.MimeType)
	setChecksumHeader(c, info.Checksum)
	c.File(info.PhysicalPath)
}

// setChecksumHeader advertises the stored SHA-256 of the served content so a
// client can verify the download (DES-2026-0912-02 §4.6). Absent for files
// uploaded before checksum recording, so clients must treat it as optional.
func setChecksumHeader(c *gin.Context, checksum string) {
	if checksum == "" {
		return
	}
	c.Header("X-File-Checksum", "sha256="+checksum)
}

// inlinePreviewExactTypes is the MIME whitelist for `GET /cloudfs/preview`
// (DES-2026-0912-02 §4.1). Anything not matched here — including SVG and HTML,
// see inlinePreviewDeniedTypes — is served with Content-Disposition: attachment.
var inlinePreviewExactTypes = map[string]bool{
	"application/json": true,
	"application/xml":  true,
	"application/pdf":  true,
	"image/png":        true,
	"image/jpeg":       true,
	"image/gif":        true,
	"image/webp":       true,
	"image/avif":       true,
	"image/bmp":        true,
	"video/mp4":        true,
	"video/webm":       true,
	"video/quicktime":  true,
	"audio/mpeg":       true,
	"audio/wav":        true,
	"audio/ogg":        true,
	"audio/aac":        true,
}

// inlinePreviewDeniedTypes overrides the `text/*` prefix rule below.
//
//   - image/svg+xml: the design keeps SVG download-only (storage.SanitizeSVG is
//     string-level, not a parser, so inline SVG keeps a residual XSS risk).
//   - text/html and application/xhtml+xml: deviation from the design's literal
//     `text/*` whitelist. Inline HTML is a stored-XSS primitive; these two are
//     therefore forced to download. Documented in the DES-2026-0912-02
//     implementation-progress section.
var inlinePreviewDeniedTypes = map[string]bool{
	"image/svg+xml":         true,
	"text/html":             true,
	"application/xhtml+xml": true,
}

// isInlinePreviewAllowed reports whether mimeType may be served inline.
func isInlinePreviewAllowed(mimeType string) bool {
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil {
		// Fall back to a manual strip of any parameters ("text/plain; charset=...").
		mediaType = strings.TrimSpace(strings.Split(mimeType, ";")[0])
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType == "" {
		return false
	}
	if inlinePreviewDeniedTypes[mediaType] {
		return false
	}
	if inlinePreviewExactTypes[mediaType] {
		return true
	}
	return strings.HasPrefix(mediaType, "text/")
}

// Preview serves file content for in-browser/desktop preview.
//
// Same authentication and visibility rules as Download (Space membership is
// enforced inside Service.Download, and the route group carries AuthRequired),
// but the response is inline for whitelisted MIME types and falls back to
// `attachment` for everything else. Range requests work because the response is
// streamed through c.File -> http.ServeContent.
//
// The existing /cloudfs/download semantics (always attachment) are unchanged.
func (h *Handler) Preview(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	filePath := c.Query("path")
	if filePath == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	info, err := h.service.DownloadInfo(spaceID, filePath, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	disposition := "attachment"
	contentType := "application/octet-stream"
	if isInlinePreviewAllowed(info.MimeType) {
		disposition = "inline"
		contentType = info.MimeType
	}

	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%s", disposition, strconv.Quote(info.FileName)))
	c.Header("Content-Type", contentType)
	// Never let a client re-interpret the body as something renderable.
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Accept-Ranges", "bytes")
	setChecksumHeader(c, info.Checksum)
	c.File(info.PhysicalPath)
}

// Delete removes a file or folder
func (h *Handler) Delete(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	itemPath := c.Query("path")
	if itemPath == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	if err := h.service.Delete(spaceID, itemPath, userID); err != nil {
		errors.JSONError(c, err)
		return
	}

	// C37: Broadcast cloudfs_item_deleted event to all clients
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "cloudfs_item_deleted",
			"payload": map[string]interface{}{
				"path":      itemPath,
				"deletedBy": userID,
			},
		})
		h.hub.Broadcast(data)
	}

	errors.Success(c, gin.H{"message": "deleted"})
}

// Rename renames a file or folder within its current parent folder
// (PATCH /cloudfs/rename, DES-2026-0912-02 §4.2).
func (h *Handler) Rename(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	var body struct {
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid JSON body"))
		return
	}
	if body.Path == "" || body.NewName == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("path and newName are required"))
		return
	}

	userID := middleware.GetUserID(c)
	res, err := h.service.Rename(spaceID, body.Path, body.NewName, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"message": "renamed",
		"type":    res.Kind,
		"path":    res.NewPath,
		"name":    res.Name,
	})
}

// Move moves a file or folder under another folder (POST /cloudfs/move,
// DES-2026-0912-02 §4.2), keeping its current name.
func (h *Handler) Move(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	var body struct {
		Path       string `json:"path"`
		TargetPath string `json:"targetPath"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid JSON body"))
		return
	}
	if body.Path == "" || body.TargetPath == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("path and targetPath are required"))
		return
	}

	userID := middleware.GetUserID(c)
	res, err := h.service.Move(spaceID, body.Path, body.TargetPath, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"message": "moved",
		"type":    res.Kind,
		"path":    res.NewPath,
		"name":    res.Name,
	})
}

// ListTrash returns the space's deleted items (GET /cloudfs/trash,
// DES-2026-0912-02 §4.3).
func (h *Handler) ListTrash(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	items, err := h.service.ListTrash(spaceID, middleware.GetUserID(c))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if items == nil {
		items = []TrashItem{}
	}
	errors.Success(c, gin.H{
		"items":      items,
		"retainDays": h.service.trashRetentionDays(),
	})
}

// RestoreTrash revives a deleted item (POST /cloudfs/trash/restore).
func (h *Handler) RestoreTrash(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	var body struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid JSON body"))
		return
	}
	if body.ID == "" && body.Path == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("id or path is required"))
		return
	}

	item, err := h.service.RestoreTrash(spaceID, body.ID, body.Path, middleware.GetUserID(c))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "restored", "type": item.Type, "path": item.Path, "name": item.Name})
}

// PurgeTrash permanently deletes one item (DELETE /cloudfs/trash).
func (h *Handler) PurgeTrash(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	id := c.Query("id")
	itemPath := c.Query("path")
	if id == "" && itemPath == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("id or path is required"))
		return
	}

	if err := h.service.PurgeTrash(spaceID, id, itemPath, middleware.GetUserID(c)); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "purged"})
}

// EmptyTrash permanently deletes every deleted item of the space
// (DELETE /cloudfs/trash/all).
func (h *Handler) EmptyTrash(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	count, err := h.service.EmptyTrash(spaceID, middleware.GetUserID(c))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "trash emptied", "purged": count})
}

// Search finds files by name across the space, optionally limited to a subtree
// (GET /cloudfs/search, DES-2026-0912-02 §4.4).
func (h *Handler) Search(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	query := c.Query("q")
	items, err := h.service.Search(spaceID, query, c.Query("path"), middleware.GetUserID(c))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if items == nil {
		items = []CloudFileItem{}
	}

	errors.Success(c, gin.H{
		"items": items,
		"query": strings.TrimSpace(query),
	})
}

// GetUsage returns storage usage and quota (L17)
func (h *Handler) GetUsage(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	used, quota, err := h.service.GetUsage(spaceID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	usedPercent := 0.0
	if quota > 0 {
		usedPercent = float64(used) / float64(quota) * 100
	}

	errors.Success(c, gin.H{
		"used":        used,
		"quota":       quota,
		"usedPercent": usedPercent,
	})
}
