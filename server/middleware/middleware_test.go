package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func TestAuthRequired(t *testing.T) {
	t.Run("no token returns 401", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		MarkServerInitialized()
		defer ResetServerInitialized()

		router := gin.New()
		router.Use(AuthRequired(cfg, db))
		router.GET("/protected", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/protected", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
		}
	})

	t.Run("invalid token returns 401", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		MarkServerInitialized()
		defer ResetServerInitialized()

		router := gin.New()
		router.Use(AuthRequired(cfg, db))
		router.GET("/protected", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
		}
	})

	t.Run("expired token returns 401", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		cfg.JWTSecret = "test-secret"
		MarkServerInitialized()
		defer ResetServerInitialized()

		// Create an expired token
		claims := JWTClaims{
			UserID:   "user-1",
			Username: "testuser",
			Email:    "test@example.com",
			Role:     RoleMember,
			Type:     "access",
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
				IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
				Issuer:    "ridgericetalk",
			},
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenString, _ := token.SignedString([]byte(cfg.JWTSecret))

		router := gin.New()
		router.Use(AuthRequired(cfg, db))
		router.GET("/protected", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+tokenString)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
		}
	})
}

func TestRequireAdmin(t *testing.T) {
	t.Run("non-admin returns 403", func(t *testing.T) {
		gin.SetMode(gin.TestMode)

		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("role", RoleMember)
			c.Next()
		})
		router.Use(RequireAdmin())
		router.GET("/admin", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/admin", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, w.Code)
		}
	})

	t.Run("admin is allowed", func(t *testing.T) {
		gin.SetMode(gin.TestMode)

		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("role", RoleAdmin)
			c.Next()
		})
		router.Use(RequireAdmin())
		router.GET("/admin", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/admin", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("owner is allowed", func(t *testing.T) {
		gin.SetMode(gin.TestMode)

		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("role", RoleOwner)
			c.Next()
		})
		router.Use(RequireAdmin())
		router.GET("/admin", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/admin", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})
}

func TestChannelPermissionRejectsLegacyDMForEveryRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: "space-legacy-dm", Name: "Legacy DM", OwnerID: "owner-legacy-dm"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	dm := &model.Channel{ID: "channel-legacy-dm", SpaceID: space.ID, Name: "legacy-dm", Type: "DM", Visibility: "public"}
	if err := db.Create(dm).Error; err != nil {
		t.Fatalf("create legacy DM channel: %v", err)
	}

	for _, role := range []string{RoleOwner, RoleAdmin, RoleMember} {
		t.Run(role, func(t *testing.T) {
			called := false
			router := gin.New()
			router.GET("/channels/:id", func(c *gin.Context) {
				c.Set("user_id", "user-legacy-dm")
				c.Set("role", role)
				c.Next()
			}, ChannelPermission(db), func(c *gin.Context) {
				called = true
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/channels/"+dm.ID, nil)
			router.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("expected 404 for %s, got %d: %s", role, w.Code, w.Body.String())
			}
			if called {
				t.Fatal("legacy DM reached the protected handler")
			}
			if !strings.Contains(w.Body.String(), "CHANNEL_NOT_FOUND") {
				t.Fatalf("expected CHANNEL_NOT_FOUND, got %s", w.Body.String())
			}
		})
	}
}

