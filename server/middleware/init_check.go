package middleware

import (
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/serverstate"
)

var (
	serverInitialized   bool
	serverInitCheckOnce sync.Once
)

// IsServerInitialized returns the cached initialization status.
func IsServerInitialized() bool {
	return serverInitialized
}

// MarkServerInitialized marks the server as initialized in memory.
// Call this after successful owner creation.
func MarkServerInitialized() {
	serverInitialized = true
}

// ResetServerInitialized resets the initialization flag (for testing only).
func ResetServerInitialized() {
	serverInitialized = false
	serverInitCheckOnce = sync.Once{}
}

// InitCheck returns a middleware that rejects all requests with 403 when the
// server has not been initialized, except for a whitelist of bootstrap endpoints.
//
// Deprecated: use InitCheckWithState for new code. This variant is kept for
// tests and legacy callers that do not yet inject a serverstate.Manager.
func InitCheck(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		method := c.Request.Method

		// Whitelist: these endpoints are allowed when server is not initialized
		if isBootstrapWhitelist(path, method) {
			c.Next()
			return
		}

		// Check cached initialization status first (fast path)
		if IsServerInitialized() {
			c.Next()
			return
		}

		// Check if any user exists (server initialized) — lazy init with sync.Once
		serverInitCheckOnce.Do(func() {
			var count int64
			if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
				// On error, keep initialized=false; will retry on next request
				return
			}
			if count > 0 {
				MarkServerInitialized()
			}
		})

		if !IsServerInitialized() {
			c.AbortWithStatusJSON(errors.ErrServerNotInit.Status, errors.FromError(c, errors.ErrServerNotInit))
			return
		}

		c.Next()
	}
}

// InitCheckWithState returns a middleware that uses serverstate.Manager as the
// source of truth for initialization status. It still honors the legacy
// package-level flag set by MarkServerInitialized for test compatibility.
func InitCheckWithState(stateMgr *serverstate.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		method := c.Request.Method

		if isBootstrapWhitelist(path, method) {
			c.Next()
			return
		}

		if stateMgr != nil && stateMgr.IsInitialized() {
			c.Next()
			return
		}

		// Legacy fallback for tests and migration paths.
		if IsServerInitialized() {
			c.Next()
			return
		}

		c.AbortWithStatusJSON(errors.ErrServerNotInit.Status, errors.FromError(c, errors.ErrServerNotInit))
	}
}

func isBootstrapWhitelist(path, method string) bool {
	// Always allow health checks
	if strings.HasSuffix(path, "/health") || strings.HasSuffix(path, "/healthz") {
		return true
	}

	// Server info endpoint
	if strings.HasSuffix(path, "/server/info") && method == http.MethodGet {
		return true
	}

	// Server network config endpoint (public discovery)
	if strings.HasSuffix(path, "/server/network") && method == http.MethodGet {
		return true
	}

	// Admin bootstrap API endpoints (only for initial setup)
	if strings.HasSuffix(path, "/admin/bootstrap/status") && method == http.MethodGet {
		return true
	}
	if strings.HasSuffix(path, "/admin/bootstrap/verify") && method == http.MethodPost {
		return true
	}
	if strings.HasSuffix(path, "/admin/bootstrap/register") && method == http.MethodPost {
		return true
	}

	// Admin static SPA and assets must be reachable before initialization
	// so the owner can complete bootstrap via the web UI. Admin API lives under /api/admin.
	// N15：HEAD 与 GET 同权放行——服务器未初始化（无 Owner）时，运维/监控常以
	// HEAD 探活管理页可用性（如部署演练手册的 curl -sI /admin）。此前仅放行
	// GET，导致 HEAD /admin 落入 503 AUTH_SERVER_NOT_INITIALIZED 误报。
	if strings.HasPrefix(path, "/admin") && (method == http.MethodGet || method == http.MethodHead) {
		return true
	}

	return false
}
