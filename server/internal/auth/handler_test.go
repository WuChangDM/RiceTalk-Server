package auth

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func TestHandlerRegister(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		handler := NewHandler(db, cfg)

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(RegisterRequest{
			Username: "testuser",
			Email:    "test@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp["code"] != "OK" {
			t.Errorf("expected code OK, got %v", resp["code"])
		}
	})

	t.Run("registration disabled", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		cfg.AllowRegister = false
		handler := NewHandler(db, cfg)

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(RegisterRequest{
			Username: "newuser",
			Email:    "new@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusForbidden, w.Code, w.Body.String())
		}
	})
}

func TestHandlerLogin(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		handler := NewHandler(db, cfg)

		// Create user directly
		passwordHash, _ := crypto.HashPassword("Password123!")
		user := &model.User{
			ID:           idgen.NextString(),
			Username:     "testuser",
			Email:        "test@example.com",
			PasswordHash: passwordHash,
			Role:         middleware.RoleMember,
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(LoginRequest{
			Username: "testuser",
			Email:    "test@example.com",
			Password: "Password123!",
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp["code"] != "OK" {
			t.Errorf("expected code OK, got %v", resp["code"])
		}
	})

	t.Run("wrong password returns 401", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		handler := NewHandler(db, cfg)

		// Create user directly
		passwordHash, _ := crypto.HashPassword("Password123!")
		user := &model.User{
			ID:           idgen.NextString(),
			Username:     "testuser",
			Email:        "test@example.com",
			PasswordHash: passwordHash,
			Role:         middleware.RoleMember,
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(LoginRequest{
			Username: "testuser",
			Email:    "test@example.com",
			Password: "WrongPassword123!",
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusUnauthorized, w.Code, w.Body.String())
		}
	})
}

func TestHandlerLogout(t *testing.T) {
	t.Run("requires authentication", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		middleware.MarkServerInitialized()
		defer middleware.ResetServerInitialized()
		handler := NewHandler(db, cfg)

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(map[string]string{
			"refreshToken": "some-token",
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/logout", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusUnauthorized, w.Code, w.Body.String())
		}
	})

	t.Run("clears auth cookies on success", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		middleware.MarkServerInitialized()
		defer middleware.ResetServerInitialized()
		handler := NewHandler(db, cfg)

		passwordHash, _ := crypto.HashPassword("Password123!")
		user := &model.User{
			ID:           idgen.NextString(),
			Username:     "logoutuser",
			Email:        "logout@example.com",
			PasswordHash: passwordHash,
			Role:         middleware.RoleMember,
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		// Login to obtain tokens
		body, _ := json.Marshal(LoginRequest{
			Username: "logoutuser",
			Email:    "logout@example.com",
			Password: "Password123!",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("login failed: status %d, body: %s", w.Code, w.Body.String())
		}
		var loginResp struct {
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &loginResp); err != nil {
			t.Fatalf("failed to decode login response: %v", err)
		}

		// Logout
		body, _ = json.Marshal(map[string]string{
			"refreshToken": loginResp.Data.RefreshToken,
		})
		w = httptest.NewRecorder()
		req, _ = http.NewRequest("POST", "/api/auth/logout", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+loginResp.Data.AccessToken)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}

		var cleared []string
		for _, c := range w.Result().Cookies() {
			if c.Name == "rrt_token" || c.Name == "rrt_refresh_token" {
				// Gin uses MaxAge=-1 to signal an expired/deleted cookie.
				if c.MaxAge > 0 {
					t.Errorf("expected cookie %s to be cleared, got MaxAge=%d", c.Name, c.MaxAge)
				}
				cleared = append(cleared, c.Name)
			}
		}
		if len(cleared) != 2 {
			t.Errorf("expected both rrt_token and rrt_refresh_token cleared, got %v", cleared)
		}
	})
}

func TestHandlerRefreshToken(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		handler := NewHandler(db, cfg)

		// Create user and generate a refresh token
		passwordHash, _ := crypto.HashPassword("Password123!")
		user := &model.User{
			ID:           idgen.NextString(),
			Username:     "testuser",
			Email:        "test@example.com",
			PasswordHash: passwordHash,
			Role:         middleware.RoleMember,
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		// Generate a valid refresh token
		_, refreshToken, err := middleware.GenerateTokenPair(user.ID, user.Username, user.Email, user.Role, user.TokenVersion, "", "", cfg)
		if err != nil {
			t.Fatalf("failed to generate token pair: %v", err)
		}

		// Store session
		session := &model.UserSession{
			ID:        idgen.NextString(),
			UserID:    user.ID,
			TokenHash: hashRefreshToken(refreshToken),
			ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
		}
		if err := db.Create(session).Error; err != nil {
			t.Fatalf("failed to create session: %v", err)
		}

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(map[string]string{
			"refreshToken": refreshToken,
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/refresh", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp["code"] != "OK" {
			t.Errorf("expected code OK, got %v", resp["code"])
		}
	})

	t.Run("reuse of refresh token returns invalid token", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		handler := NewHandler(db, cfg)

		passwordHash, _ := crypto.HashPassword("Password123!")
		user := &model.User{
			ID:           idgen.NextString(),
			Username:     "testuser",
			Email:        "test@example.com",
			PasswordHash: passwordHash,
			Role:         middleware.RoleMember,
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		_, refreshToken, err := middleware.GenerateTokenPair(user.ID, user.Username, user.Email, user.Role, user.TokenVersion, "", "", cfg)
		if err != nil {
			t.Fatalf("failed to generate token pair: %v", err)
		}

		session := &model.UserSession{
			ID:        idgen.NextString(),
			UserID:    user.ID,
			TokenHash: hashRefreshToken(refreshToken),
			ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
		}
		if err := db.Create(session).Error; err != nil {
			t.Fatalf("failed to create session: %v", err)
		}

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(map[string]string{"refreshToken": refreshToken})

		// First refresh succeeds
		w1 := httptest.NewRecorder()
		req1, _ := http.NewRequest("POST", "/api/auth/refresh", bytes.NewReader(body))
		req1.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w1, req1)
		if w1.Code != http.StatusOK {
			t.Fatalf("expected first refresh status %d, got %d, body: %s", http.StatusOK, w1.Code, w1.Body.String())
		}

		// Second refresh with the same token must fail
		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest("POST", "/api/auth/refresh", bytes.NewReader(body))
		req2.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w2, req2)
		if w2.Code != http.StatusUnauthorized {
			t.Errorf("expected second refresh status %d, got %d, body: %s", http.StatusUnauthorized, w2.Code, w2.Body.String())
		}
	})
}