func TestCSRFProtection(t *testing.T) {
	t.Run("no CSRF token returns 403 for state-changing request", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(CSRFProtection(cfg))
		router.POST("/action", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/action", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, w.Code)
		}
	})

	t.Run("GET request sets CSRF cookie", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(CSRFProtection(cfg))
		router.GET("/page", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/page", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}

		// Check that a CSRF cookie was set
		cookies := w.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, c := range cookies {
			if c.Name == CSRFTokenCookieName {
				csrfCookie = c
				break
			}
		}
		if csrfCookie == nil {
			t.Error("expected CSRF cookie to be set")
		}
	})

	t.Run("valid CSRF token allows state-changing request", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(CSRFProtection(cfg))
		router.POST("/action", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		// First, get the CSRF cookie via GET
		w1 := httptest.NewRecorder()
		req1, _ := http.NewRequest("GET", "/page", nil)
		router.ServeHTTP(w1, req1)

		cookies := w1.Result().Cookies()
		var csrfToken string
		for _, c := range cookies {
			if c.Name == CSRFTokenCookieName {
				csrfToken = c.Value
				break
			}
		}
		if csrfToken == "" {
			t.Fatal("expected CSRF cookie to be set in first request")
		}

		// Now make a POST with the CSRF token in header
		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest("POST", "/action", nil)
		req2.Header.Set("X-CSRF-Token", csrfToken)
		// Add the cookie to the request
		req2.AddCookie(&http.Cookie{Name: CSRFTokenCookieName, Value: csrfToken})
		router.ServeHTTP(w2, req2)

		if w2.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w2.Code)
		}
	})

	t.Run("POST with cookie but no header returns 403", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(CSRFProtection(cfg))
		router.POST("/action", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w1 := httptest.NewRecorder()
		req1, _ := http.NewRequest("GET", "/page", nil)
		router.ServeHTTP(w1, req1)

		cookies := w1.Result().Cookies()
		var csrfToken string
		for _, c := range cookies {
			if c.Name == CSRFTokenCookieName {
				csrfToken = c.Value
				break
			}
		}
		if csrfToken == "" {
			t.Fatal("expected CSRF cookie to be set in first request")
		}

		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest("POST", "/action", nil)
		// Only send the cookie back, not the header — this must be rejected.
		req2.AddCookie(&http.Cookie{Name: CSRFTokenCookieName, Value: csrfToken})
		router.ServeHTTP(w2, req2)

		if w2.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, w2.Code)
		}
	})

	t.Run("skips CSRF for admin bootstrap endpoints", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(CSRFProtection(cfg))
		router.POST("/admin/bootstrap/register", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/admin/bootstrap/register", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("allows WebSocket upgrade without CSRF token", func(t *testing.T) {
		// ISSUE-006: WebSocket upgrades are exempt from CSRF validation because
		// the WebSocket handshake cannot carry custom headers in the browser.
		gin.SetMode(gin.TestMode)
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(CSRFProtection(cfg))
		router.GET("/ws", func(c *gin.Context) {
			c.String(http.StatusOK, "upgraded")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/ws", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("allows WebSocket upgrade with valid CSRF token", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(CSRFProtection(cfg))
		router.GET("/ws", func(c *gin.Context) {
			c.String(http.StatusOK, "upgraded")
		})

		// First, get the CSRF cookie via GET
		w1 := httptest.NewRecorder()
		req1, _ := http.NewRequest("GET", "/page", nil)
		router.ServeHTTP(w1, req1)

		cookies := w1.Result().Cookies()
		var csrfToken string
		for _, c := range cookies {
			if c.Name == CSRFTokenCookieName {
				csrfToken = c.Value
				break
			}
		}
		if csrfToken == "" {
			t.Fatal("expected CSRF cookie to be set in first request")
		}

		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest("GET", "/ws", nil)
		req2.Header.Set("Upgrade", "websocket")
		req2.Header.Set("Connection", "Upgrade")
		req2.Header.Set("X-CSRF-Token", csrfToken)
		req2.AddCookie(&http.Cookie{Name: CSRFTokenCookieName, Value: csrfToken})
		router.ServeHTTP(w2, req2)

		if w2.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w2.Code)
		}
	})

	t.Run("allows WebSocket upgrade with CSRF token in query", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(CSRFProtection(cfg))
		router.GET("/ws", func(c *gin.Context) {
			c.String(http.StatusOK, "upgraded")
		})

		// First, get the CSRF cookie via GET
		w1 := httptest.NewRecorder()
		req1, _ := http.NewRequest("GET", "/page", nil)
		router.ServeHTTP(w1, req1)

		cookies := w1.Result().Cookies()
		var csrfToken string
		for _, c := range cookies {
			if c.Name == CSRFTokenCookieName {
				csrfToken = c.Value
				break
			}
		}
		if csrfToken == "" {
			t.Fatal("expected CSRF cookie to be set in first request")
		}

		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest("GET", "/ws?csrf_token="+csrfToken, nil)
		req2.Header.Set("Upgrade", "websocket")
		req2.Header.Set("Connection", "Upgrade")
		req2.AddCookie(&http.Cookie{Name: CSRFTokenCookieName, Value: csrfToken})
		router.ServeHTTP(w2, req2)

		if w2.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w2.Code)
		}
	})
}

