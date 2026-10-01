package voice

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func createSpaceAndMembership(t *testing.T, db *gorm.DB, spaceID, userID, role string) {
	t.Helper()
	space := &model.Space{
		ID:      spaceID,
		Name:    "test-space",
		OwnerID: userID,
	}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("failed to create space: %v", err)
	}
	if role == middleware.RoleOwner || role == middleware.RoleAdmin {
		return
	}
	membership := &model.Membership{
		ID:      idgen.NextString(),
		SpaceID: spaceID,
		UserID:  userID,
		Role:    role,
	}
	if err := db.Create(membership).Error; err != nil {
		t.Fatalf("failed to create membership: %v", err)
	}
}

func TestGenerateToken(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		spaceID := idgen.NextString()
		userID := idgen.NextString()
		createSpaceAndMembership(t, db, spaceID, userID, middleware.RoleMember)

		room := &model.VoiceRoom{
			ID:      idgen.NextString(),
			SpaceID: spaceID,
			Quality: "standard",
		}
		if err := db.Create(room).Error; err != nil {
			t.Fatalf("failed to create voice room: %v", err)
		}

		token, err := svc.GenerateToken(userID, "testuser", middleware.RoleMember, room.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if token == "" {
			t.Error("expected token, got empty string")
		}
	})

	t.Run("voice room not found", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		_, err := svc.GenerateToken("user-1", "testuser", middleware.RoleMember, "nonexistent-room")
		if err == nil {
			t.Fatal("expected error for nonexistent room, got nil")
		}
		if err.Error() != "[VOICE_ROOM_NOT_FOUND] voice room not found" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("forbidden when not space member", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		spaceID := idgen.NextString()
		room := &model.VoiceRoom{
			ID:      idgen.NextString(),
			SpaceID: spaceID,
			Quality: "standard",
		}
		if err := db.Create(room).Error; err != nil {
			t.Fatalf("failed to create voice room: %v", err)
		}

		_, err := svc.GenerateToken("user-1", "testuser", middleware.RoleMember, room.ID)
		if err == nil {
			t.Fatal("expected error for non-member, got nil")
		}
		if err.Error() != "[AUTH_FORBIDDEN] forbidden" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("owner allowed without membership", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		spaceID := idgen.NextString()
		room := &model.VoiceRoom{
			ID:      idgen.NextString(),
			SpaceID: spaceID,
			Quality: "standard",
		}
		if err := db.Create(room).Error; err != nil {
			t.Fatalf("failed to create voice room: %v", err)
		}

		token, err := svc.GenerateToken("user-1", "testuser", middleware.RoleOwner, room.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if token == "" {
			t.Error("expected token, got empty string")
		}
	})

	t.Run("auto-creates room from existing channel", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		spaceID := idgen.NextString()
		userID := idgen.NextString()
		createSpaceAndMembership(t, db, spaceID, userID, middleware.RoleMember)

		channel := &model.Channel{
			ID:           idgen.NextString(),
			SpaceID:      spaceID,
			Name:         "voice-channel",
			Type:         "VOICE",
			Visibility:   "public",
			VoiceQuality: "high",
		}
		if err := db.Create(channel).Error; err != nil {
			t.Fatalf("failed to create channel: %v", err)
		}

		token, err := svc.GenerateToken(userID, "testuser", middleware.RoleMember, channel.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if token == "" {
			t.Error("expected token, got empty string")
		}

		var room model.VoiceRoom
		if err := db.First(&room, "id = ?", channel.ID).Error; err != nil {
			t.Fatalf("expected voice room to be auto-created: %v", err)
		}
		if room.SpaceID != spaceID {
			t.Errorf("expected space_id %s, got %s", spaceID, room.SpaceID)
		}
		if room.Quality != "high" {
			t.Errorf("expected quality high, got %s", room.Quality)
		}
		if room.BindChannelID == nil || *room.BindChannelID != channel.ID {
			t.Errorf("expected bind_channel_id %s, got %v", channel.ID, room.BindChannelID)
		}
	})
}

func TestJoinVoice(t *testing.T) {
	t.Run("records user joining", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		room := model.VoiceRoom{
			ID:      idgen.NextString(),
			SpaceID: idgen.NextString(),
		}
		if err := db.Create(&room).Error; err != nil {
			t.Fatalf("failed to create room: %v", err)
		}

		err := svc.JoinVoice("user-1", room.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// Verify participant was recorded
		var participant model.VoiceParticipant
		if err := db.Where("room_id = ? AND user_id = ?", room.ID, "user-1").First(&participant).Error; err != nil {
			t.Fatalf("expected participant to exist: %v", err)
		}
		if participant.UserID != "user-1" {
			t.Errorf("expected user_id user-1, got %s", participant.UserID)
		}
	})

	t.Run("idempotent join", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		room := model.VoiceRoom{
			ID:      idgen.NextString(),
			SpaceID: idgen.NextString(),
		}
		if err := db.Create(&room).Error; err != nil {
			t.Fatalf("failed to create room: %v", err)
		}

		// Join twice
		if err := svc.JoinVoice("user-1", room.ID); err != nil {
			t.Fatalf("first join failed: %v", err)
		}
		if err := svc.JoinVoice("user-1", room.ID); err != nil {
			t.Fatalf("second join failed: %v", err)
		}

		// Should still only have one active participant record
		var count int64
		db.Model(&model.VoiceParticipant{}).Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, "user-1").Count(&count)
		if count != 1 {
			t.Errorf("expected 1 active participant record, got %d", count)
		}
	})
}