func TestHIBPRegistration(t *testing.T) {
	t.Run("rejects breached password when HIBP is enabled", func(t *testing.T) {
		password := "Password123!"
		h := sha1.Sum([]byte(password))
		hash := strings.ToUpper(hex.EncodeToString(h[:]))
		prefix := hash[:5]
		suffix := hash[5:]

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/"+prefix {
				_, _ = fmt.Fprintf(w, "%s:1\n", suffix)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		cfg.JWTSecret = "test-secret-key-for-testing"
		cfg.HIBPEnabled = true
		cfg.HIBPAPIURL = server.URL + "/"
		svc := NewService(db, cfg)

		_, err := svc.Register(&RegisterRequest{
			Username: "hibpuser",
			Email:    "hibp@example.com",
			DisplayName:       "Test User",
			Password: password,
		})
		if err == nil {
			t.Fatalf("expected registration to fail for breached password")
		}
		appErr, ok := err.(*errors.AppError)
		if !ok {
			t.Fatalf("expected AppError, got %T", err)
		}
		if appErr.Code != errors.AUTH_PASSWORD_BREACHED {
			t.Errorf("expected AUTH_PASSWORD_BREACHED, got %s", appErr.Code)
		}
	})
}

func TestSecurityAuditLog(t *testing.T) {
	t.Run("registration creates a security audit log entry", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		cfg.JWTSecret = "test-secret-key-for-testing"
		handler := NewHandler(db, cfg)

		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(RegisterRequest{
			Username: "audituser",
			Email:    "audit@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/auth/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}

		var count int64
		if err := db.Model(&model.SecurityAuditLog{}).Where("action = ?", "register_success").Count(&count).Error; err != nil {
			t.Fatalf("failed to count audit logs: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1 register_success audit log, got %d", count)
		}
	})
}

func TestAdminLogin(t *testing.T) {
	t.Run("rejects non-admin user", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		middleware.MarkServerInitialized()
		defer middleware.ResetServerInitialized()

		// Create a regular member user
		passwordHash, _ := crypto.HashPassword("Password123!")
		user := &model.User{
			ID:           idgen.NextString(),
			Username:     "member",
			Email:        "member@example.com",
			PasswordHash: passwordHash,
			Role:         middleware.RoleMember,
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		handler := NewHandler(db, cfg)
		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(map[string]string{
			"username": "member",
			"email":    "member@example.com",
			"password": "Password123!",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/admin/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusForbidden, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "ADMIN_UNAUTHORIZED") {
			t.Errorf("expected ADMIN_UNAUTHORIZED, got body: %s", w.Body.String())
		}
	})

	t.Run("allows admin user login", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		middleware.MarkServerInitialized()
		defer middleware.ResetServerInitialized()

		passwordHash, _ := crypto.HashPassword("Password123!")
		user := &model.User{
			ID:           idgen.NextString(),
			Username:     "adminuser",
			Email:        "admin@example.com",
			PasswordHash: passwordHash,
			Role:         middleware.RoleAdmin,
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		handler := NewHandler(db, cfg)
		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))

		body, _ := json.Marshal(map[string]string{
			"username": "adminuser",
			"email":    "admin@example.com",
			"password": "Password123!",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/admin/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d, body: %s", http.StatusOK, w.Code, w.Body.String())
		}
	})
}
