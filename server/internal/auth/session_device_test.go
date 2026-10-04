package auth

// T16「S-2：多设备标识」测试：设备字段落库 / refresh 继承与覆盖 /
// 会话列表归属隔离 / 踢除会话的 404 与当前会话拒绝语义。
//
// 注意：testutil.MustSetupTestDB 每次调用返回独立的内存 SQLite，
// 因此每个用例先建一个 db，Service 与所有种子数据共用同一个实例。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"

	"gorm.io/gorm"
)

// newDeviceTestUser creates a fresh active user directly in db (bypassing
// Register so device tests don't depend on registration behavior).
func newDeviceTestUser(t *testing.T, db *gorm.DB, email string) *model.User {
	t.Helper()
	user := &model.User{
		ID:           idgen.GenerateID(idgen.PrefixUser),
		Username:     "dev_" + email,
		Email:        email,
		PasswordHash: "$2a$10$dummyhashnotcheckedhere0000000000000000000000000000",
		DisplayName:  "Device User",
		Role:         middleware.RoleMember,
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	// Overwrite the dummy hash with a real bcrypt hash so Login passes.
	hash, err := crypto.HashPassword("Password123!")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).
		Update("password_hash", hash).Error; err != nil {
		t.Fatalf("seed password: %v", err)
	}
	return user
}

