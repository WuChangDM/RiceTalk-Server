package screenshare

import (
	"testing"
	"time"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

// TestStartV2ErrorPaths covers the error branches of Service.Start.
func TestStartV2ErrorPaths(t *testing.T) {
	t.Run("rejects empty channel id", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		_, err := svc.Start(user.ID, space.ID, "", StartRequest{ShareType: "screen"})
		if err == nil {
			t.Fatal("expected error for empty channel id")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SYSTEM_BAD_REQUEST {
			t.Errorf("expected SYSTEM_BAD_REQUEST, got %v", err)
		}
	})

	t.Run("rejects window share without source id", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		_, err := svc.Start(user.ID, space.ID, idgen.NextString(), StartRequest{ShareType: "window"})
		if err == nil {
			t.Fatal("expected error for window share without source id")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SCREEN_SOURCE_INVALID {
			t.Errorf("expected SCREEN_SOURCE_INVALID, got %v", err)
		}
	})

	t.Run("rejects application share without source id", func(t *testing.T) {
		user, space, db := setupServiceTestSpace(t)
		cfg := config.DefaultConfig()
		svc := NewService(db, cfg)

		_, err := svc.Start(user.ID, space.ID, idgen.NextString(), StartRequest{ShareType: "application"})
		if err == nil {
			t.Fatal("expected error for application share without source id")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SCREEN_SOURCE_INVALID {
			t.Errorf("expected SCREEN_SOURCE_INVALID, got %v", err)
		}
	})
}

// TestStopByChannelForbidden covers StopByChannel when the caller is not the
// sharer.
func TestStopByChannelForbidden(t *testing.T) {
	user, space, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	channelID := idgen.NextString()
	if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	otherUserID := idgen.NextString()
	err := svc.StopByChannel(otherUserID, channelID)
	if err == nil {
		t.Fatal("expected error for non-sharer stop")
	}
	if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.AUTH_FORBIDDEN {
		t.Errorf("expected AUTH_FORBIDDEN, got %v", err)
	}
}

// TestStopByChannelNotFound covers StopByChannel when no active session exists.
func TestStopByChannelNotFound(t *testing.T) {
	user, _, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	err := svc.StopByChannel(user.ID, idgen.NextString())
	if err == nil {
		t.Fatal("expected error for stop on channel without active session")
	}
	if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SYSTEM_NOT_FOUND {
		t.Errorf("expected SYSTEM_NOT_FOUND, got %v", err)
	}
}

// TestPauseByChannelForbidden covers PauseByChannel when the caller is not the
// sharer.
func TestPauseByChannelForbidden(t *testing.T) {
	user, space, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	channelID := idgen.NextString()
	if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	otherUserID := idgen.NextString()
	err := svc.PauseByChannel(otherUserID, channelID)
	if err == nil {
		t.Fatal("expected error for non-sharer pause")
	}
	if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.AUTH_FORBIDDEN {
		t.Errorf("expected AUTH_FORBIDDEN, got %v", err)
	}
}

// TestResumeByChannelForbidden covers ResumeByChannel when the caller is not
// the sharer.
func TestResumeByChannelForbidden(t *testing.T) {
	user, space, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	channelID := idgen.NextString()
	if _, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	otherUserID := idgen.NextString()
	err := svc.ResumeByChannel(otherUserID, channelID)
	if err == nil {
		t.Fatal("expected error for non-sharer resume")
	}
	if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.AUTH_FORBIDDEN {
		t.Errorf("expected AUTH_FORBIDDEN, got %v", err)
	}
}

// TestGetSessionByIDNotFound covers GetSessionByID for a non-existent session.
func TestGetSessionByIDNotFound(t *testing.T) {
	_, _, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	_, err := svc.GetSessionByID("nonexistent-session-id")
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
	if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SYSTEM_NOT_FOUND {
		t.Errorf("expected SYSTEM_NOT_FOUND, got %v", err)
	}
}

// TestGetStatusNotFound covers GetStatus for a non-existent session.
func TestGetStatusNotFound(t *testing.T) {
	_, _, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	_, err := svc.GetStatus("nonexistent-session-id")
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
	if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SYSTEM_NOT_FOUND {
		t.Errorf("expected SYSTEM_NOT_FOUND, got %v", err)
	}
}

// TestGetActiveSessionByChannelEmptyID covers GetActiveSessionByChannel with an
// empty channel id (returns ErrNotFound directly).
func TestGetActiveSessionByChannelEmptyID(t *testing.T) {
	_, _, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	_, err := svc.GetActiveSessionByChannel("")
	if err == nil {
		t.Fatal("expected error for empty channel id")
	}
	if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.SYSTEM_NOT_FOUND {
		t.Errorf("expected SYSTEM_NOT_FOUND, got %v", err)
	}
}

// TestGetStatusByChannelInactive covers GetStatusByChannel when no active
// session exists — it should return an inactive state, not an error.
func TestGetStatusByChannelInactive(t *testing.T) {
	_, _, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	channelID := idgen.NextString()
	status, err := svc.GetStatusByChannel(channelID)
	if err != nil {
		t.Fatalf("expected no error for inactive channel, got %v", err)
	}
	if status.Active {
		t.Error("expected active=false for inactive channel")
	}
	if status.ChannelID != channelID {
		t.Errorf("expected channelID %s, got %s", channelID, status.ChannelID)
	}
}

// TestIsUserInSpace covers IsUserInSpace for both member and non-member.
func TestIsUserInSpace(t *testing.T) {
	user, space, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	inSpace, err := svc.IsUserInSpace(user.ID, space.ID)
	if err != nil {
		t.Fatalf("IsUserInSpace failed: %v", err)
	}
	if !inSpace {
		t.Error("expected user to be in space")
	}

	otherUserID := idgen.NextString()
	inSpace, err = svc.IsUserInSpace(otherUserID, space.ID)
	if err != nil {
		t.Fatalf("IsUserInSpace failed: %v", err)
	}
	if inSpace {
		t.Error("expected other user to not be in space")
	}
}

// TestIsUserSpaceAdmin covers IsUserSpaceAdmin for member, admin and non-member.
func TestIsUserSpaceAdmin(t *testing.T) {
	user, space, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	// Initially the user is a MEMBER, not an admin.
	isAdmin, err := svc.IsUserSpaceAdmin(user.ID, space.ID)
	if err != nil {
		t.Fatalf("IsUserSpaceAdmin failed: %v", err)
	}
	if isAdmin {
		t.Error("expected member to not be admin")
	}

	// Promote to ADMIN.
	if err := db.Model(&model.Membership{}).
		Where("space_id = ? AND user_id = ?", space.ID, user.ID).
		Update("role", middleware.RoleAdmin).Error; err != nil {
		t.Fatalf("promote to admin: %v", err)
	}
	isAdmin, err = svc.IsUserSpaceAdmin(user.ID, space.ID)
	if err != nil {
		t.Fatalf("IsUserSpaceAdmin failed: %v", err)
	}
	if !isAdmin {
		t.Error("expected admin to be admin")
	}

	// Non-member returns false without error.
	otherUserID := idgen.NextString()
	isAdmin, err = svc.IsUserSpaceAdmin(otherUserID, space.ID)
	if err != nil {
		t.Fatalf("IsUserSpaceAdmin failed: %v", err)
	}
	if isAdmin {
		t.Error("expected non-member to not be admin")
	}
}

// TestCanUserManageSession covers CanUserManageSession branches.
func TestCanUserManageSession(t *testing.T) {
	user, space, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	channelID := idgen.NextString()
	sessionID, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	session, err := svc.GetSessionByID(sessionID)
	if err != nil {
		t.Fatalf("GetSessionByID failed: %v", err)
	}

	// Sharer can always manage their own session.
	canManage, err := svc.CanUserManageSession(user.ID, false, session)
	if err != nil {
		t.Fatalf("CanUserManageSession failed: %v", err)
	}
	if !canManage {
		t.Error("expected sharer to manage own session")
	}

	// Another member of the same space can manage when policy is "all".
	otherUser := &model.User{
		ID: idgen.NextString(), Username: "viewer", Email: "v@e.com",
		PasswordHash: "x", Role: middleware.RoleMember, IsActive: true,
	}
	if err := db.Create(otherUser).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	if err := db.Create(&model.Membership{
		ID: idgen.NextString(), UserID: otherUser.ID, SpaceID: space.ID,
		Role: middleware.RoleMember, JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	canManage, err = svc.CanUserManageSession(otherUser.ID, false, session)
	if err != nil {
		t.Fatalf("CanUserManageSession failed: %v", err)
	}
	if !canManage {
		t.Error("expected member to manage session with policy=all")
	}

	// With admin_only policy, non-admin members cannot manage.
	session.ViewerPolicy = "admin_only"
	canManage, err = svc.CanUserManageSession(otherUser.ID, false, session)
	if err != nil {
		t.Fatalf("CanUserManageSession failed: %v", err)
	}
	if canManage {
		t.Error("expected non-admin to not manage session with policy=admin_only")
	}

	// Global admin can manage admin_only sessions.
	canManage, err = svc.CanUserManageSession(otherUser.ID, true, session)
	if err != nil {
		t.Fatalf("CanUserManageSession failed: %v", err)
	}
	if !canManage {
		t.Error("expected global admin to manage admin_only session")
	}

	// Promote otherUser to space admin — should be able to manage.
	if err := db.Model(&model.Membership{}).
		Where("space_id = ? AND user_id = ?", space.ID, otherUser.ID).
		Update("role", middleware.RoleAdmin).Error; err != nil {
		t.Fatalf("promote to admin: %v", err)
	}
	canManage, err = svc.CanUserManageSession(otherUser.ID, false, session)
	if err != nil {
		t.Fatalf("CanUserManageSession failed: %v", err)
	}
	if !canManage {
		t.Error("expected space admin to manage admin_only session")
	}
}

// TestCanUserManageSessionNotInSpace covers the case where the user is not a
// member of the session's space.
func TestCanUserManageSessionNotInSpace(t *testing.T) {
	user, space, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	channelID := idgen.NextString()
	sessionID, err := svc.Start(user.ID, space.ID, channelID, StartRequest{ShareType: "screen"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	session, err := svc.GetSessionByID(sessionID)
	if err != nil {
		t.Fatalf("GetSessionByID failed: %v", err)
	}

	otherUserID := idgen.NextString()
	canManage, err := svc.CanUserManageSession(otherUserID, false, session)
	if err != nil {
		t.Fatalf("CanUserManageSession failed: %v", err)
	}
	if canManage {
		t.Error("expected non-member to not manage session")
	}
}

// TestCountViewers covers the countViewers helper.
func TestCountViewers(t *testing.T) {
	user, space, db := setupServiceTestSpace(t)
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg)

	// Empty channelID returns 0.
	if count := svc.countViewers("", user.ID); count != 0 {
		t.Errorf("expected 0 viewers for empty channel, got %d", count)
	}

	// Add a couple of voice participants (excluding the sharer).
	channelID := idgen.NextString()
	viewer1 := &model.VoiceParticipant{
		ID: idgen.NextString(), RoomID: channelID, UserID: "viewer-1",
		JoinedAt: time.Now().UTC(),
	}
	viewer2 := &model.VoiceParticipant{
		ID: idgen.NextString(), RoomID: channelID, UserID: "viewer-2",
		JoinedAt: time.Now().UTC(),
	}
	if err := db.Create(viewer1).Error; err != nil {
		t.Fatalf("create viewer1: %v", err)
	}
	if err := db.Create(viewer2).Error; err != nil {
		t.Fatalf("create viewer2: %v", err)
	}

	// Excluded user (the sharer) — should not be counted.
	if err := db.Create(&model.VoiceParticipant{
		ID: idgen.NextString(), RoomID: channelID, UserID: user.ID,
		JoinedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("create sharer participant: %v", err)
	}

	// Left participant — should not be counted.
	if err := db.Create(&model.VoiceParticipant{
		ID: idgen.NextString(), RoomID: channelID, UserID: "viewer-3",
		JoinedAt: time.Now().UTC(), LeftAt: &time.Time{},
	}).Error; err != nil {
		t.Fatalf("create left participant: %v", err)
	}

	if count := svc.countViewers(channelID, user.ID); count != 2 {
		t.Errorf("expected 2 viewers, got %d", count)
	}
	_ = space
}
