package remoteassist

import (
	"testing"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

// FIX-20261003-01 NJ-16：远程协助创建请求的 channelId 改为可选。
// 此前无频道上下文发起必然命中 CHANNEL_NOT_FOUND（「频道不存在或已被删除」），
// 错误语义错位误导用户。channelId 为空时改按「双方共同空间」校验。

func TestCreateRequestWithoutChannel(t *testing.T) {
	t.Run("succeeds with empty channelId when pair shares a space", func(t *testing.T) {
		requester, target, _, _, db := setupTestSpace(t)
		svc := newTestService(db)

		session, err := svc.CreateRequest(requester.ID, target.ID, "", Permissions{Mouse: true})
		if err != nil {
			t.Fatalf("CreateRequest without channel failed: %v", err)
		}
		if session.ChannelID != "" {
			t.Errorf("expected empty ChannelID, got %q", session.ChannelID)
		}
		if session.Status != StatusPending {
			t.Errorf("expected status pending, got %q", session.Status)
		}
		if session.TargetID != target.ID || session.RequesterID != requester.ID {
			t.Errorf("unexpected session participants: requester=%q target=%q", session.RequesterID, session.TargetID)
		}
	})

	t.Run("forbidden when pair shares no space", func(t *testing.T) {
		requester, _, _, _, db := setupTestSpace(t)
		svc := newTestService(db)

		// outsider 用户不属于 requester 所在空间
		passwordHash, _ := crypto.HashPassword("Password123!")
		outsider := &model.User{
			ID:           idgen.NextString(),
			Username:     "outsider",
			Email:        "outsider@example.com",
			PasswordHash: passwordHash,
			Role:         middleware.RoleMember,
			IsActive:     true,
		}
		if err := db.Create(outsider).Error; err != nil {
			t.Fatalf("create outsider: %v", err)
		}

		_, err := svc.CreateRequest(requester.ID, outsider.ID, "", Permissions{})
		appErr, ok := err.(*errors.AppError)
		if !ok || appErr.Code != errors.REMOTE_ASSIST_FORBIDDEN {
			t.Fatalf("expected REMOTE_ASSIST_FORBIDDEN, got %v", err)
		}
	})

	t.Run("unknown target still yields USER_NOT_FOUND", func(t *testing.T) {
		requester, _, _, _, db := setupTestSpace(t)
		svc := newTestService(db)

		_, err := svc.CreateRequest(requester.ID, "user_missing", "", Permissions{})
		appErr, ok := err.(*errors.AppError)
		if !ok || appErr.Code != errors.USER_NOT_FOUND {
			t.Fatalf("expected USER_NOT_FOUND, got %v", err)
		}
	})

	t.Run("empty requester still rejected as bad request", func(t *testing.T) {
		_, target, _, _, db := setupTestSpace(t)
		svc := newTestService(db)

		_, err := svc.CreateRequest("", target.ID, "", Permissions{})
		appErr, ok := err.(*errors.AppError)
		if !ok || appErr.Code != errors.SYSTEM_BAD_REQUEST {
			t.Fatalf("expected SYSTEM_BAD_REQUEST, got %v", err)
		}
	})

	t.Run("common-space lookup reads memberships from db", func(t *testing.T) {
		// 回归护栏：共同空间判定必须读库（Membership 表），不能依赖内存态
		requester, target, space, _, db := setupTestSpace(t)
		svc := newTestService(db)

		in, err := svc.IsUserInSpace(target.ID, space.ID)
		if err != nil || !in {
			t.Fatalf("target membership missing: in=%v err=%v", in, err)
		}
		common, err := svc.findCommonSpace(requester.ID, target.ID)
		if err != nil {
			t.Fatalf("findCommonSpace failed: %v", err)
		}
		if common != space.ID {
			t.Fatalf("expected common space %q, got %q", space.ID, common)
		}
	})
}
