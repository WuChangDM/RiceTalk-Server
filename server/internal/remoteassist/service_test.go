package remoteassist

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

// setupTestSpace creates two users (requester + target), a space, a channel
// and memberships for both users. Returns the entities ready for use in tests.
func setupTestSpace(t *testing.T) (requester, target *model.User, space *model.Space, channel *model.Channel, db *gorm.DB) {
	t.Helper()
	db = testutil.MustSetupTestDB()

	passwordHash, _ := crypto.HashPassword("Password123!")
	requester = &model.User{
		ID:           idgen.NextString(),
		Username:     "requester",
		Email:        "requester@example.com",
		PasswordHash: passwordHash,
		Role:         middleware.RoleMember,
		IsActive:     true,
	}
	if err := db.Create(requester).Error; err != nil {
		t.Fatalf("create requester: %v", err)
	}

	target = &model.User{
		ID:           idgen.NextString(),
		Username:     "target",
		Email:        "target@example.com",
		PasswordHash: passwordHash,
		Role:         middleware.RoleMember,
		IsActive:     true,
	}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	space = &model.Space{
		ID:      idgen.NextString(),
		Name:    "Test Space",
		OwnerID: requester.ID,
	}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}

	for _, u := range []*model.User{requester, target} {
		m := &model.Membership{
			ID:       idgen.NextString(),
			UserID:   u.ID,
			SpaceID:  space.ID,
			Role:     middleware.RoleMember,
			JoinedAt: time.Now().UTC(),
		}
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("create membership: %v", err)
		}
	}

	channel = &model.Channel{
		ID:      idgen.NextString(),
		SpaceID: space.ID,
		Name:    "voice-1",
		Type:    "voice",
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	return requester, target, space, channel, db
}

func newTestService(db *gorm.DB) *Service {
	hub := realtime.NewHub(testutil.TestLogger())
	return NewService(db, hub, nil)
}

func TestCreateRequest(t *testing.T) {
	t.Run("creates pending session with 30s expiry", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{Mouse: true, Keyboard: false})
		if err != nil {
			t.Fatalf("CreateRequest failed: %v", err)
		}

		if session.ID == "" {
			t.Error("expected non-empty session ID")
		}
		if session.Status != StatusPending {
			t.Errorf("expected status %q, got %q", StatusPending, session.Status)
		}
		if session.RequesterID != requester.ID {
			t.Errorf("expected requesterID %s, got %s", requester.ID, session.RequesterID)
		}
		if session.TargetID != target.ID {
			t.Errorf("expected targetID %s, got %s", target.ID, session.TargetID)
		}
		if !session.Permissions.Mouse || session.Permissions.Keyboard {
			t.Errorf("unexpected permissions: %+v", session.Permissions)
		}
		// Expiry should be ~30s in the future.
		got := time.Until(session.ExpiresAt)
		if got < 25*time.Second || got > 35*time.Second {
			t.Errorf("expected expiry ~30s, got %v", got)
		}

		// Verify the persisted record matches.
		var stored model.RemoteAssistSession
		if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
			t.Fatalf("session not persisted: %v", err)
		}
		if stored.Status != StatusPending {
			t.Errorf("expected persisted status %q, got %q", StatusPending, stored.Status)
		}

		svc.cancelTimeout(session.ID)
	})

	t.Run("rejects self request", func(t *testing.T) {
		requester, _, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		_, err := svc.CreateRequest(requester.ID, requester.ID, channel.ID, Permissions{})
		if err == nil {
			t.Fatal("expected error for self request")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_INVALID_STATE {
			t.Errorf("expected REMOTE_ASSIST_INVALID_STATE, got %v", err)
		}
	})

	t.Run("rejects when requester is not a space member", func(t *testing.T) {
		_, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		// A user that is not a member of the space.
		outsider := &model.User{
			ID:           idgen.NextString(),
			Username:     "outsider",
			Email:        "outsider@example.com",
			PasswordHash: "x",
			Role:         middleware.RoleMember,
			IsActive:     true,
		}
		if err := db.Create(outsider).Error; err != nil {
			t.Fatalf("create outsider: %v", err)
		}

		_, err := svc.CreateRequest(outsider.ID, target.ID, channel.ID, Permissions{})
		if err == nil {
			t.Fatal("expected error for non-member requester")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_FORBIDDEN {
			t.Errorf("expected REMOTE_ASSIST_FORBIDDEN, got %v", err)
		}
	})

	t.Run("rejects duplicate active session", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		if _, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{}); err != nil {
			t.Fatalf("first CreateRequest failed: %v", err)
		}
		_, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if err == nil {
			t.Fatal("expected error for duplicate request")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_ALREADY_ACTIVE {
			t.Errorf("expected REMOTE_ASSIST_ALREADY_ACTIVE, got %v", err)
		}
	})
}