func TestLeaveVoice(t *testing.T) {
	t.Run("records user leaving", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		room := model.VoiceRoom{
			ID:      idgen.NextString(),
			SpaceID: idgen.NextString(),
		}
		if err := db.Create(&room).Error; err != nil {
			t.Fatalf("failed to create room: %v", err)
		}

		// Join first
		if err := svc.JoinVoice("user-1", room.ID); err != nil {
			t.Fatalf("join failed: %v", err)
		}

		// Then leave
		err := svc.LeaveVoice("user-1", room.ID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		// 软删除模式：验证活跃参与者（left_at IS NULL）为 0
		var activeCount int64
		db.Model(&model.VoiceParticipant{}).Where("room_id = ? AND user_id = ? AND left_at IS NULL", room.ID, "user-1").Count(&activeCount)
		if activeCount != 0 {
			t.Errorf("expected 0 active participant records, got %d", activeCount)
		}

		// 验证记录仍存在（软删除），且 left_at 已设置
		var participant model.VoiceParticipant
		if err := db.Where("room_id = ? AND user_id = ?", room.ID, "user-1").First(&participant).Error; err != nil {
			t.Fatalf("expected soft-deleted participant record to exist: %v", err)
		}
		if participant.LeftAt == nil {
			t.Error("expected left_at to be set after leaving")
		}
		if participant.DisconnectReason != "user_left" {
			t.Errorf("expected disconnect_reason 'user_left', got '%s'", participant.DisconnectReason)
		}
	})

	t.Run("no-op when room does not exist", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		err := svc.LeaveVoice("user-1", "nonexistent-room")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})
}