func TestBruteForceProtector(t *testing.T) {
	t.Run("records failures and locks", func(t *testing.T) {
		bfp := NewBruteForceProtector()

		// Should allow initially
		allowed, _ := bfp.AllowAccount("user@example.com")
		if !allowed {
			t.Error("expected account to be allowed initially")
		}

		// Record 5 failures
		for i := 0; i < 5; i++ {
			bfp.RecordFailure("user@example.com", "192.168.1.1")
		}

		// Should now be locked
		allowed, remaining := bfp.AllowAccount("user@example.com")
		if allowed {
			t.Error("expected account to be locked after 5 failures")
		}
		if remaining <= 0 {
			t.Error("expected positive lock duration")
		}
	})

	t.Run("IP-level locking", func(t *testing.T) {
		bfp := NewBruteForceProtector()

		// Should allow initially
		allowed, _ := bfp.AllowIP("192.168.1.1")
		if !allowed {
			t.Error("expected IP to be allowed initially")
		}

		// Record 20 failures
		for i := 0; i < 20; i++ {
			bfp.RecordFailure("user"+string(rune(i))+"@example.com", "192.168.1.1")
		}

		// Should now be locked
		allowed, remaining := bfp.AllowIP("192.168.1.1")
		if allowed {
			t.Error("expected IP to be locked after 20 failures")
		}
		if remaining <= 0 {
			t.Error("expected positive lock duration")
		}
	})

	t.Run("reset account clears failures", func(t *testing.T) {
		bfp := NewBruteForceProtector()

		// Record some failures
		for i := 0; i < 3; i++ {
			bfp.RecordFailure("user@example.com", "192.168.1.1")
		}

		// Reset
		bfp.ResetAccount("user@example.com")

		// Should be allowed again
		allowed, _ := bfp.AllowAccount("user@example.com")
		if !allowed {
			t.Error("expected account to be allowed after reset")
		}
	})

	t.Run("account locked after 5th failure", func(t *testing.T) {
		bfp := NewBruteForceProtector()

		// 1st to 4th should not lock
		for i := 0; i < 4; i++ {
			bfp.RecordFailure("user@example.com", "192.168.1.1")
		}
		allowed, _ := bfp.AllowAccount("user@example.com")
		if !allowed {
			t.Error("expected account to still be allowed after 4th failure")
		}

		// 5th should lock
		bfp.RecordFailure("user@example.com", "192.168.1.1")
		allowed, _ = bfp.AllowAccount("user@example.com")
		if allowed {
			t.Error("expected account to be locked after 5th failure")
		}
	})
}

func TestGenerateTokenPair(t *testing.T) {
	t.Run("generates valid access and refresh tokens", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.JWTSecret = "test-secret-key-for-testing"

		accessToken, refreshToken, err := GenerateTokenPair("user-123", "testuser", "test@example.com", RoleMember, 1, "space-1", "session-1", cfg)
		if err != nil {
			t.Fatalf("GenerateTokenPair() error = %v", err)
		}
		if accessToken == "" {
			t.Error("access token should not be empty")
		}
		if refreshToken == "" {
			t.Error("refresh token should not be empty")
		}
		if accessToken == refreshToken {
			t.Error("access and refresh tokens should be different")
		}
	})

	t.Run("access token has correct claims", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.JWTSecret = "test-secret-key-for-testing"

		accessToken, _, err := GenerateTokenPair("user-123", "testuser", "test@example.com", RoleOwner, 1, "space-1", "session-1", cfg)
		if err != nil {
			t.Fatalf("GenerateTokenPair() error = %v", err)
		}

		claims, err := ParseToken(accessToken, cfg)
		if err != nil {
			t.Fatalf("ParseToken() error = %v", err)
		}
		if claims.UserID != "user-123" {
			t.Errorf("UserID = %v, want user-123", claims.UserID)
		}
		if claims.Username != "testuser" {
			t.Errorf("Username = %v, want testuser", claims.Username)
		}
		if claims.Email != "test@example.com" {
			t.Errorf("Email = %v, want test@example.com", claims.Email)
		}
		if claims.Role != RoleOwner {
			t.Errorf("Role = %v, want %v", claims.Role, RoleOwner)
		}
		if claims.Type != "access" {
			t.Errorf("Type = %v, want access", claims.Type)
		}
		if claims.TokenVersion != 1 {
			t.Errorf("TokenVersion = %v, want 1", claims.TokenVersion)
		}
	})

	t.Run("refresh token has correct type", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.JWTSecret = "test-secret-key-for-testing"

		_, refreshToken, err := GenerateTokenPair("user-123", "testuser", "test@example.com", RoleMember, 1, "space-1", "session-1", cfg)
		if err != nil {
			t.Fatalf("GenerateTokenPair() error = %v", err)
		}

		claims, err := ParseToken(refreshToken, cfg)
		if err != nil {
			t.Fatalf("ParseToken() error = %v", err)
		}
		if claims.Type != "refresh" {
			t.Errorf("Type = %v, want refresh", claims.Type)
		}
		if claims.Username != "" {
			t.Error("refresh token should not contain username")
		}
	})
}

func TestParseToken(t *testing.T) {
	t.Run("valid token", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.JWTSecret = "test-secret-key-for-testing"

		accessToken, _, err := GenerateTokenPair("user-123", "testuser", "test@example.com", RoleMember, 1, "space-1", "session-1", cfg)
		if err != nil {
			t.Fatalf("GenerateTokenPair() error = %v", err)
		}

		claims, err := ParseToken(accessToken, cfg)
		if err != nil {
			t.Fatalf("ParseToken() error = %v", err)
		}
		if claims.UserID != "user-123" {
			t.Errorf("UserID = %v, want user-123", claims.UserID)
		}
	})

	t.Run("token with wrong secret", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.JWTSecret = "test-secret-key-for-testing"

		accessToken, _, err := GenerateTokenPair("user-123", "testuser", "test@example.com", RoleMember, 1, "space-1", "session-1", cfg)
		if err != nil {
			t.Fatalf("GenerateTokenPair() error = %v", err)
		}

		cfg2 := config.DefaultConfig()
		cfg2.JWTSecret = "different-secret-key"
		_, err = ParseToken(accessToken, cfg2)
		if err == nil {
			t.Fatal("expected error for token with wrong secret, got nil")
		}
	})
}

