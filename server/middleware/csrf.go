package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
)

// CSRFTokenCookieName is the name of the CSRF cookie
const CSRFTokenCookieName = "csrf_token"

// CSRFTokenHeader is the name of the CSRF token header
const CSRFTokenHeader = "X-CSRF-Token"

// generateRandomToken generates a cryptographically secure random token
func generateRandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use a shorter token (still random from os entropy)
		return ""
	}
	return hex.EncodeToString(b)
}

// CSRFProtection is a gin middleware for CSRF protection using Double Submit Cookie pattern.
//
// How it works:
//  1. On first visit (any safe method), a random token is set in a non-HttpOnly cookie.
//  2. For state-changing requests, the client must read the cookie and send the same
//     value back in the X-CSRF-Token header.
//  3. The server compares the header value with the cookie value — if they match,
//     the request is legitimate (same-origin).
//
// This is the standard Double Submit Cookie pattern. The token is NOT a secret;
// it relies on the browser's same-origin policy preventing attackers from reading
// or setting the cookie on cross-origin requests.
func CSRFProtection(cfg *config.Config) gin.HandlerFunc {
	// Skip CSRF for safe methods
	safeMethods := map[string]bool{
		http.MethodGet:     true,
		http.MethodHead:    true,
		http.MethodOptions: true,
		http.MethodTrace:   true,
	}

	// In development (HTTP), don't set Secure flag on cookies
	isSecure := cfg.Env == "production"

	return func(c *gin.Context) {
		// WebSocket upgrade requests are authenticated via JWT by the /ws handler
		// itself (see cmd/server/main.go). CSRF double-submit is redundant here
		// because WS auth is token-based, not cookie-session-based, and because
		// file://-origin desktop clients have unreliable cookie behavior during
		// the handshake. Skip CSRF entirely for WebSocket upgrades.
		if isWebSocketUpgrade(c) {
			c.Next()
			return
		}

		// For safe methods: ensure CSRF cookie exists (sets token for the session).
		if safeMethods[c.Request.Method] {
			if _, err := c.Cookie(CSRFTokenCookieName); err != nil {
				// No cookie yet — set one
				token := generateRandomToken()
				if token != "" {
					c.SetSameSite(http.SameSiteStrictMode)
					c.SetCookie(CSRFTokenCookieName, token, 86400, "/", "", isSecure, false)
				}
			}
			c.Next()
			return
		}

		// Skip CSRF for public auth endpoints (login, register, password reset).
		// These don't have a session yet and rely on rate limiting + brute-force protection.
		if strings.Contains(c.Request.URL.Path, "/auth/") {
			c.Next()
			return
		}

		// Skip CSRF for public admin login and bootstrap endpoints (server initialization).
		// Like login/register, these run before any user session exists.
		if strings.Contains(c.Request.URL.Path, "/admin/login") || strings.Contains(c.Request.URL.Path, "/admin/bootstrap") {
			c.Next()
			return
		}

		// Skip for API token-based auth (JWT Bearer) on non-WebSocket routes.
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			// JWT-based requests are stateless and don't need CSRF
			c.Next()
			return
		}

		// Get token from header
		token := c.GetHeader(CSRFTokenHeader)
		if token == "" {
			// Also check form value
			token = c.PostForm("csrf_token")
		}

		// Get token from cookie
		cookieToken, err := c.Cookie(CSRFTokenCookieName)
		if err != nil || cookieToken == "" {
			// No CSRF cookie: reject state-changing requests without Bearer auth.
			// The client should have received a cookie on a prior GET request.
			c.AbortWithStatusJSON(errors.ErrCSRFInvalid.Status, errors.FromError(c, errors.ErrCSRFInvalid))
			return
		}

		// If header is empty: this is a CSRF risk. State-changing requests
		// without Bearer auth MUST include the CSRF token header. Reject.
		if token == "" {
			c.AbortWithStatusJSON(errors.ErrCSRFInvalid.Status, errors.FromError(c, errors.ErrCSRFInvalid))
			return
		}

		if !strings.EqualFold(token, cookieToken) {
			c.AbortWithStatusJSON(errors.ErrCSRFInvalid.Status, errors.FromError(c, errors.ErrCSRFInvalid))
			return
		}

		c.Next()
	}
}

// isWebSocketUpgrade reports whether the request is a WebSocket upgrade.
func isWebSocketUpgrade(c *gin.Context) bool {
	return strings.EqualFold(c.GetHeader("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(c.GetHeader("Connection")), "upgrade")
}

// SetCSRFCookie sets the CSRF token cookie and returns the token value.
// Useful for endpoints that need to explicitly refresh/establish the token.
func SetCSRFCookie(c *gin.Context, cfg *config.Config) string {
	isSecure := cfg.Env == "production"
	token := generateRandomToken()
	if token != "" {
		c.SetSameSite(http.SameSiteStrictMode)
		c.SetCookie(CSRFTokenCookieName, token, 86400, "/", "", isSecure, false)
	}
	return token
}