func TestAuthorize(t *testing.T) {
	t.Run("target authorizes pending session", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{Mouse: true})
		updated, err := svc.Authorize(session.ID, target.ID)
		if err != nil {
			t.Fatalf("Authorize failed: %v", err)
		}
		if updated.Status != StatusAuthorized {
			t.Errorf("expected status %q, got %q", StatusAuthorized, updated.Status)
		}
	})

	t.Run("non-target cannot authorize", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		_, err := svc.Authorize(session.ID, requester.ID)
		if err == nil {
			t.Fatal("expected error when requester tries to authorize")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_PERMISSION_DENIED {
			t.Errorf("expected REMOTE_ASSIST_PERMISSION_DENIED, got %v", err)
		}
	})

	t.Run("cannot authorize non-pending session", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if _, err := svc.Authorize(session.ID, target.ID); err != nil {
			t.Fatalf("first Authorize failed: %v", err)
		}
		// Second authorize should fail.
		_, err := svc.Authorize(session.ID, target.ID)
		if err == nil {
			t.Fatal("expected error when authorizing already-authorized session")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_INVALID_STATE {
			t.Errorf("expected REMOTE_ASSIST_INVALID_STATE, got %v", err)
		}
	})
}

func TestReject(t *testing.T) {
	t.Run("target rejects pending session", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		updated, err := svc.Reject(session.ID, target.ID)
		if err != nil {
			t.Fatalf("Reject failed: %v", err)
		}
		if updated.Status != StatusRejected {
			t.Errorf("expected status %q, got %q", StatusRejected, updated.Status)
		}
	})

	t.Run("non-target cannot reject", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		_, err := svc.Reject(session.ID, requester.ID)
		if err == nil {
			t.Fatal("expected error when requester tries to reject")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_PERMISSION_DENIED {
			t.Errorf("expected REMOTE_ASSIST_PERMISSION_DENIED, got %v", err)
		}
	})
}

func TestEnd(t *testing.T) {
	t.Run("requester can end authorized session", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if _, err := svc.Authorize(session.ID, target.ID); err != nil {
			t.Fatalf("Authorize failed: %v", err)
		}
		updated, err := svc.End(session.ID, requester.ID)
		if err != nil {
			t.Fatalf("End failed: %v", err)
		}
		if updated.Status != StatusEnded {
			t.Errorf("expected status %q, got %q", StatusEnded, updated.Status)
		}
	})

	t.Run("target can end authorized session", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if _, err := svc.Authorize(session.ID, target.ID); err != nil {
			t.Fatalf("Authorize failed: %v", err)
		}
		if _, err := svc.End(session.ID, target.ID); err != nil {
			t.Fatalf("End failed: %v", err)
		}
	})

	t.Run("unrelated user cannot end", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if _, err := svc.Authorize(session.ID, target.ID); err != nil {
			t.Fatalf("Authorize failed: %v", err)
		}
		_, err := svc.End(session.ID, "user-unrelated")
		if err == nil {
			t.Fatal("expected error when unrelated user ends session")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_PERMISSION_DENIED {
			t.Errorf("expected REMOTE_ASSIST_PERMISSION_DENIED, got %v", err)
		}
	})
}

func TestListActiveSessions(t *testing.T) {
	t.Run("returns pending and authorized sessions for the user", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		s1, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		s2, _ := svc.CreateRequest(target.ID, requester.ID, channel.ID, Permissions{})
		// Reject the second one so it should not appear in active list.
		if _, err := svc.Reject(s2.ID, requester.ID); err != nil {
			t.Fatalf("Reject failed: %v", err)
		}

		got, err := svc.ListActiveSessions(requester.ID)
		if err != nil {
			t.Fatalf("ListActiveSessions failed: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 active session, got %d", len(got))
		}
		if got[0].ID != s1.ID {
			t.Errorf("expected session %s, got %s", s1.ID, got[0].ID)
		}
	})
}

