package screenshare

import (
	"testing"
	"time"

	"gorm.io/gorm"

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

// setupServiceTestSpace creates a user, space and membership for screenshare service tests.
func setupServiceTestSpace(t *testing.T) (*model.User, *model.Space, *gorm.DB) {
	db := testutil.MustSetupTestDB()

	passwordHash, _ := crypto.HashPassword("Password123!")
	user := &model.User{
		ID:           idgen.NextString(),
		Username:     "sharer",
		Email:        "sharer@example.com",
		PasswordHash: passwordHash,
		Role:         middleware.RoleMember,
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	space := &model.Space{
		ID:      idgen.NextString(),
		Name:    "Test Space",
		OwnerID: user.ID,
	}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}

	membership := &model.Membership{
		ID:       idgen.NextString(),
		UserID:   user.ID,
		SpaceID:  space.ID,
		Role:     middleware.RoleMember,
		JoinedAt: time.Now().UTC(),
	}
	if err := db.Create(membership).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	return user, space, db
}

func TestStartV2(t *testing.T) {
	t.Run("fills new fields with defaults", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		var session model.ScreenShareSession
		if err := db.Where("space_id = ? AND active = ?", space.ID, true).First(&session).Error; err != nil {
			t.Fatalf("expected active session: %v", err)
		}
		if session.ChannelID != channelID {
			t.Errorf("expected channelID %s, got %s", channelID, session.ChannelID)
		}
		if session.ShareType != "screen" {
			t.Errorf("expected shareType 'screen', got '%s'", session.ShareType)
		}
		if session.Status != "active" {
			t.Errorf("expected status 'active', got '%s'", session.Status)
		}
		if session.MaxViewers != 50 {
			t.Errorf("expected maxViewers 50, got %d", session.MaxViewers)
		}
		if !session.Active {
			t.Error("expected active=true")
		}
	})

	t.Run("fills new fields with custom values", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		channelID := idgen.NextString()
		req := StartRequest{
			ShareType:     "window",
			SourceID:      "win-123",
			Resolution:    "1920x1080",
			FrameRate:     30,
			MaxBitrate:    4000,
			ShareAudio:    true,
			SuppressVoice: true,
			MaxViewers:    10,
		}
		if _, err := svc.Start(user.ID, space.ID, channelID, req); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		var session model.ScreenShareSession
		if err := db.Where("space_id = ? AND active = ?", space.ID, true).First(&session).Error; err != nil {
			t.Fatalf("expected active session: %v", err)
		}
		if session.ShareType != "window" {
			t.Errorf("expected shareType 'window', got '%s'", session.ShareType)
		}
		if session.SourceID != "win-123" {
			t.Errorf("expected sourceId 'win-123', got '%s'", session.SourceID)
		}
		if session.MaxViewers != 10 {
			t.Errorf("expected maxViewers 10, got %d", session.MaxViewers)
		}
		if session.ChannelID != channelID {
			t.Errorf("expected channelID %s, got %s", channelID, session.ChannelID)
		}
	})

	t.Run("rejects second active session on same channel", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("first Start failed: %v", err)
		}

		_, err := svc.Start("user-2", space.ID, channelID, StartRequest{ShareType: "window", SourceID: "win-1"})
		if err == nil {
			t.Fatal("expected error for second active session on same channel")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SCREEN_SHARE_ALREADY_ACTIVE {
			t.Errorf("expected SCREEN_SHARE_ALREADY_ACTIVE, got %v", err)
		}
	})

	t.Run("rejects invalid share type", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		_, err := svc.Start(user.ID, space.ID, idgen.NextString(), StartRequest{ShareType: "browser_tab"})
		if err == nil {
			t.Fatal("expected error for invalid share type")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SCREEN_SOURCE_INVALID {
			t.Errorf("expected SCREEN_SOURCE_INVALID, got %v", err)
		}
	})
}

func TestStopByChannelV2(t *testing.T) {
	t.Run("sets ended fields", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		if err := svc.StopByChannel(user.ID, channelID); err != nil {
			t.Fatalf("StopByChannel failed: %v", err)
		}

		var session model.ScreenShareSession
		if err := db.Where("channel_id = ?", channelID).First(&session).Error; err != nil {
			t.Fatalf("expected session to exist: %v", err)
		}
		if session.Active {
			t.Error("expected active=false after stop")
		}
		if session.Status != "ended" {
			t.Errorf("expected status 'ended', got '%s'", session.Status)
		}
		if session.EndedAt == nil {
			t.Error("expected ended_at to be set")
		}
		if session.EndedReason != "user_stopped" {
			t.Errorf("expected ended_reason 'user_stopped', got '%s'", session.EndedReason)
		}
	})
}

func TestPauseAndResumeByChannelV2(t *testing.T) {
	t.Run("pauses and resumes active session", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		if err := svc.PauseByChannel(user.ID, channelID); err != nil {
			t.Fatalf("PauseByChannel failed: %v", err)
		}

		var session model.ScreenShareSession
		if err := db.Where("channel_id = ?", channelID).First(&session).Error; err != nil {
			t.Fatalf("expected session: %v", err)
		}
		if session.Status != "paused" {
			t.Errorf("expected status 'paused', got '%s'", session.Status)
		}
		if !session.Active {
			t.Error("expected active to remain true when paused")
		}

		if err := svc.ResumeByChannel(user.ID, channelID); err != nil {
			t.Fatalf("ResumeByChannel failed: %v", err)
		}
		if err := db.Where("channel_id = ?", channelID).First(&session).Error; err != nil {
			t.Fatalf("expected session: %v", err)
		}
		if session.Status != "active" {
			t.Errorf("expected status 'active', got '%s'", session.Status)
		}
	})
}