func TestCORSMiddleware(t *testing.T) {
	t.Run("development allows localhost origin", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		t.Setenv("RRT_ENV", "development")

		router := gin.New()
		router.Use(CORSMiddleware())
		router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
		if h := w.Header().Get("Access-Control-Allow-Origin"); h != "http://localhost:3000" {
			t.Errorf("expected Access-Control-Allow-Origin header, got %q", h)
		}
	})

	t.Run("production rejects unknown origin", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		t.Setenv("RRT_ENV", "production")

		router := gin.New()
		router.Use(CORSMiddleware())
		router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://evil.com")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, w.Code)
		}
	})

	t.Run("production allows configured origin", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		t.Setenv("RRT_ENV", "production")
		t.Setenv("RRT_CORS_ORIGINS", "https://app.example.com")

		router := gin.New()
		router.Use(CORSMiddleware())
		router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://app.example.com")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
		if h := w.Header().Get("Access-Control-Allow-Origin"); h != "https://app.example.com" {
			t.Errorf("expected allowed origin header, got %q", h)
		}
	})

	t.Run("config-aware variant uses allowed origins slice", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		t.Setenv("RRT_ENV", "production")
		t.Setenv("RRT_CORS_ORIGINS", "")

		router := gin.New()
		router.Use(CORSMiddlewareWithConfig([]string{"https://trusted.example.com"}))
		router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://trusted.example.com")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})
}