// loginWithDevice performs a real Login and returns the response.
func loginWithDevice(t *testing.T, svc *Service, email, deviceType, deviceName string) *TokenResponse {
	t.Helper()
	resp, err := svc.Login(&LoginRequest{
		Email:      email,
		Password:   "Password123!",
		DeviceType: deviceType,
		DeviceName: deviceName,
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	return resp
}

// sessionByID fetches the raw session row for assertions.
func sessionByID(t *testing.T, db *gorm.DB, sessionID string) *model.UserSession {
	t.Helper()
	var sess model.UserSession
	if err := db.Where("id = ?", sessionID).First(&sess).Error; err != nil {
		t.Fatalf("session %s not found: %v", sessionID, err)
	}
	return &sess
}

// sessionIDFromToken parses the session_id claim out of a refresh token (M1).
func sessionIDFromToken(t *testing.T, refreshToken string) string {
	t.Helper()
	claims, err := middleware.ParseToken(refreshToken, testConfig())
	if err != nil {
		t.Fatalf("parse refresh token: %v", err)
	}
	if claims.SessionID == "" {
		t.Fatal("refresh token has empty session_id claim")
	}
	return claims.SessionID
}

func TestLoginStoresDeviceFields(t *testing.T) {
	t.Run("device fields persisted on login", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		u := newDeviceTestUser(t, db, "device-login@example.com")

		_ = loginWithDevice(t, svc, "device-login@example.com", "windows-desktop", "DEV-PC")

		var sessions []model.UserSession
		if err := db.Where("user_id = ?", u.ID).Find(&sessions).Error; err != nil {
			t.Fatalf("query sessions: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("expected 1 session, got %d", len(sessions))
		}
		sess := sessions[0]
		if sess.DeviceType != "windows-desktop" {
			t.Errorf("expected deviceType 'windows-desktop', got %q", sess.DeviceType)
		}
		if sess.DeviceName != "DEV-PC" {
			t.Errorf("expected deviceName 'DEV-PC', got %q", sess.DeviceName)
		}
		if sess.LastActiveAt.IsZero() {
			t.Error("expected non-zero lastActiveAt on fresh login")
		}
	})

	t.Run("missing device fields default to empty string", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		u := newDeviceTestUser(t, db, "device-empty@example.com")

		if _, err := svc.Login(&LoginRequest{Email: "device-empty@example.com", Password: "Password123!"}); err != nil {
			t.Fatalf("login failed: %v", err)
		}

		var sess model.UserSession
		if err := db.Where("user_id = ?", u.ID).First(&sess).Error; err != nil {
			t.Fatalf("session not found: %v", err)
		}
		if sess.DeviceType != "" || sess.DeviceName != "" {
			t.Errorf("expected empty device fields, got type=%q name=%q", sess.DeviceType, sess.DeviceName)
		}
	})

	t.Run("overlong fields are truncated (rune-safe)", func(t *testing.T) {
		longType := strings.Repeat("x", 100) // > 32
		longName := strings.Repeat("米", 100) // > 64, CJK rune-safety
		got := sanitizeSessionDevice(longType, longName)
		if runeCount := len([]rune(got.Type)); runeCount != maxDeviceTypeLen {
			t.Errorf("expected deviceType truncated to %d runes, got %d", maxDeviceTypeLen, runeCount)
		}
		if runeCount := len([]rune(got.Name)); runeCount != maxDeviceNameLen {
			t.Errorf("expected deviceName truncated to %d runes, got %d", maxDeviceNameLen, runeCount)
		}
	})
}

func TestRefreshTokenPreservesDeviceFields(t *testing.T) {
	t.Run("rotation inherits device fields when not reported", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		newDeviceTestUser(t, db, "device-refresh@example.com")

		first := loginWithDevice(t, svc, "device-refresh@example.com", "windows-desktop", "DEV-PC")
		oldSessionID := sessionIDFromToken(t, first.RefreshToken)
		oldSess := sessionByID(t, db, oldSessionID)

		// Refresh WITHOUT device info → fields inherited from old row.
		time.Sleep(20 * time.Millisecond) // ensure lastActiveAt strictly increases
		second, err := svc.RefreshToken(first.RefreshToken, nil)
		if err != nil {
			t.Fatalf("refresh failed: %v", err)
		}
		newSess := sessionByID(t, db, sessionIDFromToken(t, second.RefreshToken))

		if newSess.ID == oldSess.ID {
			t.Fatal("expected rotation to create a new session row")
		}
		if newSess.DeviceType != "windows-desktop" || newSess.DeviceName != "DEV-PC" {
			t.Errorf("expected device fields inherited, got type=%q name=%q", newSess.DeviceType, newSess.DeviceName)
		}
		if !newSess.LastActiveAt.After(oldSess.LastActiveAt) {
			t.Errorf("expected lastActiveAt bumped on refresh: old=%v new=%v", oldSess.LastActiveAt, newSess.LastActiveAt)
		}
	})

	t.Run("explicit device report overrides inherited fields", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		newDeviceTestUser(t, db, "device-override@example.com")

		first := loginWithDevice(t, svc, "device-override@example.com", "windows-desktop", "OLD-PC")
		dev := sanitizeSessionDevice("windows-desktop", "RENAMED-PC")
		second, err := svc.RefreshToken(first.RefreshToken, &dev)
		if err != nil {
			t.Fatalf("refresh failed: %v", err)
		}
		newSess := sessionByID(t, db, sessionIDFromToken(t, second.RefreshToken))
		if newSess.DeviceName != "RENAMED-PC" {
			t.Errorf("expected deviceName override 'RENAMED-PC', got %q", newSess.DeviceName)
		}
	})
}

func TestListSessionsScopeAndCurrentMark(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, testConfig())
	userA := newDeviceTestUser(t, db, "list-a@example.com")
	newDeviceTestUser(t, db, "list-b@example.com")

	// User A logs in from two devices; user B from one.
	loginA1 := loginWithDevice(t, svc, "list-a@example.com", "windows-desktop", "A-DESKTOP")
	loginWithDevice(t, svc, "list-a@example.com", "web", "A-LAPTOP")
	loginWithDevice(t, svc, "list-b@example.com", "web", "B-LAPTOP")

	a1SessionID := sessionIDFromToken(t, loginA1.RefreshToken)

	// A lists sessions from device 1's perspective.
	sessions, err := svc.ListSessions(userA.ID, a1SessionID)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions for user A, got %d", len(sessions))
	}
	// Only user A's rows, never user B's.
	for _, s := range sessions {
		if s.DeviceName == "B-LAPTOP" {
			t.Error("user B's session leaked into user A's list")
		}
	}
	// Current-session flag marks exactly one row.
	currentCount := 0
	for _, s := range sessions {
		if s.IsCurrent {
			currentCount++
			if s.ID != a1SessionID {
				t.Errorf("wrong row flagged current: %s", s.ID)
			}
		}
	}
	if currentCount != 1 {
		t.Errorf("expected exactly 1 current session, got %d", currentCount)
	}
}

