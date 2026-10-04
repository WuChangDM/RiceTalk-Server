package remoteassist

// A6-S1 / A7-S1（DES-20261001-01 §7.2 / §8.1）校验器单元测试：
//   - ValidateFromTarget：剪贴板回传（remote_assist_clipboard_data）的门——
//     sender 必须是会话 target，返回 requester 供路由；
//   - ValidateChatParticipant：会话内聊天（remote_assist_chat）的门——
//     sender ∈ {requester, target}，返回对端供路由。
//
// 两者与 ValidateControlEvent 共享同一状态闸门（仅 authorized）与 M10 惰性
// 时限判定，会话终止后所有剪贴板/聊天消息自然失效。复用 service_test.go 的
// setupTestSpace / newTestService。

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/model"
)

// authorizedSession 建一个已授权会话；age>0 时把 updated_at 回拨（绕过
// GORM 自动时间戳），模拟 M10 时间流逝。
func authorizedSession(t *testing.T, age time.Duration) (svc *Service, sessionID, requesterID, targetID string, db *gorm.DB) {
	t.Helper()
	requester, target, _, channel, db := setupTestSpace(t)
	svc = newTestService(db)
	session, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{Mouse: true})
	if err != nil {
		t.Fatalf("CreateRequest failed: %v", err)
	}
	if _, err := svc.Authorize(session.ID, target.ID); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}
	if age > 0 {
		if err := db.Model(&model.RemoteAssistSession{}).
			Where("id = ?", session.ID).
			UpdateColumn("updated_at", time.Now().UTC().Add(-age)).Error; err != nil {
			t.Fatalf("backdate updated_at: %v", err)
		}
	}
	return svc, session.ID, requester.ID, target.ID, db
}