func TestOptionalAuth(t *testing.T) {
	t.Run("no auth header proceeds", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(OptionalAuth(cfg, db))
		router.GET("/public", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/public", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("malformed auth header proceeds", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()

		router := gin.New()
		router.Use(OptionalAuth(cfg, db))
		router.GET("/public", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/public", nil)
		req.Header.Set("Authorization", "NotBearer token")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("valid token sets user context", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		cfg.JWTSecret = "test-secret-key-for-testing"

		// Create a user in the test DB so TokenVersion validation passes
		db.Exec("INSERT INTO users (id, username, email, password_hash, role, token_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))",
			"user-123", "testuser", "test@example.com", "hash", RoleMember, 0)

		accessToken, _, _ := GenerateTokenPair("user-123", "testuser", "test@example.com", RoleMember, 0, "space-1", "session-1", cfg)

		router := gin.New()
		router.Use(OptionalAuth(cfg, db))
		router.GET("/public", func(c *gin.Context) {
			uid, _ := c.Get("user_id")
			if uid != "user-123" {
				t.Errorf("expected user_id user-123, got %v", uid)
			}
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/public", nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})
}

// TestBotTokenRequired 批次9c M22新增：验证 BotToken 中间件的可选验证模式
func TestBotTokenRequired(t *testing.T) {
	t.Run("no bot token falls back to user auth (backward compatible)", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()

		router := gin.New()
		router.Use(BotTokenRequired(db))
		router.GET("/bots", func(c *gin.Context) {
			// Should reach here without bot_id being set
			_, hasBotID := c.Get("bot_id")
			if hasBotID {
				t.Error("expected bot_id to NOT be set when no X-Bot-Token header")
			}
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/bots", nil)
		// No X-Bot-Token header — should fall back to user auth
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d (backward compatible), got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("valid bot token sets bot_id and bot_space_id in context", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()

		// Create a bot record in the database
		spaceID := "space-1"
		bot := model.Bot{
			ID:        "bot-1",
			SpaceID:   spaceID,
			Name:      "MusicBot",
			Token:     "valid-bot-token-12345",
			CreatedBy: "admin-1",
		}
		if err := db.Create(&bot).Error; err != nil {
			t.Fatalf("failed to create bot: %v", err)
		}

		router := gin.New()
		router.Use(BotTokenRequired(db))
		router.GET("/bots", func(c *gin.Context) {
			botID, _ := c.Get("bot_id")
			botSpaceID, _ := c.Get("bot_space_id")
			if botID != "bot-1" {
				t.Errorf("expected bot_id 'bot-1', got %v", botID)
			}
			if botSpaceID != spaceID {
				t.Errorf("expected bot_space_id '%s', got %v", spaceID, botSpaceID)
			}
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/bots", nil)
		req.Header.Set("X-Bot-Token", "valid-bot-token-12345")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}
	})

	t.Run("invalid bot token returns 401 with BOT_TOKEN_INVALID", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()

		router := gin.New()
		router.Use(BotTokenRequired(db))
		router.GET("/bots", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/bots", nil)
		req.Header.Set("X-Bot-Token", "invalid-token")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
		}
	})
}

// TestCSRFProtectionAdminLoginSkip verifies that the public admin login endpoint
// is exempt from CSRF, matching the admin bootstrap endpoints.
func TestCSRFProtectionAdminLoginSkip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.DefaultConfig()

	router := gin.New()
	router.Use(CSRFProtection(cfg))
	router.POST("/admin/login", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/admin/login", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestParseTokenAlgorithmConfusion verifies that ParseToken rejects tokens
// signed with algorithms other than HS256 (e.g. none, HS512).
func TestParseTokenAlgorithmConfusion(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-key-for-testing"

	claims := JWTClaims{
		UserID:   "user-123",
		Username: "testuser",
		Email:    "test@example.com",
		Role:     RoleMember,
		Type:     "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "ridgericetalk",
		},
	}

	t.Run("rejects alg=none token", func(t *testing.T) {
		token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
		tokenString, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("failed to sign none token: %v", err)
		}
		if _, err := ParseToken(tokenString, cfg); err == nil {
			t.Error("expected alg=none token to be rejected")
		}
	})

	t.Run("rejects HS512 token", func(t *testing.T) {
		token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
		tokenString, err := token.SignedString([]byte(cfg.JWTSecret))
		if err != nil {
			t.Fatalf("failed to sign HS512 token: %v", err)
		}
		if _, err := ParseToken(tokenString, cfg); err == nil {
			t.Error("expected HS512 token to be rejected")
		}
	})
}

// TestChannelPermissionEquivalence 是 rbac 重构（CheckChannelAccess/AuthorizeChannel
// 抽取、ChannelPermission 改薄封装）的逐分支等价性锁定：中间件路径与直调路径都必须
// 与重构前口径一致（dm 先于 OWNER、public/admin-only/role-specific、频道不存在、空
// channelID、DB 故障）。
func TestChannelPermissionEquivalence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()

	space := &model.Space{ID: "space-rbac", Name: "RBAC", OwnerID: "owner-rbac"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	channels := []model.Channel{
		{ID: "ch-rbac-public", SpaceID: space.ID, Name: "pub", Type: "text", Visibility: "public"},
		{ID: "ch-rbac-admin", SpaceID: space.ID, Name: "admin", Type: "text", Visibility: "admin-only"},
		{ID: "ch-rbac-role-none", SpaceID: space.ID, Name: "role-none", Type: "text", Visibility: "role-specific"},
		{ID: "ch-rbac-role-deny", SpaceID: space.ID, Name: "role-deny", Type: "text", Visibility: "role-specific"},
		{ID: "ch-rbac-role-allow", SpaceID: space.ID, Name: "role-allow", Type: "text", Visibility: "role-specific"},
		{ID: "ch-rbac-dm", SpaceID: space.ID, Name: "dm", Type: "DM", Visibility: "public"},
	}
	for i := range channels {
		if err := db.Create(&channels[i]).Error; err != nil {
			t.Fatalf("create channel %s: %v", channels[i].ID, err)
		}
	}
	// 注意：CanView 带 gorm `default:true`，Create 传零值(false) 会被默认值覆盖，
	// 故先建行再显式 Update 成 false（模拟「显式拒绝」权限行）。
	perms := []model.ChannelRolePermission{
		{ID: idgen.NextString(), ChannelID: "ch-rbac-role-deny", Role: RoleMember, CanView: true},
		{ID: idgen.NextString(), ChannelID: "ch-rbac-role-allow", Role: RoleMember, CanView: true, CanWrite: true},
	}
	for i := range perms {
		if err := db.Create(&perms[i]).Error; err != nil {
			t.Fatalf("create channel role permission: %v", err)
		}
	}
	if err := db.Model(&model.ChannelRolePermission{}).
		Where("channel_id = ? AND role = ?", "ch-rbac-role-deny", RoleMember).
		Update("can_view", false).Error; err != nil {
		t.Fatalf("force can_view=false: %v", err)
	}

	run := func(t *testing.T, channelID, role string) (status int, body string, called bool) {
		t.Helper()
		router := gin.New()
		router.GET("/channels/:id", func(c *gin.Context) {
			c.Set("user_id", "user-rbac")
			c.Set("role", role)
			c.Next()
		}, ChannelPermission(db), func(c *gin.Context) {
			called = true
			c.Status(http.StatusOK)
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/channels/"+channelID, nil)
		router.ServeHTTP(w, req)
		return w.Code, w.Body.String(), called
	}

	cases := []struct {
		name       string
		channelID  string
		role       string
		wantStatus int
		wantCode   string // 空串 = 期望放行（handler 被调用）
	}{
		{"dm 任意角色(含 OWNER) 404", "ch-rbac-dm", RoleOwner, http.StatusNotFound, "CHANNEL_NOT_FOUND"},
		{"dm MEMBER 404", "ch-rbac-dm", RoleMember, http.StatusNotFound, "CHANNEL_NOT_FOUND"},
		{"public MEMBER 放行", "ch-rbac-public", RoleMember, http.StatusOK, ""},
		{"admin-only MEMBER 403", "ch-rbac-admin", RoleMember, http.StatusForbidden, "CHANNEL_ACCESS_DENIED"},
		{"admin-only ADMIN 放行", "ch-rbac-admin", RoleAdmin, http.StatusOK, ""},
		{"role-specific 无权限行 403", "ch-rbac-role-none", RoleMember, http.StatusForbidden, "CHANNEL_ACCESS_DENIED"},
		{"role-specific can_view=false 403", "ch-rbac-role-deny", RoleMember, http.StatusForbidden, "CHANNEL_ACCESS_DENIED"},
		{"role-specific can_view=true 放行", "ch-rbac-role-allow", RoleMember, http.StatusOK, ""},
		{"频道不存在 404", "ch-rbac-missing", RoleMember, http.StatusNotFound, "CHANNEL_NOT_FOUND"},
		{"OWNER 对 admin-only 放行", "ch-rbac-admin", RoleOwner, http.StatusOK, ""},
		{"OWNER 对 role-specific(无行) 放行", "ch-rbac-role-none", RoleOwner, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body, called := run(t, tc.channelID, tc.role)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", status, tc.wantStatus, body)
			}
			if tc.wantCode == "" {
				if !called {
					t.Fatalf("expected protected handler to be reached, body: %s", body)
				}
				return
			}
			if called {
				t.Fatalf("protected handler must not be reached on denial")
			}
			if !strings.Contains(body, tc.wantCode) {
				t.Fatalf("expected code %s in body, got %s", tc.wantCode, body)
			}
		})
	}
}

// TestAuthorizeChannelDirect 覆盖薄封装的直调路径（handler 侧复用入口）：
// 返回值是三态 AppError，且 CheckChannelAccess 空频道不得 panic。
func TestAuthorizeChannelDirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: "space-direct", Name: "Direct", OwnerID: "owner-direct"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	pub := &model.Channel{ID: "ch-direct-pub", SpaceID: space.ID, Name: "pub", Type: "text", Visibility: "public"}
	admin := &model.Channel{ID: "ch-direct-admin", SpaceID: space.ID, Name: "admin", Type: "text", Visibility: "admin-only"}
	dm := &model.Channel{ID: "ch-direct-dm", SpaceID: space.ID, Name: "dm", Type: "DM", Visibility: "public"}
	for _, ch := range []*model.Channel{pub, admin, dm} {
		if err := db.Create(ch).Error; err != nil {
			t.Fatalf("create channel %s: %v", ch.ID, err)
		}
	}

	newCtx := func(role string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("user_id", "user-direct")
		c.Set("role", role)
		return c
	}

	if err := AuthorizeChannel(newCtx(RoleMember), db, pub.ID); err != nil {
		t.Fatalf("public should pass, got %v", err)
	}
	if err := AuthorizeChannel(newCtx(RoleMember), db, admin.ID); err == nil {
		t.Fatal("admin-only + MEMBER should be denied")
	} else if err.Code != "CHANNEL_ACCESS_DENIED" || err.Status != http.StatusForbidden {
		t.Fatalf("admin-only denial = code %s status %d", err.Code, err.Status)
	}
	if err := AuthorizeChannel(newCtx(RoleOwner), db, dm.ID); err == nil {
		t.Fatal("dm must be 404 even for OWNER")
	} else if err.Code != "CHANNEL_NOT_FOUND" || err.Status != http.StatusNotFound {
		t.Fatalf("dm denial = code %s status %d", err.Code, err.Status)
	}
	if err := AuthorizeChannel(newCtx(RoleMember), db, "ch-direct-missing"); err == nil {
		t.Fatal("missing channel should be 404")
	} else if err.Code != "CHANNEL_NOT_FOUND" || err.Status != http.StatusNotFound {
		t.Fatalf("missing channel = code %s status %d", err.Code, err.Status)
	}
	if err := AuthorizeChannel(newCtx(RoleMember), db, ""); err == nil {
		t.Fatal("empty channel id should be 400")
	} else if err.Code != "SYSTEM_BAD_REQUEST" || err.Status != http.StatusBadRequest {
		t.Fatalf("empty channel = code %s status %d", err.Code, err.Status)
	}
	if err := CheckChannelAccess(newCtx(RoleOwner), nil, nil); err == nil {
		t.Fatal("nil channel should be 404, not panic")
	} else if err.Code != "CHANNEL_NOT_FOUND" {
		t.Fatalf("nil channel = code %s", err.Code)
	}
}

// TestAuthorizeChannelDBFailure 覆盖 DB 故障分支：必须回 500 SYSTEM_INTERNAL_ERROR
// （重构前 ChannelPermission 对非 ErrRecordNotFound 的查询错误即 500，语义不得漂移）。
func TestAuthorizeChannelDBFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close sql.DB: %v", err)
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("role", RoleMember)
	appErr := AuthorizeChannel(c, db, "ch-any")
	if appErr == nil {
		t.Fatal("closed DB should surface an internal error")
	}
	if appErr.Status != http.StatusInternalServerError || appErr.Code != "SYSTEM_INTERNAL_ERROR" {
		t.Fatalf("closed DB = code %s status %d, want SYSTEM_INTERNAL_ERROR/500", appErr.Code, appErr.Status)
	}
}

// TestChannelPermissionUnknownVisibility 锁定 CheckChannelAccess 的 default 分支：
// 未知 visibility 必须按「默认拒绝」回 403 CHANNEL_ACCESS_DENIED（中间件路径 + 直调路径）。
func TestChannelPermissionUnknownVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: "space-unknown-vis", Name: "UnknownVis", OwnerID: "owner-unknown-vis"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	odd := &model.Channel{ID: "ch-unknown-vis", SpaceID: space.ID, Name: "odd", Type: "text", Visibility: "members-only"}
	if err := db.Create(odd).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	t.Run("middleware MEMBER 403", func(t *testing.T) {
		called := false
		router := gin.New()
		router.GET("/channels/:id", func(c *gin.Context) {
			c.Set("user_id", "user-unknown-vis")
			c.Set("role", RoleMember)
		}, ChannelPermission(db), func(c *gin.Context) {
			called = true
			c.Status(http.StatusOK)
		})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/channels/"+odd.ID, nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body: %s)", w.Code, w.Body.String())
		}
		if called {
			t.Fatal("protected handler must not be reached for unknown visibility")
		}
		if !strings.Contains(w.Body.String(), "CHANNEL_ACCESS_DENIED") {
			t.Fatalf("expected CHANNEL_ACCESS_DENIED in body, got %s", w.Body.String())
		}
	})

	t.Run("direct MEMBER 403", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("user_id", "user-unknown-vis")
		c.Set("role", RoleMember)
		appErr := AuthorizeChannel(c, db, odd.ID)
		if appErr == nil {
			t.Fatal("unknown visibility should be denied")
		}
		if appErr.Status != http.StatusForbidden || appErr.Code != "CHANNEL_ACCESS_DENIED" {
			t.Fatalf("unknown visibility = code %s status %d, want CHANNEL_ACCESS_DENIED/403", appErr.Code, appErr.Status)
		}
	})

	t.Run("OWNER 放行", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("role", RoleOwner)
		if appErr := AuthorizeChannel(c, db, odd.ID); appErr != nil {
			t.Fatalf("OWNER should pass unknown visibility, got %v", appErr)
		}
	})
}