func TestCleanupOnStartup(t *testing.T) {
	t.Run("cleans up stale records", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		room := model.VoiceRoom{
			ID:          idgen.NextString(),
			SpaceID:     idgen.NextString(),
			LiveKitRoom: "rrt-room-" + idgen.NextString(),
		}
		if err := db.Create(&room).Error; err != nil {
			t.Fatalf("failed to create room: %v", err)
		}

		// Create a user
		user := model.User{
			ID:       idgen.NextString(),
			Username: "testuser",
			Email:    "test@example.com",
			Role:     middleware.RoleMember,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}

		// Create a stale participant (older than 1 hour)
		staleTime := time.Now().Add(-2 * time.Hour)
		participant := model.VoiceParticipant{
			ID:           idgen.NextString(),
			RoomID:       room.ID,
			UserID:       user.ID,
			JoinedAt:     staleTime,
			LastActiveAt: staleTime,
		}
		if err := db.Create(&participant).Error; err != nil {
			t.Fatalf("failed to create participant: %v", err)
		}

		// Run cleanup
		svc.CleanupOnStartup()

		// 软删除模式：验证活跃参与者（left_at IS NULL）为 0
		var activeCount int64
		db.Model(&model.VoiceParticipant{}).Where("id = ? AND left_at IS NULL", participant.ID).Count(&activeCount)
		if activeCount != 0 {
			t.Errorf("expected stale participant to be soft-deleted (left_at set), got %d active", activeCount)
		}

		// 验证记录仍存在且 left_at 已设置
		var p model.VoiceParticipant
		if err := db.Where("id = ?", participant.ID).First(&p).Error; err != nil {
			t.Fatalf("expected soft-deleted record to exist: %v", err)
		}
		if p.LeftAt == nil {
			t.Error("expected left_at to be set after cleanup")
		}
		if p.DisconnectReason != "startup_cleanup" {
			t.Errorf("expected disconnect_reason 'startup_cleanup', got '%s'", p.DisconnectReason)
		}
	})

	t.Run("cleans up orphan user records", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		room := model.VoiceRoom{
			ID:          idgen.NextString(),
			SpaceID:     idgen.NextString(),
			LiveKitRoom: "rrt-room-" + idgen.NextString(),
		}
		if err := db.Create(&room).Error; err != nil {
			t.Fatalf("failed to create room: %v", err)
		}

		// Create a participant for a non-existent user (orphan)
		participant := model.VoiceParticipant{
			ID:           idgen.NextString(),
			RoomID:       room.ID,
			UserID:       "nonexistent-user",
			JoinedAt:     time.Now(),
			LastActiveAt: time.Now(),
		}
		if err := db.Create(&participant).Error; err != nil {
			t.Fatalf("failed to create participant: %v", err)
		}

		// Run cleanup
		svc.CleanupOnStartup()

		// 软删除模式：验证活跃参与者（left_at IS NULL）为 0
		var activeCount int64
		db.Model(&model.VoiceParticipant{}).Where("id = ? AND left_at IS NULL", participant.ID).Count(&activeCount)
		if activeCount != 0 {
			t.Errorf("expected orphan participant to be soft-deleted, got %d active", activeCount)
		}
	})
}

// TestRotateE2EEKeyBatch4 批次4新增：密钥轮换后应生成新密钥
func TestRotateE2EEKeyBatch4(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	room := model.VoiceRoom{
		ID:      idgen.NextString(),
		SpaceID: idgen.NextString(),
	}
	if err := db.Create(&room).Error; err != nil {
		t.Fatalf("failed to create room: %v", err)
	}

	// Directly create an initial key to test rotation
	initialKey := &model.E2EEKey{
		ID:     idgen.NextString(),
		RoomID: room.ID,
		KeyBytes: "initial-encrypted-key",
	}
	if err := db.Create(initialKey).Error; err != nil {
		t.Fatalf("failed to create initial key: %v", err)
	}

	// Rotate key
	if err := svc.RotateE2EEKey(room.ID); err != nil {
		t.Fatalf("RotateE2EEKey failed: %v", err)
	}

	// Verify old key is deleted
	var oldKeyCount int64
	db.Model(&model.E2EEKey{}).Where("id = ?", initialKey.ID).Count(&oldKeyCount)
	if oldKeyCount != 0 {
		t.Errorf("expected old key to be deleted, got count %d", oldKeyCount)
	}

	// Verify new key is generated
	var newKey model.E2EEKey
	if err := db.Where("room_id = ?", room.ID).First(&newKey).Error; err != nil {
		t.Fatalf("expected new key to exist after rotation: %v", err)
	}
	if newKey.ID == initialKey.ID {
		t.Error("expected new key to have different ID from old key")
	}
	if newKey.KeyBytes == "initial-encrypted-key" {
		t.Error("expected new key to have different key bytes from old key")
	}
}

