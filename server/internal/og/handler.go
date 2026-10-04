package og

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
)

// Handler handles OG preview requests
type Handler struct {
	service *Service
	cfg     *config.Config
}

// NewHandler creates a new OG handler
func NewHandler(db *gorm.DB, cfg *config.Config) *Handler {
	return &Handler{
		service: NewService(db),
		cfg:     cfg,
	}
}

// RegisterRoutes registers OG routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/preview", h.Preview)
}

// Preview handles GET /api/v1/preview?url=...
func (h *Handler) Preview(c *gin.Context) {
	url := c.Query("url")
	if url == "" {
		c.JSON(http.StatusBadRequest, errors.FromError(c, errors.ErrBadRequest.WithDetails("url parameter is required")))
		return
	}

	preview, err := h.service.FetchPreview(url)
	if err != nil {
		errMsg := err.Error()
		// Return specific error for SSRF attempts
		if strings.Contains(errMsg, "SSRF") {
			c.JSON(http.StatusForbidden, errors.FromError(c, errors.ErrForbidden.WithDetails(errMsg)))
			return
		}
		// Return empty preview on other errors (graceful degradation)
		c.JSON(http.StatusOK, gin.H{
			"code": "OK",
			"data": gin.H{
				"url":         url,
				"title":       "",
				"description": "",
				"image":       "",
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": "OK",
		"data": preview,
	})
}