func TestHandleTimeout(t *testing.T) {
	t.Run("auto-rejects pending session after timeout", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})

		// Manually trigger the timeout handler to avoid waiting 30s in tests.
		svc.handleTimeout(session.ID)

		var stored model.RemoteAssistSession
		if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
			t.Fatalf("session not found: %v", err)
		}
		if stored.Status != StatusTimeout {
			t.Errorf("expected status %q, got %q", StatusTimeout, stored.Status)
		}
	})

	t.Run("does not transition non-pending sessions", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		// Authorize before timeout fires.
		if _, err := svc.Authorize(session.ID, target.ID); err != nil {
			t.Fatalf("Authorize failed: %v", err)
		}

		svc.handleTimeout(session.ID)

		var stored model.RemoteAssistSession
		_ = db.First(&stored, "id = ?", session.ID).Error
		if stored.Status != StatusAuthorized {
			t.Errorf("expected status %q, got %q", StatusAuthorized, stored.Status)
		}
	})
}

func TestToSessionResponse(t *testing.T) {
	t.Run("decodes permissions JSON", func(t *testing.T) {
		now := time.Now().UTC()
		s := &model.RemoteAssistSession{
			ID:          "ra_1",
			RequesterID: "user_1",
			TargetID:    "user_2",
			ChannelID:   "ch_1",
			Status:      StatusPending,
			Permissions: `{"mouse":true,"keyboard":false}`,
			ExpiresAt:   now.Add(30 * time.Second),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		resp := toSessionResponse(s)
		if !resp.Permissions.Mouse || resp.Permissions.Keyboard {
			t.Errorf("unexpected permissions: %+v", resp.Permissions)
		}
	})

	t.Run("handles empty permissions", func(t *testing.T) {
		s := &model.RemoteAssistSession{
			ID:          "ra_1",
			RequesterID: "user_1",
			TargetID:    "user_2",
			ChannelID:   "ch_1",
			Status:      StatusPending,
		}
		resp := toSessionResponse(s)
		if resp.Permissions.Mouse || resp.Permissions.Keyboard {
			t.Errorf("expected zero-value permissions, got %+v", resp.Permissions)
		}
	})
}

// TestPermissionsScreen covers DES-2026-0912-07 §4.1: the new "screen" permission
// (remote assist 画面回传) must round-trip through the JSON permissions column,
// and sessions persisted by clients that predate the field must decode to
// screen=false so their behaviour is unchanged (blind operation).
func TestPermissionsScreen(t *testing.T) {
	t.Run("legacy JSON without screen decodes to false", func(t *testing.T) {
		s := &model.RemoteAssistSession{
			ID:          "ra_legacy",
			RequesterID: "user_1",
			TargetID:    "user_2",
			ChannelID:   "ch_1",
			Status:      StatusPending,
			Permissions: `{"mouse":true,"keyboard":true}`,
		}
		resp := toSessionResponse(s)
		if !resp.Permissions.Mouse || !resp.Permissions.Keyboard {
			t.Fatalf("legacy permissions lost: %+v", resp.Permissions)
		}
		if resp.Permissions.Screen {
			t.Fatalf("legacy session must not grant screen, got %+v", resp.Permissions)
		}
	})

	t.Run("screen true survives create + reload round-trip", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		created, err := svc.CreateRequest(requester.ID, target.ID, channel.ID,
			Permissions{Mouse: true, Keyboard: false, Screen: true})
		if err != nil {
			t.Fatalf("CreateRequest: %v", err)
		}
		if !created.Permissions.Screen {
			t.Fatalf("screen dropped on create: %+v", created.Permissions)
		}

		// Re-read from the DB so the JSON column is exercised end-to-end.
		reloaded, err := svc.GetSession(created.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if !reloaded.Permissions.Screen || !reloaded.Permissions.Mouse || reloaded.Permissions.Keyboard {
			t.Fatalf("permissions not persisted: %+v", reloaded.Permissions)
		}
	})

	t.Run("screen false is preserved (blind operation)", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		created, err := svc.CreateRequest(requester.ID, target.ID, channel.ID,
			Permissions{Mouse: true, Keyboard: true, Screen: false})
		if err != nil {
			t.Fatalf("CreateRequest: %v", err)
		}
		reloaded, err := svc.GetSession(created.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if reloaded.Permissions.Screen {
			t.Fatalf("screen must stay false, got %+v", reloaded.Permissions)
		}
	})

	t.Run("screen is independent of mouse/keyboard (view-only assist)", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		created, err := svc.CreateRequest(requester.ID, target.ID, channel.ID,
			Permissions{Mouse: false, Keyboard: false, Screen: true})
		if err != nil {
			t.Fatalf("CreateRequest: %v", err)
		}
		if !created.Permissions.Screen || created.Permissions.Mouse || created.Permissions.Keyboard {
			t.Fatalf("view-only permissions wrong: %+v", created.Permissions)
		}
		// 只想看画面时，授权闸门本身与 mouse/keyboard 无关：被控端授权后
		// 服务端仍会放行控制事件，是否注入由客户端按权限位决定（面板不发送）。
		if _, err := svc.Authorize(created.ID, target.ID); err != nil {
			t.Fatalf("Authorize: %v", err)
		}
		if err := svc.ValidateControlEvent(created.ID, requester.ID, target.ID); err != nil {
			t.Fatalf("authorized session should still accept control events: %v", err)
		}
	})
}