// ===== A1 屏幕共享虚身份 token 测试 =====

// setupTokenTestRoom 创建空间+成员+语音房，返回 service 与可用 ID。
func setupTokenTestRoom(t *testing.T) (*Service, string, string) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	spaceID := idgen.NextString()
	userID := idgen.NextString()
	createSpaceAndMembership(t, db, spaceID, userID, middleware.RoleMember)

	room := &model.VoiceRoom{
		ID:      idgen.NextString(),
		SpaceID: spaceID,
		Quality: "standard",
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}
	return svc, userID, room.ID
}

// parseTokenIdentity 不验签，仅解出 JWT payload 里的 sub 字段（identity）。
func parseTokenIdentity(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3-part JWT, got %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("failed to decode JWT payload: %v", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("failed to parse JWT payload: %v", err)
	}
	return claims.Sub
}

func TestGenerateTokenWithIdentity(t *testing.T) {
	t.Run("share virtual identity accepted", func(t *testing.T) {
		svc, userID, roomID := setupTokenTestRoom(t)

		token, err := svc.GenerateTokenWithIdentity(userID, "testuser", middleware.RoleMember, roomID, userID+ShareIdentitySuffix)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if got := parseTokenIdentity(t, token); got != userID+ShareIdentitySuffix {
			t.Errorf("expected token identity %q, got %q", userID+ShareIdentitySuffix, got)
		}
	})

	t.Run("voice-native virtual identity accepted", func(t *testing.T) {
		svc, userID, roomID := setupTokenTestRoom(t)

		token, err := svc.GenerateTokenWithIdentity(userID, "testuser", middleware.RoleMember, roomID, userID+VoiceIdentitySuffix)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if got := parseTokenIdentity(t, token); got != userID+VoiceIdentitySuffix {
			t.Errorf("expected token identity %q, got %q", userID+VoiceIdentitySuffix, got)
		}
	})

	t.Run("other user's voice-native identity rejected", func(t *testing.T) {
		svc, userID, roomID := setupTokenTestRoom(t)

		otherID := idgen.NextString()
		_, err := svc.GenerateTokenWithIdentity(userID, "testuser", middleware.RoleMember, roomID, otherID+VoiceIdentitySuffix)
		if err == nil {
			t.Fatal("expected error for other user's voice-native identity, got nil")
		}
		if err.Error() != "[AUTH_FORBIDDEN] forbidden" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("other user's share identity rejected", func(t *testing.T) {
		svc, userID, roomID := setupTokenTestRoom(t)

		otherID := idgen.NextString()
		_, err := svc.GenerateTokenWithIdentity(userID, "testuser", middleware.RoleMember, roomID, otherID+ShareIdentitySuffix)
		if err == nil {
			t.Fatal("expected error for other user's share identity, got nil")
		}
		if err.Error() != "[AUTH_FORBIDDEN] forbidden" {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("arbitrary identity rejected", func(t *testing.T) {
		svc, userID, roomID := setupTokenTestRoom(t)

		_, err := svc.GenerateTokenWithIdentity(userID, "testuser", middleware.RoleMember, roomID, userID+":admin")
		if err == nil {
			t.Fatal("expected error for arbitrary virtual identity, got nil")
		}
	})

	t.Run("empty identity equals GenerateToken", func(t *testing.T) {
		svc, userID, roomID := setupTokenTestRoom(t)

		token, err := svc.GenerateTokenWithIdentity(userID, "testuser", middleware.RoleMember, roomID, "")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if got := parseTokenIdentity(t, token); got != userID {
			t.Errorf("expected token identity %q, got %q", userID, got)
		}

		token2, err := svc.GenerateToken(userID, "testuser", middleware.RoleMember, roomID)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if got := parseTokenIdentity(t, token2); got != userID {
			t.Errorf("expected GenerateToken identity %q, got %q", userID, got)
		}
	})

	t.Run("virtual identity still requires membership", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg, nil)

		spaceID := idgen.NextString()
		room := &model.VoiceRoom{ID: idgen.NextString(), SpaceID: spaceID, Quality: "standard"}
		if err := db.Create(room).Error; err != nil {
			t.Fatalf("failed to create voice room: %v", err)
		}

		// 非成员即使请求自己的 :share 虚身份也必须被拒
		_, err := svc.GenerateTokenWithIdentity("user-1", "testuser", middleware.RoleMember, room.ID, "user-1"+ShareIdentitySuffix)
		if err == nil {
			t.Fatal("expected error for non-member virtual identity, got nil")
		}
		if err.Error() != "[AUTH_FORBIDDEN] forbidden" {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestValidateVirtualIdentity(t *testing.T) {
	cases := []struct {
		name     string
		identity string
		userID   string
		wantErr  bool
	}{
		{"real identity", "uid123", "uid123", false},
		{"own share identity", "uid123:share", "uid123", false},
		{"own voice-native identity", "uid123:voice-native", "uid123", false},
		{"other user's share identity", "uid456:share", "uid123", true},
		{"other user's voice-native identity", "uid456:voice-native", "uid123", true},
		{"arbitrary suffix", "uid123:screen", "uid123", true},
		{"voice-native suffix only", ":voice-native", "uid123", true},
		{"share suffix only", ":share", "uid123", true},
		{"empty identity", "", "uid123", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVirtualIdentity(tc.identity, tc.userID)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

// ===== L13 LiveKit E2EE 框架集成测试 =====

// TestGenerateTokenE2EEMetadata verifies L13: when LiveKitE2EEEnabled=true,
// the VoiceRoom is created with E2EEEnabled=true
func TestGenerateTokenE2EEMetadata(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.LiveKitE2EEEnabled = true // L13: 启用 E2EE
	svc := NewService(db, cfg, nil)

	spaceID := idgen.NextString()
	userID := idgen.NextString()
	createSpaceAndMembership(t, db, spaceID, userID, middleware.RoleMember)

	room := &model.VoiceRoom{
		ID:      idgen.NextString(),
		SpaceID: spaceID,
		Quality: "standard",
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}

	token, err := svc.GenerateToken(userID, "testuser", middleware.RoleOwner, room.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if token == "" {
		t.Error("expected token, got empty string")
	}

	// 验证 VoiceRoom 创建时 E2EEEnabled=true
	var stored model.VoiceRoom
	if err := db.First(&stored, "id = ?", room.ID).Error; err != nil {
		t.Fatalf("expected VoiceRoom to exist: %v", err)
	}
	if !stored.E2EEEnabled {
		t.Errorf("expected E2EEEnabled=true when config.LiveKitE2EEEnabled=true, got false")
	}
}

// TestGenerateTokenE2EEDisabled verifies L13: when LiveKitE2EEEnabled=false,
// the VoiceRoom is created with E2EEEnabled=false
func TestGenerateTokenE2EEDisabled(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.LiveKitE2EEEnabled = false // L13: 禁用 E2EE
	svc := NewService(db, cfg, nil)

	spaceID := idgen.NextString()
	userID := idgen.NextString()
	createSpaceAndMembership(t, db, spaceID, userID, middleware.RoleMember)

	room := &model.VoiceRoom{
		ID:      idgen.NextString(),
		SpaceID: spaceID,
		Quality: "standard",
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}

	_, err := svc.GenerateToken(userID, "testuser", middleware.RoleOwner, room.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// 验证 VoiceRoom 创建时 E2EEEnabled=false
	var stored model.VoiceRoom
	if err := db.First(&stored, "id = ?", room.ID).Error; err != nil {
		t.Fatalf("expected VoiceRoom to exist: %v", err)
	}
	if stored.E2EEEnabled {
		t.Errorf("expected E2EEEnabled=false when config.LiveKitE2EEEnabled=false, got true")
	}
}