func TestRevokeSessionSemantics(t *testing.T) {
	// setup returns: svc, userA.ID, A's current session id, A's other session id, B's session id
	setup := func(t *testing.T) (*Service, *gorm.DB, string, string, string, string) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		userA := newDeviceTestUser(t, db, "revoke-a@example.com")
		newDeviceTestUser(t, db, "revoke-b@example.com")

		loginA1 := loginWithDevice(t, svc, "revoke-a@example.com", "windows-desktop", "A-DESKTOP")
		loginA2 := loginWithDevice(t, svc, "revoke-a@example.com", "web", "A-LAPTOP")
		loginB1 := loginWithDevice(t, svc, "revoke-b@example.com", "web", "B-LAPTOP")

		a1 := sessionIDFromToken(t, loginA1.RefreshToken)
		a2 := sessionIDFromToken(t, loginA2.RefreshToken)
		b1 := sessionIDFromToken(t, loginB1.RefreshToken)
		return svc, db, userA.ID, a1, a2, b1
	}

	t.Run("revoke own other session succeeds", func(t *testing.T) {
		svc, db, userA, a1, a2, _ := setup(t)

		if err := svc.RevokeSession(userA, a1, a2); err != nil {
			t.Fatalf("expected revoke of own other session to succeed, got %v", err)
		}
		var count int64
		db.Model(&model.UserSession{}).Where("id = ?", a2).Count(&count)
		if count != 0 {
			t.Error("expected revoked session row deleted")
		}
	})

	t.Run("revoke another user's session returns 404", func(t *testing.T) {
		svc, db, userA, a1, _, b1 := setup(t)

		err := svc.RevokeSession(userA, a1, b1)
		if err == nil {
			t.Fatal("expected error revoking foreign session, got nil")
		}
		if err != errors.ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
		var count int64
		db.Model(&model.UserSession{}).Where("id = ?", b1).Count(&count)
		if count != 1 {
			t.Error("expected foreign session row to remain")
		}
	})

	t.Run("revoke current session rejected with 400", func(t *testing.T) {
		svc, db, userA, a1, _, _ := setup(t)

		err := svc.RevokeSession(userA, a1, a1)
		if err == nil {
			t.Fatal("expected error revoking current session, got nil")
		}
		appErr, ok := err.(*errors.AppError)
		if !ok {
			t.Fatalf("expected AppError, got %T", err)
		}
		if appErr.Status != 400 {
			t.Errorf("expected HTTP 400, got %d", appErr.Status)
		}
		var count int64
		db.Model(&model.UserSession{}).Where("id = ?", a1).Count(&count)
		if count != 1 {
			t.Error("expected current session row to remain")
		}
	})

	t.Run("unknown session id returns 404", func(t *testing.T) {
		svc, _, userA, a1, _, _ := setup(t)
		err := svc.RevokeSession(userA, a1, "nonexistent-session-id")
		if err != errors.ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

// TestSessionDeviceJSONContract guards the wire format consumed by the
// client's「登录设备」UI (deviceType/deviceName/lastActiveAt/isCurrent).
func TestSessionDeviceJSONContract(t *testing.T) {
	info := SessionInfo{
		ID:           "sess-1",
		DeviceType:   "windows-desktop",
		DeviceName:   "DEV-PC",
		LastActiveAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		CreatedAt:    time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		ExpiresAt:    time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		IsCurrent:    true,
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "deviceType", "deviceName", "lastActiveAt", "createdAt", "expiresAt", "isCurrent"} {
		if _, ok := m[key]; !ok {
			t.Errorf("SessionInfo JSON missing key %q", key)
		}
	}
}