// TestChannelPermissionEmptyChannelIDMiddleware 锁定中间件路径的空 :id：
// 必须回 400 SYSTEM_BAD_REQUEST（此前仅直调路径有覆盖）。
func TestChannelPermissionEmptyChannelIDMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/channels/", nil)
	c.Params = gin.Params{{Key: "id", Value: ""}}
	c.Set("user_id", "user-empty-id")
	c.Set("role", RoleMember)

	ChannelPermission(db)(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SYSTEM_BAD_REQUEST") {
		t.Fatalf("expected SYSTEM_BAD_REQUEST in body, got %s", w.Body.String())
	}
}

// TestChannelPermissionDBFailureMiddleware 锁定中间件路径的真 DB 故障分支：
// 关闭底层连接（非 mock）后必须回 500 SYSTEM_INTERNAL_ERROR，且不进入受保护 handler、不 panic。
func TestChannelPermissionDBFailureMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close sql.DB: %v", err)
	}

	called := false
	router := gin.New()
	router.GET("/channels/:id", func(c *gin.Context) {
		c.Set("user_id", "user-db-fail")
		c.Set("role", RoleMember)
	}, ChannelPermission(db), func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/channels/ch-db-fail", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("protected handler must not be reached on DB failure")
	}
	if !strings.Contains(w.Body.String(), "SYSTEM_INTERNAL_ERROR") {
		t.Fatalf("expected SYSTEM_INTERNAL_ERROR in body, got %s", w.Body.String())
	}
}