func requireCode(t *testing.T, err error, want errors.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	appErr, ok := err.(*errors.AppError)
	if !ok || appErr.Code != want {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

// TestValidateFromTarget 覆盖 A6-S1：合法 target 放行并返回 requester /
// requester（非 target）拒绝 / 第三者拒绝 / 未知会话 / pending / ended /
// M10 超限（惰性判定强制结束并拒绝）/ 空参数。
func TestValidateFromTarget(t *testing.T) {
	t.Run("session target passes and receives the requester id", func(t *testing.T) {
		svc, sessionID, requesterID, targetID, _ := authorizedSession(t, 0)
		defer svc.cancelTimeout(sessionID)

		got, err := svc.ValidateFromTarget(sessionID, targetID)
		if err != nil {
			t.Fatalf("target data reply must pass, got %v", err)
		}
		if got != requesterID {
			t.Errorf("returned id = %s, want requester %s", got, requesterID)
		}
	})

	t.Run("requester is not the target and is rejected", func(t *testing.T) {
		svc, sessionID, requesterID, _, _ := authorizedSession(t, 0)
		defer svc.cancelTimeout(sessionID)

		_, err := svc.ValidateFromTarget(sessionID, requesterID)
		requireCode(t, err, errors.REMOTE_ASSIST_PERMISSION_DENIED)
	})

	t.Run("third party is rejected", func(t *testing.T) {
		svc, sessionID, _, _, _ := authorizedSession(t, 0)
		defer svc.cancelTimeout(sessionID)

		_, err := svc.ValidateFromTarget(sessionID, "user-attacker")
		requireCode(t, err, errors.REMOTE_ASSIST_PERMISSION_DENIED)
	})

	t.Run("unknown session is rejected", func(t *testing.T) {
		svc, _, _, targetID, _ := authorizedSession(t, 0)

		_, err := svc.ValidateFromTarget("ra_does_not_exist", targetID)
		requireCode(t, err, errors.REMOTE_ASSIST_NOT_FOUND)
	})

	t.Run("pending session is rejected", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if err != nil {
			t.Fatalf("CreateRequest failed: %v", err)
		}
		defer svc.cancelTimeout(session.ID)

		_, err = svc.ValidateFromTarget(session.ID, target.ID)
		requireCode(t, err, errors.REMOTE_ASSIST_INVALID_STATE)
	})

	t.Run("ended session is rejected", func(t *testing.T) {
		svc, sessionID, _, targetID, _ := authorizedSession(t, 0)

		if _, err := svc.End(sessionID, targetID); err != nil {
			t.Fatalf("End failed: %v", err)
		}
		_, err := svc.ValidateFromTarget(sessionID, targetID)
		requireCode(t, err, errors.REMOTE_ASSIST_INVALID_STATE)
	})

	t.Run("over-limit session is force-ended and rejected (M10)", func(t *testing.T) {
		svc, sessionID, requesterID, targetID, db := authorizedSession(t, MaxSessionDuration+time.Minute)

		_, err := svc.ValidateFromTarget(sessionID, targetID)
		requireCode(t, err, errors.REMOTE_ASSIST_EXPIRED)

		// 惰性判定同时把会话强制结束（后续 requester 侧控制事件同判死）。
		var stored model.RemoteAssistSession
		if err := db.First(&stored, "id = ?", sessionID).Error; err != nil {
			t.Fatalf("session not found: %v", err)
		}
		if stored.Status != StatusEnded {
			t.Errorf("expected force-ended status, got %q", stored.Status)
		}
		err = svc.ValidateControlEvent(sessionID, requesterID, targetID)
		if err == nil {
			t.Error("control events must be dead after the lazy expiry too")
		}
	})

	t.Run("empty arguments are rejected", func(t *testing.T) {
		svc, sessionID, _, targetID, _ := authorizedSession(t, 0)
		defer svc.cancelTimeout(sessionID)

		if _, err := svc.ValidateFromTarget("", targetID); err == nil {
			t.Error("empty sessionId must be rejected")
		}
		if _, err := svc.ValidateFromTarget(sessionID, ""); err == nil {
			t.Error("empty sender must be rejected")
		}
	})
}

// TestValidateChatParticipant 覆盖 A7-S1：双方互为对端 / 第三者拒绝 /
// pending / ended / M10 超限 / 未知会话。
func TestValidateChatParticipant(t *testing.T) {
	t.Run("requester passes and peer is the target", func(t *testing.T) {
		svc, sessionID, requesterID, targetID, _ := authorizedSession(t, 0)
		defer svc.cancelTimeout(sessionID)

		got, err := svc.ValidateChatParticipant(sessionID, requesterID)
		if err != nil {
			t.Fatalf("requester chat must pass, got %v", err)
		}
		if got != targetID {
			t.Errorf("peer = %s, want target %s", got, targetID)
		}
	})

	t.Run("target passes and peer is the requester", func(t *testing.T) {
		svc, sessionID, requesterID, targetID, _ := authorizedSession(t, 0)
		defer svc.cancelTimeout(sessionID)

		got, err := svc.ValidateChatParticipant(sessionID, targetID)
		if err != nil {
			t.Fatalf("target chat must pass, got %v", err)
		}
		if got != requesterID {
			t.Errorf("peer = %s, want requester %s", got, requesterID)
		}
	})

	t.Run("third party is rejected", func(t *testing.T) {
		svc, sessionID, _, _, _ := authorizedSession(t, 0)
		defer svc.cancelTimeout(sessionID)

		_, err := svc.ValidateChatParticipant(sessionID, "user-attacker")
		requireCode(t, err, errors.REMOTE_ASSIST_PERMISSION_DENIED)
	})

	t.Run("pending session is rejected", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if err != nil {
			t.Fatalf("CreateRequest failed: %v", err)
		}
		defer svc.cancelTimeout(session.ID)

		_, err = svc.ValidateChatParticipant(session.ID, requester.ID)
		requireCode(t, err, errors.REMOTE_ASSIST_INVALID_STATE)
	})

	t.Run("ended session is rejected", func(t *testing.T) {
		svc, sessionID, requesterID, _, _ := authorizedSession(t, 0)

		if _, err := svc.End(sessionID, requesterID); err != nil {
			t.Fatalf("End failed: %v", err)
		}
		_, err := svc.ValidateChatParticipant(sessionID, requesterID)
		requireCode(t, err, errors.REMOTE_ASSIST_INVALID_STATE)
	})

	t.Run("over-limit session is force-ended and rejected (M10)", func(t *testing.T) {
		svc, sessionID, _, targetID, _ := authorizedSession(t, MaxSessionDuration+time.Minute)

		_, err := svc.ValidateChatParticipant(sessionID, targetID)
		requireCode(t, err, errors.REMOTE_ASSIST_EXPIRED)
	})

	t.Run("unknown session is rejected", func(t *testing.T) {
		svc, _, requesterID, _, _ := authorizedSession(t, 0)

		_, err := svc.ValidateChatParticipant("ra_does_not_exist", requesterID)
		requireCode(t, err, errors.REMOTE_ASSIST_NOT_FOUND)
	})

	t.Run("empty arguments are rejected", func(t *testing.T) {
		svc, sessionID, requesterID, _, _ := authorizedSession(t, 0)
		defer svc.cancelTimeout(sessionID)

		if _, err := svc.ValidateChatParticipant("", requesterID); err == nil {
			t.Error("empty sessionId must be rejected")
		}
		if _, err := svc.ValidateChatParticipant(sessionID, ""); err == nil {
			t.Error("empty sender must be rejected")
		}
	})
}
