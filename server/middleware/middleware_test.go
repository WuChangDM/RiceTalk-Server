package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

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
