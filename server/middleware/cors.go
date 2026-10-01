package middleware

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
)

// CORSMiddleware returns a lightweight CORS middleware without external dependencies.
// In production, the allowed origins MUST be configured explicitly via RRT_CORS_ORIGINS
// (or through the config.AllowedOrigins slice when a config-aware variant is used).
// If no origin is allowed, the request is rejected with 403.
func CORSMiddleware() gin.HandlerFunc {
	var allowedOrigins []string
	isProd := os.Getenv("RRT_ENV") == "production"
	if isProd {
		origins := os.Getenv("RRT_CORS_ORIGINS")
		if origins != "" {
			allowedOrigins = strings.Split(origins, ",")
			for i := range allowedOrigins {
				allowedOrigins[i] = strings.TrimSpace(allowedOrigins[i])
			}
		}
	}

	allowedMethods := "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS"
	allowedHeaders := "Origin, Content-Type, Accept, Authorization, X-CSRF-Token, X-Requested-With"

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		// Determine if origin is allowed
		originAllowed := false
		if !isProd {
			// Development: allow same-origin and localhost
			if origin == "" || strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "https://localhost:") {
				originAllowed = true
			}
		}
		if !originAllowed {
			originAllowed = config.IsAllowedOrigin(origin, allowedOrigins)
		}

		// Production: an explicit Origin header from a non-allowed origin is a CORS violation.
		// 例外：Electron 桌面客户端加载 file:// 本地文件时 origin 是 file:// 或 null，
		// 但它是受信任的桌面应用，不应被 CORS 拦截（否则 upload() 等带 Origin 的
		// 请求会 403，而其他 fetch 请求因不发 Origin 却能通过，行为不一致）。
		if isProd && origin != "" && !originAllowed && !isTrustedDesktopOrigin(origin) {
			errors.JSONError(c, errors.New(errors.CORS_ORIGIN_NOT_ALLOWED, "origin not allowed"))
			return
		}

		// 受信任的桌面客户端 origin（file:// / null）：不设置 Access-Control-Allow-Origin
		// （CORS 规范不允许 file:// 作为 Allow-Origin 值），但放行请求继续处理。
		// 其他 origin：正常设置 CORS header。
		if originAllowed && origin != "" && !isTrustedDesktopOrigin(origin) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		}

		c.Header("Access-Control-Allow-Methods", allowedMethods)
		c.Header("Access-Control-Allow-Headers", allowedHeaders)
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// isTrustedDesktopOrigin reports whether the origin belongs to a trusted
// desktop client (Electron) that loads local files via file:// protocol.
// Such clients send Origin: file:// or Origin: null, which are not valid
// CORS Allow-Origin values but should not be rejected — the desktop app
// is inherently trusted (it runs the user's own binary, not arbitrary web
// content). Without this exemption, upload() and other XHR/fetch requests
// that carry an Origin header are rejected with CORS_ORIGIN_NOT_ALLOWED,
// while requests without Origin (e.g. some fetch calls) pass — creating
// inconsistent behavior where upload fails but other APIs work.
func isTrustedDesktopOrigin(origin string) bool {
	return origin == "file://" || origin == "null"
}

// CORSMiddlewareWithConfig is a config-aware CORS middleware that uses the
// AllowedOrigins slice from the application configuration. It falls back to
// RRT_CORS_ORIGINS only when the config slice is empty.
func CORSMiddlewareWithConfig(allowedOrigins []string) gin.HandlerFunc {
	isProd := os.Getenv("RRT_ENV") == "production"
	if isProd && len(allowedOrigins) == 0 {
		origins := os.Getenv("RRT_CORS_ORIGINS")
		if origins != "" {
			allowedOrigins = strings.Split(origins, ",")
			for i := range allowedOrigins {
				allowedOrigins[i] = strings.TrimSpace(allowedOrigins[i])
			}
		}
	}

	allowedMethods := "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS"
	allowedHeaders := "Origin, Content-Type, Accept, Authorization, X-CSRF-Token, X-Requested-With"

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		originAllowed := false
		if !isProd {
			if origin == "" || strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "https://localhost:") {
				originAllowed = true
			}
		}
		if !originAllowed {
			originAllowed = config.IsAllowedOrigin(origin, allowedOrigins)
		}

		// Production: an explicit Origin header from a non-allowed origin is a CORS violation.
		// 例外：Electron 桌面客户端加载 file:// 本地文件时 origin 是 file:// 或 null，
		// 但它是受信任的桌面应用，不应被 CORS 拦截（否则 upload() 等带 Origin 的
		// 请求会 403，而其他 fetch 请求因不发 Origin 却能通过，行为不一致）。
		if isProd && origin != "" && !originAllowed && !isTrustedDesktopOrigin(origin) {
			errors.JSONError(c, errors.New(errors.CORS_ORIGIN_NOT_ALLOWED, "origin not allowed"))
			return
		}

		// 受信任的桌面客户端 origin（file:// / null）：不设置 Access-Control-Allow-Origin
		// （CORS 规范不允许 file:// 作为 Allow-Origin 值），但放行请求继续处理。
		// 其他 origin：正常设置 CORS header。
		if originAllowed && origin != "" && !isTrustedDesktopOrigin(origin) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		}

		c.Header("Access-Control-Allow-Methods", allowedMethods)
		c.Header("Access-Control-Allow-Headers", allowedHeaders)
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

