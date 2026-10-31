package files

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/storage"
	"ridgericetalk/middleware"
)

// Handler handles HTTP requests for file operations
type Handler struct {
	storage storage.Storage
	db      *gorm.DB
	cfg     *config.Config
}

// NewHandler creates a new file handler
func NewHandler(db *gorm.DB, st storage.Storage, cfg *config.Config) *Handler {
	return &Handler{storage: st, db: db, cfg: cfg}
}

// RegisterRoutes registers file routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	files := r.Group("/files")
	files.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		files.POST("", h.Upload)
		// Deprecated alias kept for backward compatibility
		files.POST("/upload", h.Upload)
	}
	r.GET("/files/*filepath", middleware.AuthRequired(h.cfg, h.db), h.Download)
}

// Upload handles file upload. It accepts multipart field names "file",
// "files[]", or "files" for broader client compatibility.
func (h *Handler) Upload(c *gin.Context) {
	var file multipart.File
	var header *multipart.FileHeader
	var err error

	for _, field := range []string{"file", "files[]", "files"} {
		file, header, err = c.Request.FormFile(field)
		if err == nil {
			break
		}
	}
	if err != nil {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "file required"))
		return
	}
	defer file.Close()

	// Check file size (max 100MB)
	if header.Size > 100*1024*1024 {
		errors.JSONError(c, errors.New(errors.FILE_TOO_LARGE, "file too large (max 100MB)"))
		return
	}

	// Read first 512 bytes for magic number validation
	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	buf = buf[:n]

	// Validate file type using magic numbers
	detectedType, valid := storage.ValidateFileType(bytes.NewReader(buf),
		"image/jpeg", "image/png", "image/gif", "image/webp", "image/svg+xml",
		"audio/mpeg", "audio/wav", "audio/ogg",
		"video/mp4",
		"application/pdf", "application/zip", "text/plain")
	if !valid {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "unsupported or invalid file type"))
		return
	}

	// Reconstruct reader: validated buffer + remaining file content
	reader := io.MultiReader(bytes.NewReader(buf), file)

	// Generate storage key
	key := storage.GenerateKey("uploads", header.Filename)

	// Store file (use detected type instead of client-provided Content-Type)
	_, err = h.storage.Put(key, reader, header.Size, detectedType)
	if err != nil {
		errors.JSONError(c, errors.New(errors.FILE_UPLOAD_FAILED, "failed to save file"))
		return
	}

	errors.Success(c, gin.H{
		"url":      storage.URLWithBase(key, h.publicFilesBase(c)),
		"filename": header.Filename,
		"size":     header.Size,
	})
}

// publicFilesBase 返回上传响应里文件 URL 的对外 base（形如 http://host:port/api/v1/files）。
//
// 优先级（task-100）：
//  1. 配置里的 RRT_PUBLIC_ADDRESS **带显式端口** → 以配置为准（运维已写明对外入口）；
//  2. 否则用本次请求的 Host 兜底 —— NAT 端口映射 / 预构建包部署下，客户端实际访问的
//     host:port 才是可达地址；配置缺端口时若在这里猜端口（恒定追加 RRT_PORT），反代
//     （443/80）部署会拼出不可达地址；
//  3. Host 也为空（极少数直连测试）→ 返回空串，storage.URLWithBase 退回相对路径，保持
//     本地开发/单测的既有口径。
func (h *Handler) publicFilesBase(c *gin.Context) string {
	if h.cfg != nil {
		if base, ok := storage.AbsoluteFilesBase(h.cfg.PublicAddress); ok {
			return base
		}
	}
	host := strings.TrimSpace(c.Request.Host)
	if host == "" {
		return ""
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); proto != "" {
		scheme = strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
	}
	return scheme + "://" + host + storage.FilesPathPrefix
}

// Download handles file download
func (h *Handler) Download(c *gin.Context) {
	filepath := strings.TrimPrefix(c.Param("filepath"), "/")
	if filepath == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	reader, size, contentType, err := h.storage.Get(filepath)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	defer reader.Close()

	c.Header("Content-Type", contentType)
	c.Header("Content-Length", fmt.Sprintf("%d", size))
	io.Copy(c.Writer, reader)
}
