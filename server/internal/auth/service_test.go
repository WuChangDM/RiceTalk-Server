package auth

import (
	"strings"
	"testing"
	"time"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

// testSecurityQuestions returns valid security question answers for tests.
func testSecurityQuestions() []SecurityQuestionItem {
	return []SecurityQuestionItem{
		{Question: "您父亲的生日是哪一天？", Answer: "1970-01-01"},
		{Question: "您出生城市是哪里？", Answer: "上海"},
	}
}

// testConfig returns a config suitable for testing: HIBP is disabled to avoid
// network dependencies, and PasswordHistoryCount is kept at the default.
func testConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.HIBPEnabled = false
	cfg.JWTSecret = "test-secret"
	return cfg
}

func TestRegister(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		resp, err := svc.Register(&RegisterRequest{
			Username: "testuser",
			Email:    "test@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if resp == nil {
			t.Fatal("expected response, got nil")
		}
		if resp.User == nil {
			t.Fatal("expected user in response, got nil")
		}
		if resp.User.Username == "" {
			t.Error("expected non-empty auto-generated username, got empty")
		}
		if !strings.HasPrefix(resp.User.Username, "test_") {
			t.Errorf("expected username derived from email local-part, got %s", resp.User.Username)
		}
		if resp.AccessToken == "" {
			t.Error("expected access token, got empty")
		}
		if resp.RefreshToken == "" {
			t.Error("expected refresh token, got empty")
		}
	})

	t.Run("username is auto-generated internal identifier", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		resp1, err := svc.Register(&RegisterRequest{
			Username: "testuser",
			Email:    "first@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err != nil {
			t.Fatalf("first register failed: %v", err)
		}

		resp2, err := svc.Register(&RegisterRequest{
			Username: "testuser",
			Email:    "second@example.com",
			DisplayName:       "Second User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err != nil {
			t.Fatalf("second register failed: %v", err)
		}

		// Both succeed because username is a system-generated identifier, not a
		// user-supplied login key. Generated usernames must differ (snowflake suffix).
		if resp1.User.Username == resp2.User.Username {
			t.Errorf("expected distinct generated usernames, got %s", resp1.User.Username)
		}
		if resp1.User.Username == "testuser" {
			t.Errorf("expected username to be auto-generated (not the request username), got %s", resp1.User.Username)
		}
		if resp1.User.DisplayName != "Test User" {
			t.Errorf("expected DisplayName 'Test User', got %q", resp1.User.DisplayName)
		}
	})

	t.Run("duplicate email", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		_, err := svc.Register(&RegisterRequest{
			Username: "firstuser",
			Email:    "test@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err != nil {
			t.Fatalf("first register failed: %v", err)
		}

		_, err = svc.Register(&RegisterRequest{
			Username: "seconduser",
			Email:    "test@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err == nil {
			t.Fatal("expected error for duplicate email, got nil")
		}
		if err.Error() != "[AUTH_EMAIL_EXISTS] email already registered" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid password", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		_, err := svc.Register(&RegisterRequest{
			Username: "testuser",
			Email:    "test@example.com",
			DisplayName:       "Test User",
			Password: "short",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err == nil {
			t.Fatal("expected error for invalid password, got nil")
		}
	})

	t.Run("display name unique within default space", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		space := &model.Space{ID: idgen.NextString(), Name: "Default", OwnerID: "", CreatedAt: time.Now()}
		if err := db.Create(space).Error; err != nil {
			t.Fatalf("create space: %v", err)
		}

		_, err := svc.Register(&RegisterRequest{
			Username: "u1",
			Email:    "u1@example.com",
			DisplayName:       "Alice",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err != nil {
			t.Fatalf("first register failed: %v", err)
		}

		// 大小写不敏感：同空间注册 "alice" 冲突
		_, err = svc.Register(&RegisterRequest{
			Username: "u2",
			Email:    "u2@example.com",
			DisplayName:       "alice",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err == nil {
			t.Fatal("expected display name conflict, got nil")
		}
		appErr, ok := err.(*errors.AppError)
		if !ok || appErr.Code != errors.AUTH_DISPLAY_NAME_EXISTS {
			t.Errorf("expected AUTH_DISPLAY_NAME_EXISTS, got %v", err)
		}

		// 非冲突名注册成功
		_, err = svc.Register(&RegisterRequest{
			Username: "u3",
			Email:    "u3@example.com",
			DisplayName:       "Bob",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err != nil {
			t.Errorf("expected non-conflicting register to succeed, got %v", err)
		}
	})
}

// TestRegisterAutoJoinDefaultSpaceMultiSpace verifies ISSUE-080: when multiple
// spaces exist, Register must auto-join the originally bootstrapped space
// (earliest created_at), not whichever row a plain First() happens to return.
// Joining a leftover empty shell space breaks voice access (403) because the
// user's membership.space no longer matches the channels' space.
func TestRegisterAutoJoinDefaultSpaceMultiSpace(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())

	// Main space created first (the bootstrapped one), then a leftover shell
	// space created later. A rowid-ordered First() could return either, so the
	// fix must key on created_at.
	mainSpace := &model.Space{
		ID:        idgen.NextString(),
		Name:      "Main Space",
		OwnerID:   "",
		CreatedAt: time.Now().Add(-time.Hour),
	}
	shellSpace := &model.Space{
		ID:        idgen.NextString(),
		Name:      "Shell Space",
		OwnerID:   "",
		CreatedAt: time.Now(),
	}
	if err := db.Create(mainSpace).Error; err != nil {
		t.Fatalf("create main space: %v", err)
	}
	if err := db.Create(shellSpace).Error; err != nil {
		t.Fatalf("create shell space: %v", err)
	}

	resp, err := svc.Register(&RegisterRequest{
		Username:          "multispace",
		Email:             "multispace@example.com",
		DisplayName:       "Test User",
		Password:          "Password123!",
		SecurityQuestions: testSecurityQuestions(),
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	var membership model.Membership
	if err := db.Where("user_id = ?", resp.User.ID).First(&membership).Error; err != nil {
		t.Fatalf("expected membership for new user: %v", err)
	}
	if membership.SpaceID != mainSpace.ID {
		t.Errorf("expected auto-join to main space %s, got %s", mainSpace.ID, membership.SpaceID)
	}
	if resp.Space == nil || resp.Space.ID != mainSpace.ID {
		t.Errorf("expected response space %s, got %+v", mainSpace.ID, resp.Space)
	}
}

func TestLogin(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		// Create user directly to avoid generating a session during register
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

		resp, err := svc.Login(&LoginRequest{
			Username: "testuser",
			Email:    "test@example.com",
			Password: "Password123!",
		})
		if err != nil {
			if ae, ok := err.(*errors.AppError); ok {
				t.Fatalf("expected no error, got AppError{Code:%s Message:%s Status:%d Details:%q}", ae.Code, ae.Message, ae.Status, ae.Details)
			}
			t.Fatalf("expected no error, got %v", err)
		}
		if resp == nil || resp.User == nil {
			t.Fatal("expected response with user")
		}
	})

	t.Run("invalid credentials - user not found", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		_, err := svc.Login(&LoginRequest{
			Username: "nonexistent",
			Email:    "nonexistent@example.com",
			Password: "Password123!",
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "[AUTH_INVALID_CREDENTIALS] invalid email or password" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

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

		_, err := svc.Login(&LoginRequest{
			Username: "testuser",
			Email:    "test@example.com",
			Password: "WrongPassword123!",
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "[AUTH_INVALID_CREDENTIALS] invalid email or password" {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestGetUserByID(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		registered, err := svc.Register(&RegisterRequest{
			Username: "testuser",
			Email:    "test@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err != nil {
			t.Fatalf("register failed: %v", err)
		}

		user, err := svc.GetUserByID(registered.User.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if user == nil {
			t.Fatal("expected user, got nil")
		}
		if user.ID != registered.User.ID {
			t.Errorf("expected user ID %s, got %s", registered.User.ID, user.ID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		_, err := svc.GetUserByID("999999999")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestCheckServerInitialized(t *testing.T) {
	t.Run("false when no users", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		initialized, err := svc.CheckServerInitialized()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if initialized {
			t.Error("expected initialized to be false")
		}
	})

	t.Run("true after creating a user", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())

		_, err := svc.Register(&RegisterRequest{
			Username: "testuser",
			Email:    "test@example.com",
			DisplayName:       "Test User",
			Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
		})
		if err != nil {
			t.Fatalf("register failed: %v", err)
		}

		initialized, err := svc.CheckServerInitialized()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !initialized {
			t.Error("expected initialized to be true")
		}
	})
}

// TestBatch2AuthDisabledUser verifies H7: disabled user gets AUTH_USER_DISABLED error.
func TestBatch2AuthDisabledUser(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())

	// Register a user
	resp, err := svc.Register(&RegisterRequest{
		Username: "disableduser",
		Email:    "disabled@example.com",
		DisplayName:       "Test User",
		Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Disable the user
	if err := db.Model(&model.User{}).Where("id = ?", resp.User.ID).Update("is_active", false).Error; err != nil {
		t.Fatalf("failed to disable user: %v", err)
	}

	// Try to login — should get AUTH_USER_DISABLED
	_, err = svc.Login(&LoginRequest{
		Username: "disableduser",
		Email:    "disabled@example.com",
		Password: "Password123!",
	})
	if err == nil {
		t.Fatal("expected error for disabled user login, got nil")
	}
	appErr, ok := err.(*errors.AppError)
	if !ok {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.Code != errors.AUTH_USER_DISABLED {
		t.Errorf("expected AUTH_USER_DISABLED, got %s", appErr.Code)
	}
	if appErr.Status != 403 {
		t.Errorf("expected status 403, got %d", appErr.Status)
	}
}

// TestBatch2RegisterResponseSpace verifies H4: register response includes space info.
func TestBatch2RegisterResponseSpace(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())

	// Create a default space first (Register auto-joins the first space)
	space := &model.Space{
		ID:      idgen.NextString(),
		Name:    "Test Space",
		OwnerID: "",
	}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}

	resp, err := svc.Register(&RegisterRequest{
		Username: "spacetest",
		Email:    "space@example.com",
		DisplayName:       "Test User",
		Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if resp.Space == nil {
		t.Fatal("expected space in response, got nil (design doc §10.2 requires space field)")
	}
	if resp.Space.ID == "" {
		t.Error("expected space ID, got empty")
	}
	if resp.Space.Name == "" {
		t.Error("expected space name, got empty")
	}
	if resp.Space.Role == "" {
		t.Error("expected space role, got empty")
	}
}

// TestBatch2LoginResponseSpace verifies H4: login response includes space info.
func TestBatch2LoginResponseSpace(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())

	// Create a default space first
	space := &model.Space{
		ID:      idgen.NextString(),
		Name:    "Test Space",
		OwnerID: "",
	}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}

	// Register
	_, err := svc.Register(&RegisterRequest{
		Username: "loginspacetest",
		Email:    "loginspace@example.com",
		DisplayName:       "Test User",
		Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Login
	resp, err := svc.Login(&LoginRequest{
		Username: "loginspacetest",
		Email:    "loginspace@example.com",
		Password: "Password123!",
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if resp.Space == nil {
		t.Fatal("expected space in login response, got nil (design doc §10.3 requires space field)")
	}
	if resp.Space.ID == "" {
		t.Error("expected space ID, got empty")
	}
}

// TestBatch2AdminSessionTTL verifies H2: admin sessions have shorter TTL.
func TestBatch2AdminSessionTTL(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	cfg.AdminSessionTTL = 15 // 15 minutes
	svc := NewService(db, cfg)

	// Create an admin user
	admin := &model.User{
		ID:           idgen.NextString(),
		Username:     "adminuser",
		Email:        "admin@example.com",
		Role:         middleware.RoleAdmin,
		PasswordHash: "$2a$12$test",
		IsActive:     true,
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	// Generate token pair for admin
	resp, err := svc.generateTokenPair(admin, SessionDevice{})
	if err != nil {
		t.Fatalf("generateTokenPair failed: %v", err)
	}

	// Verify session has IsAdminSession=true and shorter TTL
	var session model.UserSession
	if err := db.Where("token_hash = ?", hashRefreshToken(resp.RefreshToken)).First(&session).Error; err != nil {
		t.Fatalf("find session: %v", err)
	}
	if !session.IsAdminSession {
		t.Error("expected IsAdminSession=true for admin user")
	}
	// Admin session should expire in ~15 minutes, not 7 days
	expectedMaxExpiry := session.CreatedAt.Add(20 * time.Minute) // 15min + buffer
	if session.ExpiresAt.After(expectedMaxExpiry) {
		t.Errorf("admin session ExpiresAt = %v, should be within 20 minutes of creation", session.ExpiresAt)
	}
}

// TestBatch2NormalUserSessionTTL verifies H2: normal user sessions have 7-day TTL.
func TestBatch2NormalUserSessionTTL(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := testConfig()
	svc := NewService(db, cfg)

	// Register a normal user
	resp, err := svc.Register(&RegisterRequest{
		Username: "normaluser",
		Email:    "normal@example.com",
		DisplayName:       "Test User",
		Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Verify session has IsAdminSession=false and 7-day TTL
	var session model.UserSession
	if err := db.Where("token_hash = ?", hashRefreshToken(resp.RefreshToken)).First(&session).Error; err != nil {
		t.Fatalf("find session: %v", err)
	}
	if session.IsAdminSession {
		t.Error("expected IsAdminSession=false for normal user")
	}
	// Normal session should expire in ~7 days
	expectedMinExpiry := session.CreatedAt.Add(6 * 24 * time.Hour) // at least 6 days
	if session.ExpiresAt.Before(expectedMinExpiry) {
		t.Errorf("normal session ExpiresAt = %v, should be at least 6 days from creation", session.ExpiresAt)
	}
}

// TestCreateOwnerFromEnvSuccess verifies H1: env-based owner creation succeeds on an uninitialized server.
func TestCreateOwnerFromEnvSuccess(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())

	resp, err := svc.CreateOwnerFromEnv(&RegisterRequest{
		Username:          "envowner",
		Email:             "envowner@example.com",
		Password:          "Password123!",
		DisplayName:       "Env Owner",
		SecurityQuestions: testSecurityQuestions(),
	}, "MyServer")
	if err != nil {
		t.Fatalf("CreateOwnerFromEnv failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response, got nil")
	}
	if resp.User == nil {
		t.Fatal("expected non-nil user")
	}
	if !strings.HasPrefix(resp.User.Username, "envowner_") {
		t.Errorf("expected auto-generated username with envowner prefix, got %+v", resp.User.Username)
	}
	if resp.User.DisplayName != "Env Owner" {
		t.Errorf("expected DisplayName 'Env Owner', got %q", resp.User.DisplayName)
	}
	if resp.User.Role != middleware.RoleOwner {
		t.Errorf("expected role OWNER, got %s", resp.User.Role)
	}

	// Verify owner, space, membership, channels were created
	var userCount int64
	db.Model(&model.User{}).Count(&userCount)
	if userCount != 1 {
		t.Errorf("expected 1 user, got %d", userCount)
	}
	var spaceCount int64
	db.Model(&model.Space{}).Count(&spaceCount)
	if spaceCount != 1 {
		t.Errorf("expected 1 space, got %d", spaceCount)
	}
	var channelCount int64
	db.Model(&model.Channel{}).Count(&channelCount)
	if channelCount != 2 {
		t.Errorf("expected 2 default channels, got %d", channelCount)
	}
}

// TestCreateOwnerFromEnvAlreadyInitialized verifies H1: env-based owner creation
// fails with AUTH_ALREADY_INITIALIZED when the server is already initialized.
func TestCreateOwnerFromEnvAlreadyInitialized(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())

	// First create owner successfully
	_, err := svc.CreateOwnerFromEnv(&RegisterRequest{
		Username: "firstowner",
		Email:    "first@example.com",
		DisplayName:       "Test User",
		Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
	}, "")
	if err != nil {
		t.Fatalf("first CreateOwnerFromEnv failed: %v", err)
	}

	// Second attempt should fail
	_, err = svc.CreateOwnerFromEnv(&RegisterRequest{
		Username: "secondowner",
		Email:    "second@example.com",
		DisplayName:       "Test User",
		Password: "Password123!",
			SecurityQuestions: testSecurityQuestions(),
	}, "")
	if err == nil {
		t.Fatal("expected error on second CreateOwnerFromEnv, got nil")
	}
	appErr, ok := err.(*errors.AppError)
	if !ok {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.Code != errors.AUTH_ALREADY_INITIALIZED {
		t.Errorf("expected AUTH_ALREADY_INITIALIZED, got %s", appErr.Code)
	}
}

// TestOwnerConfigIsConfigured verifies H1: OwnerConfig.IsConfigured() logic.
func TestOwnerConfigIsConfigured(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.OwnerConfig
		expected bool
	}{
		{"all set", config.OwnerConfig{Username: "u", Password: "p", Email: "e"}, true},
		{"missing username", config.OwnerConfig{Password: "p", Email: "e"}, false},
		{"missing password", config.OwnerConfig{Username: "u", Email: "e"}, false},
		{"missing email", config.OwnerConfig{Username: "u", Password: "p"}, false},
		{"all empty", config.OwnerConfig{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsConfigured(); got != tt.expected {
				t.Errorf("IsConfigured() = %v, want %v", got, tt.expected)
			}
		})
	}
}
