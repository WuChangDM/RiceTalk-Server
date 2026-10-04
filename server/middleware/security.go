package middleware

import (
	"fmt"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// SecurityHeadersMiddleware adds common security headers to every response.
func SecurityHeadersMiddleware() gin.HandlerFunc {
	isProd := os.Getenv("RRT_ENV") == "production"
	domain := os.Getenv("RRT_PUBLIC_ADDRESS")
	if domain == "" {
		domain = "localhost"
	}

	return func(c *gin.Context) {
		// L29: Override Server header to hide framework info (per security design §2.3)
		c.Header("Server", "RRT-Server")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		// X-XSS-Protection is deprecated and can introduce security vulnerabilities
		// CSP provides sufficient XSS protection
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Permissions-Policy", "camera=(), microphone=(self), speaker-selection=(self), geolocation=()")

		// HSTS only in production
		if isProd {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}

		// Dynamic CSP based on environment
		// Strip protocol prefix if present to avoid invalid URLs like ws://http://localhost
		cspDomain := strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
		// Allow LiveKit WebSocket connections (RRT_LIVEKIT_URL for internal, RRT_LIVEKIT_PUBLIC_URL for frontend)
		lkDomains := ""
		for _, envName := range []string{"RRT_LIVEKIT_URL", "RRT_LIVEKIT_PUBLIC_URL"} {
			livekitUrl := os.Getenv(envName)
			if livekitUrl == "" {
				continue
			}
			httpUrl := livekitUrl
			if strings.HasPrefix(livekitUrl, "ws://") {
				httpUrl = "http://" + strings.TrimPrefix(livekitUrl, "ws://")
			} else if strings.HasPrefix(livekitUrl, "wss://") {
				httpUrl = "https://" + strings.TrimPrefix(livekitUrl, "wss://")
			}
			lkDomains += " " + livekitUrl + " " + httpUrl
		}
		if !isProd {
			lkDomains += " ws://localhost:7880 http://localhost:7880 ws://localhost:8081 http://localhost:8081"
		}
		csp := fmt.Sprintf("default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https://p1.music.126.net https://p2.music.126.net https://p3.music.126.net https://p4.music.126.net; connect-src 'self' ws://%s wss://%s http://%s https://%s%s;", cspDomain, cspDomain, cspDomain, cspDomain, lkDomains)
		c.Header("Content-Security-Policy", csp)

		c.Next()
	}
}