func TestForceStopByChannelV2(t *testing.T) {
	t.Run("force-stops active session and sets force_stopped status", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		// Make the sharer a space admin so they can force-stop their own session in tests.
		if err := db.Model(&model.Membership{}).
			Where("space_id = ? AND user_id = ?", space.ID, user.ID).
			Update("role", middleware.RoleAdmin).Error; err != nil {
			t.Fatalf("promote sharer to admin: %v", err)
		}

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		if err := svc.ForceStopByChannel(user.ID, channelID); err != nil {
			t.Fatalf("ForceStopByChannel failed: %v", err)
		}

		var session model.ScreenShareSession
		if err := db.Where("channel_id = ?", channelID).First(&session).Error; err != nil {
			t.Fatalf("expected session to exist: %v", err)
		}
		if session.Active {
			t.Error("expected active=false after force stop")
		}
		if session.Status != "force_stopped" {
			t.Errorf("expected status 'force_stopped', got '%s'", session.Status)
		}
		if session.EndedAt == nil {
			t.Error("expected ended_at to be set")
		}
		if session.EndedReason != "force_stopped_by_admin" {
			t.Errorf("expected ended_reason 'force_stopped_by_admin', got '%s'", session.EndedReason)
		}
	})

	t.Run("force stop kicks main and share virtual identities", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		if err := db.Model(&model.Membership{}).
			Where("space_id = ? AND user_id = ?", space.ID, user.ID).
			Update("role", middleware.RoleAdmin).Error; err != nil {
			t.Fatalf("promote sharer to admin: %v", err)
		}

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		var kicks [][2]string
		svc.kickParticipants = func(livekitRoom, userID string) {
			kicks = append(kicks, [2]string{livekitRoom, userID})
		}

		if err := svc.ForceStopByChannel(user.ID, channelID); err != nil {
			t.Fatalf("ForceStopByChannel failed: %v", err)
		}

		if len(kicks) != 1 {
			t.Fatalf("expected 1 kick call, got %d", len(kicks))
		}
		if kicks[0][0] != "rrt-room-"+channelID {
			t.Errorf("expected livekit room %q, got %q", "rrt-room-"+channelID, kicks[0][0])
		}
		if kicks[0][1] != user.ID {
			t.Errorf("expected kick target %q, got %q", user.ID, kicks[0][1])
		}
	})

	t.Run("user stop kicks only share virtual identity (main voice stays)", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		// 主动停止只应踢 :share 虚身份，主身份（仍在语音通话）必须保留。
		var removed [][2]string
		svc.kickVirtualParticipant = func(livekitRoom, userID string) {
			removed = append(removed, [2]string{livekitRoom, userID})
		}

		if err := svc.StopByChannel(user.ID, channelID); err != nil {
			t.Fatalf("StopByChannel failed: %v", err)
		}
		if len(removed) != 1 {
			t.Fatalf("expected 1 remove (virtual identity), got %d: %v", len(removed), removed)
		}
		if removed[0][0] != "rrt-room-"+channelID {
			t.Errorf("expected livekit room %q, got %q", "rrt-room-"+channelID, removed[0][0])
		}
		if removed[0][1] != user.ID {
			t.Errorf("expected virtual identity for %q removed, got %q", user.ID, removed[0][1])
		}
	})

	t.Run("nil roomClient is safe", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		if err := db.Model(&model.Membership{}).
			Where("space_id = ? AND user_id = ?", space.ID, user.ID).
			Update("role", middleware.RoleAdmin).Error; err != nil {
			t.Fatalf("promote sharer to admin: %v", err)
		}

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		// DefaultConfig 未配置 LiveKit → roomClient=nil，踢出必须安全跳过。
		if err := svc.ForceStopByChannel(user.ID, channelID); err != nil {
			t.Fatalf("ForceStopByChannel failed with nil roomClient: %v", err)
		}
	})

	t.Run("non-admin cannot force-stop", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		channelID := idgen.NextString()
		if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		nonAdminID := idgen.NextString()
		err := svc.ForceStopByChannel(nonAdminID, channelID)
		if err == nil {
			t.Fatal("expected error for non-admin force stop")
		}
	})
}

func TestGetActiveSessionsBySpaceV2(t *testing.T) {
	t.Run("returns active sessions for a space", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		if _, err := svc.Start(user.ID, space.ID, idgen.NextString(), StartRequest{ShareType: "screen"}); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		sessions, err := svc.GetActiveSessionsBySpace(space.ID)
		if err != nil {
			t.Fatalf("GetActiveSessionsBySpace failed: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("expected 1 active session, got %d", len(sessions))
		}
		if sessions[0].SpaceID != space.ID {
			t.Errorf("expected spaceID %s, got %s", space.ID, sessions[0].SpaceID)
		}
	})
}