// TestValidateControlEvent covers H14: the WebSocket control-event forwarding
// path (hub.go "remote_assist_control") must verify that the sender is the
// session's requester, that the payload target is the session's target, and
// that the session still exists and is authorized. Without this check any
// authenticated user could inject mouse/keyboard events into another user's
// machine while that user has an unrelated assist session open.
func TestValidateControlEvent(t *testing.T) {
	// authorizedSession returns an authorized session between requester and
	// target, plus the service and db.
	authorizedSession := func(t *testing.T) (svc *Service, sessionID, requesterID, targetID string) {
		t.Helper()
		requester, target, _, channel, db := setupTestSpace(t)
		svc = newTestService(db)
		session, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{Mouse: true, Keyboard: true})
		if err != nil {
			t.Fatalf("CreateRequest failed: %v", err)
		}
		if _, err := svc.Authorize(session.ID, target.ID); err != nil {
			t.Fatalf("Authorize failed: %v", err)
		}
		return svc, session.ID, requester.ID, target.ID
	}

	t.Run("allows the session requester targeting the session target", func(t *testing.T) {
		svc, sessionID, requesterID, targetID := authorizedSession(t)

		if err := svc.ValidateControlEvent(sessionID, requesterID, targetID); err != nil {
			t.Fatalf("expected control event to be allowed, got %v", err)
		}
	})

	t.Run("rejects a non-requester (third party injection)", func(t *testing.T) {
		svc, sessionID, _, targetID := authorizedSession(t)

		// An unrelated authenticated user who guessed the sessionID.
		err := svc.ValidateControlEvent(sessionID, "user-attacker", targetID)
		if err == nil {
			t.Fatal("expected control event from a non-requester to be rejected")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_PERMISSION_DENIED {
			t.Errorf("expected REMOTE_ASSIST_PERMISSION_DENIED, got %v", err)
		}
	})

	t.Run("rejects the target sending control events to itself", func(t *testing.T) {
		svc, sessionID, _, targetID := authorizedSession(t)

		err := svc.ValidateControlEvent(sessionID, targetID, targetID)
		if err == nil {
			t.Fatal("expected control event from the target to be rejected")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_PERMISSION_DENIED {
			t.Errorf("expected REMOTE_ASSIST_PERMISSION_DENIED, got %v", err)
		}
	})

	t.Run("rejects an unknown session", func(t *testing.T) {
		svc, _, requesterID, targetID := authorizedSession(t)

		err := svc.ValidateControlEvent("ra_does_not_exist", requesterID, targetID)
		if err == nil {
			t.Fatal("expected control event for a non-existent session to be rejected")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_NOT_FOUND {
			t.Errorf("expected REMOTE_ASSIST_NOT_FOUND, got %v", err)
		}
	})

	t.Run("rejects an empty session id", func(t *testing.T) {
		svc, _, requesterID, targetID := authorizedSession(t)

		if err := svc.ValidateControlEvent("", requesterID, targetID); err == nil {
			t.Fatal("expected control event without a sessionId to be rejected")
		}
	})

	t.Run("rejects a session that is still pending", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		// Pending: the target has not authorized yet.
		session, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{Mouse: true})
		if err != nil {
			t.Fatalf("CreateRequest failed: %v", err)
		}
		defer svc.cancelTimeout(session.ID)

		err = svc.ValidateControlEvent(session.ID, requester.ID, target.ID)
		if err == nil {
			t.Fatal("expected control event for a pending session to be rejected")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_INVALID_STATE {
			t.Errorf("expected REMOTE_ASSIST_INVALID_STATE, got %v", err)
		}
	})

	t.Run("rejects a session that has ended", func(t *testing.T) {
		svc, sessionID, requesterID, targetID := authorizedSession(t)
		if _, err := svc.End(sessionID, requesterID); err != nil {
			t.Fatalf("End failed: %v", err)
		}

		err := svc.ValidateControlEvent(sessionID, requesterID, targetID)
		if err == nil {
			t.Fatal("expected control event for an ended session to be rejected")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_INVALID_STATE {
			t.Errorf("expected REMOTE_ASSIST_INVALID_STATE, got %v", err)
		}
	})

	t.Run("rejects a mismatched targetId", func(t *testing.T) {
		svc, sessionID, requesterID, _ := authorizedSession(t)

		err := svc.ValidateControlEvent(sessionID, requesterID, "user-someone-else")
		if err == nil {
			t.Fatal("expected control event with a mismatched targetId to be rejected")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_PERMISSION_DENIED {
			t.Errorf("expected REMOTE_ASSIST_PERMISSION_DENIED, got %v", err)
		}
	})
}

// TestSessionDurationLimit covers M10 / DES-2026-0912-07 §5.2: the 30-minute
// cap on *authorized* sessions is enforced server-side (previously it existed
// only in RemoteAssistPanel). The deadline is UpdatedAt + MaxSessionDuration,
// judged lazily at the control-event gate and visibility paths, plus a
// per-session notification timer scheduled at Authorize.
func TestSessionDurationLimit(t *testing.T) {
	// authorizedSession creates + authorizes a session and back-dates its
	// updated_at by the given duration (UpdateColumn bypasses GORM's
	// auto-timestamp so we can simulate the passage of time).
	authorizedSession := func(t *testing.T, age time.Duration) (svc *Service, sessionID, requesterID, targetID string, db *gorm.DB) {
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
		if err := db.Model(&model.RemoteAssistSession{}).
			Where("id = ?", session.ID).
			UpdateColumn("updated_at", time.Now().UTC().Add(-age)).Error; err != nil {
			t.Fatalf("backdate updated_at: %v", err)
		}
		return svc, session.ID, requester.ID, target.ID, db
	}

	t.Run("under the cap: control events pass and session stays authorized", func(t *testing.T) {
		svc, sessionID, requesterID, targetID, _ := authorizedSession(t, time.Minute)
		defer svc.cancelTimeout(sessionID)

		if err := svc.ValidateControlEvent(sessionID, requesterID, targetID); err != nil {
			t.Fatalf("control event within 30min must pass, got %v", err)
		}
		got, err := svc.GetSession(sessionID)
		if err != nil {
			t.Fatalf("GetSession failed: %v", err)
		}
		if got.Status != StatusAuthorized {
			t.Errorf("expected status %q, got %q", StatusAuthorized, got.Status)
		}
	})

	t.Run("over the cap: control-event gate force-ends and rejects", func(t *testing.T) {
		svc, sessionID, requesterID, targetID, db := authorizedSession(t, MaxSessionDuration+time.Minute)

		err := svc.ValidateControlEvent(sessionID, requesterID, targetID)
		if err == nil {
			t.Fatal("expected control event on an over-limit session to be rejected")
		}
		if appErr, ok := err.(*errors.AppError); !ok || appErr.Code != errors.REMOTE_ASSIST_EXPIRED {
			t.Errorf("expected REMOTE_ASSIST_EXPIRED, got %v", err)
		}

		var stored model.RemoteAssistSession
		if err := db.First(&stored, "id = ?", sessionID).Error; err != nil {
			t.Fatalf("session not found: %v", err)
		}
		if stored.Status != StatusEnded {
			t.Errorf("expected force-ended status %q, got %q", StatusEnded, stored.Status)
		}
	})

	t.Run("over the cap: ListActiveSessions expires and excludes", func(t *testing.T) {
		svc, sessionID, requesterID, _, db := authorizedSession(t, MaxSessionDuration+time.Minute)

		got, err := svc.ListActiveSessions(requesterID)
		if err != nil {
			t.Fatalf("ListActiveSessions failed: %v", err)
		}
		for _, s := range got {
			if s.ID == sessionID {
				t.Error("over-limit session must be excluded from the active list")
			}
		}
		var stored model.RemoteAssistSession
		if err := db.First(&stored, "id = ?", sessionID).Error; err != nil {
			t.Fatalf("session not found: %v", err)
		}
		if stored.Status != StatusEnded {
			t.Errorf("expected status %q, got %q", StatusEnded, stored.Status)
		}
	})

	t.Run("over the cap: stale session no longer blocks a new request", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if err != nil {
			t.Fatalf("CreateRequest failed: %v", err)
		}
		if _, err := svc.Authorize(session.ID, target.ID); err != nil {
			t.Fatalf("Authorize failed: %v", err)
		}
		defer svc.cancelTimeout(session.ID)
		// Backdate past the cap, then the duplicate-active check in
		// CreateRequest must expire it instead of refusing the new request.
		if err := db.Model(&model.RemoteAssistSession{}).
			Where("id = ?", session.ID).
			UpdateColumn("updated_at", time.Now().UTC().Add(-MaxSessionDuration-time.Minute)).Error; err != nil {
			t.Fatalf("backdate updated_at: %v", err)
		}
		if _, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{}); err != nil {
			t.Fatalf("CreateRequest after expiry should succeed, got %v", err)
		}
		var stored model.RemoteAssistSession
		if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
			t.Fatalf("session not found: %v", err)
		}
		if stored.Status != StatusEnded {
			t.Errorf("expected stale session force-ended, got %q", stored.Status)
		}
	})

	t.Run("handleSessionExpiry leaves non-authorized sessions untouched", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		// Pending session (its own 30s expiry is a different mechanism).
		pending, err := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if err != nil {
			t.Fatalf("CreateRequest failed: %v", err)
		}
		defer svc.cancelTimeout(pending.ID)
		// Simulate a pending row that has sat far longer than the cap.
		if err := db.Model(&model.RemoteAssistSession{}).
			Where("id = ?", pending.ID).
			UpdateColumn("updated_at", time.Now().UTC().Add(-MaxSessionDuration-time.Minute)).Error; err != nil {
			t.Fatalf("backdate updated_at: %v", err)
		}

		svc.handleSessionExpiry(pending.ID)

		var stored model.RemoteAssistSession
		if err := db.First(&stored, "id = ?", pending.ID).Error; err != nil {
			t.Fatalf("session not found: %v", err)
		}
		if stored.Status != StatusPending {
			t.Errorf("pending session must not be duration-expired, got %q", stored.Status)
		}
	})

	t.Run("handleSessionExpiry is idempotent for already-ended sessions", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if _, err := svc.End(session.ID, requester.ID); err != nil {
			t.Fatalf("End failed: %v", err)
		}

		// Must not panic and must not resurrect / re-transition anything.
		svc.handleSessionExpiry(session.ID)
		svc.handleSessionExpiry(session.ID)

		var stored model.RemoteAssistSession
		if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
			t.Fatalf("session not found: %v", err)
		}
		if stored.Status != StatusEnded {
			t.Errorf("expected status to stay %q, got %q", StatusEnded, stored.Status)
		}
	})

	t.Run("authorize schedules the duration timer and end cancels it", func(t *testing.T) {
		requester, target, _, channel, db := setupTestSpace(t)
		svc := newTestService(db)

		session, _ := svc.CreateRequest(requester.ID, target.ID, channel.ID, Permissions{})
		if _, err := svc.Authorize(session.ID, target.ID); err != nil {
			t.Fatalf("Authorize failed: %v", err)
		}

		svc.mu.Lock()
		_, hasTimer := svc.timeoutTimers[session.ID]
		svc.mu.Unlock()
		if !hasTimer {
			t.Error("Authorize must schedule the duration-limit timer")
		}

		if _, err := svc.End(session.ID, target.ID); err != nil {
			t.Fatalf("End failed: %v", err)
		}
		svc.mu.Lock()
		_, hasTimer = svc.timeoutTimers[session.ID]
		svc.mu.Unlock()
		if hasTimer {
			t.Error("End must cancel the duration-limit timer")
		}
	})
}

// Compile-time check: ensure *Service implements the expected surface.
var _ interface {
	IsUserInSpace(string, string) (bool, error)
} = (*Service)(nil)

func TestConfigDefault(t *testing.T) {
	// Sanity check that config.DefaultConfig is usable for tests that
	// construct handlers needing a *config.Config.
	cfg := config.DefaultConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}