// TestAuthorizeChannelOwnerShortCircuit 锁定 AuthorizeChannel 的 OWNER 短路：
// OWNER 先于可见性/权限行判定放行；dm 与频道不存在仍 404，DB 故障 fail-closed 500。
func TestAuthorizeChannelOwnerShortCircuit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ownerCtx := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("user_id", "owner-short")
		c.Set("role", RoleOwner)
		return c
	}
	assertAppErr := func(t *testing.T, appErr *errors.AppError, wantCode string, wantStatus int) {
		t.Helper()
		if appErr == nil {
			t.Fatalf("expected error %s, got nil", wantCode)
		}
		if string(appErr.Code) != wantCode || appErr.Status != wantStatus {
			t.Fatalf("code %s status %d, want %s/%d", appErr.Code, appErr.Status, wantCode, wantStatus)
		}
	}

	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: "space-owner-short", Name: "OwnerShort", OwnerID: "owner-short"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	channels := []model.Channel{
		{ID: "ch-owner-role", SpaceID: space.ID, Name: "role", Type: "text", Visibility: "role-specific"},
		{ID: "ch-owner-odd", SpaceID: space.ID, Name: "odd", Type: "text", Visibility: "members-only"},
		{ID: "ch-owner-dm", SpaceID: space.ID, Name: "dm", Type: "DM", Visibility: "public"},
		{ID: "ch-owner-admin", SpaceID: space.ID, Name: "admin", Type: "text", Visibility: "admin-only"},
	}
	for i := range channels {
		if err := db.Create(&channels[i]).Error; err != nil {
			t.Fatalf("create channel %s: %v", channels[i].ID, err)
		}
	}

	t.Run("role-specific 无权限行放行", func(t *testing.T) {
		if appErr := AuthorizeChannel(ownerCtx(), db, "ch-owner-role"); appErr != nil {
			t.Fatalf("OWNER should pass role-specific, got %v", appErr)
		}
	})
	t.Run("admin-only 放行", func(t *testing.T) {
		if appErr := AuthorizeChannel(ownerCtx(), db, "ch-owner-admin"); appErr != nil {
			t.Fatalf("OWNER should pass admin-only, got %v", appErr)
		}
	})
	t.Run("未知 visibility 放行", func(t *testing.T) {
		if appErr := AuthorizeChannel(ownerCtx(), db, "ch-owner-odd"); appErr != nil {
			t.Fatalf("OWNER should pass unknown visibility, got %v", appErr)
		}
	})
	t.Run("dm 仍 404", func(t *testing.T) {
		assertAppErr(t, AuthorizeChannel(ownerCtx(), db, "ch-owner-dm"), "CHANNEL_NOT_FOUND", http.StatusNotFound)
	})
	t.Run("频道不存在仍 404", func(t *testing.T) {
		assertAppErr(t, AuthorizeChannel(ownerCtx(), db, "ch-owner-missing"), "CHANNEL_NOT_FOUND", http.StatusNotFound)
	})
	t.Run("不查 role-specific 权限行", func(t *testing.T) {
		// 删掉权限表：OWNER 若仍查权限行会 500，放行即证明短路发生在查询之前。
		freshDB := testutil.MustSetupTestDB()
		if err := freshDB.Create(&model.Channel{ID: "ch-owner-no-perm-table", SpaceID: space.ID, Name: "role2", Type: "text", Visibility: "role-specific"}).Error; err != nil {
			t.Fatalf("create channel: %v", err)
		}
		if err := freshDB.Migrator().DropTable(&model.ChannelRolePermission{}); err != nil {
			t.Fatalf("drop channel_role_permissions: %v", err)
		}
		if appErr := AuthorizeChannel(ownerCtx(), freshDB, "ch-owner-no-perm-table"); appErr != nil {
			t.Fatalf("OWNER must not query role-specific rows, got %v", appErr)
		}
	})
	t.Run("DB 故障 fail-closed 500", func(t *testing.T) {
		freshDB := testutil.MustSetupTestDB()
		sqlDB, err := freshDB.DB()
		if err != nil {
			t.Fatalf("get sql.DB: %v", err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("close sql.DB: %v", err)
		}
		// D-4：OWNER 也不再 fail-open——故障时后续业务查询同样失败，放行没有收益。
		assertAppErr(t, AuthorizeChannel(ownerCtx(), freshDB, "ch-any"), "SYSTEM_INTERNAL_ERROR", http.StatusInternalServerError)
	})
}

// TestAuthorizeChannelOwnerDBFailureMiddleware 锁定 D-4：OWNER + 真 DB 故障时，
// 中间件路径（ChannelPermission）同样 fail-closed 返回 500，且受保护 handler 不被调用。
func TestAuthorizeChannelOwnerDBFailureMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close sql.DB: %v", err)
	}

	called := false
	router := gin.New()
	router.GET("/channels/:id", func(c *gin.Context) {
		c.Set("user_id", "owner-db-fail")
		c.Set("role", RoleOwner)
	}, ChannelPermission(db), func(c *gin.Context) {
		called = true
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/channels/ch-owner-db-fail", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("protected handler must not be reached on OWNER + DB failure")
	}
	if !strings.Contains(w.Body.String(), "SYSTEM_INTERNAL_ERROR") {
		t.Fatalf("expected SYSTEM_INTERNAL_ERROR in body, got %s", w.Body.String())
	}
}
