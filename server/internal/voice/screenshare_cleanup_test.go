package voice

import (
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

// TestLeaveVoiceEndsScreenShare 验证：用户主动退出语音频道时，其在该频道的活跃屏幕共享
// 被级联结束（对齐 KOOK/Oopz/TeamSpeak：连接断开则共享终止），避免重进频道时回填"幽灵共享"。
func TestLeaveVoiceEndsScreenShare(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	room := model.VoiceRoom{ID: idgen.NextString(), SpaceID: idgen.NextString()}
	if err := db.Create(&room).Error; err != nil {
		t.Fatalf("failed to create room: %v", err)
	}
	if err := svc.JoinVoice("user-1", room.ID); err != nil {
		t.Fatalf("join failed: %v", err)
	}

	// 用户在该频道有一个活跃共享会话
	sess := model.ScreenShareSession{
		ID:        idgen.NextString(),
		UserID:    "user-1",
		SpaceID:   room.SpaceID,
		ChannelID: room.ID,
		Status:    "active",
		Active:    true,
		StartedAt: time.Now().UTC(),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("failed to create screenshare session: %v", err)
	}

	if err := svc.LeaveVoice("user-1", room.ID); err != nil {
		t.Fatalf("leave failed: %v", err)
	}

	var got model.ScreenShareSession
	if err := db.Where("id = ?", sess.ID).First(&got).Error; err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}
	if got.Active {
		t.Error("expected screenshare active=false after user left")
	}
	if got.Status != "ended" {
		t.Errorf("expected status 'ended', got '%s'", got.Status)
	}
	if got.EndedReason != "user_left" {
		t.Errorf("expected ended_reason 'user_left', got '%s'", got.EndedReason)
	}
	if got.EndedAt == nil {
		t.Error("expected ended_at to be set")
	}
}

// TestParticipantLeftWebhookEndsScreenShare 验证：LiveKit participant_left webhook
// （意外断连/崩溃的主入口）也级联结束共享。
func TestParticipantLeftWebhookEndsScreenShare(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)
	h := &Handler{service: svc, db: db, cfg: cfg, hub: nil}

	room := model.VoiceRoom{ID: idgen.NextString(), SpaceID: idgen.NextString()}
	if err := db.Create(&room).Error; err != nil {
		t.Fatalf("failed to create room: %v", err)
	}
	sess := model.ScreenShareSession{
		ID:        idgen.NextString(),
		UserID:    "user-1",
		SpaceID:   room.SpaceID,
		ChannelID: room.ID,
		Status:    "active",
		Active:    true,
		StartedAt: time.Now().UTC(),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("failed to create screenshare session: %v", err)
	}

	n := h.cleanupScreenShareOnLeave("user-1", room.ID, "participant_left")
	if n != 1 {
		t.Errorf("expected 1 session ended, got %d", n)
	}

	var got model.ScreenShareSession
	if err := db.Where("id = ?", sess.ID).First(&got).Error; err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}
	if got.Active {
		t.Error("expected screenshare active=false after participant_left")
	}
	if got.EndedReason != "participant_left" {
		t.Errorf("expected ended_reason 'participant_left', got '%s'", got.EndedReason)
	}

	// 幂等：再次调用应为 0
	if n := h.cleanupScreenShareOnLeave("user-1", room.ID, "participant_left"); n != 0 {
		t.Errorf("expected idempotent 0 on second call, got %d", n)
	}
}

// TestEndActiveScreenSharesInvokesKicker 验证：级联结束共享时，若提供了 kicker，
// 必须以真实 uid 调用一次（踢出 :share 虚身份连接由调用方实现，这里只验证接线）。
func TestEndActiveScreenSharesInvokesKicker(t *testing.T) {
	db := testutil.MustSetupTestDB()

	room := model.VoiceRoom{ID: idgen.NextString(), SpaceID: idgen.NextString()}
	if err := db.Create(&room).Error; err != nil {
		t.Fatalf("failed to create room: %v", err)
	}
	sess := model.ScreenShareSession{
		ID:        idgen.NextString(),
		UserID:    "user-1",
		SpaceID:   room.SpaceID,
		ChannelID: room.ID,
		Status:    "active",
		Active:    true,
		StartedAt: time.Now().UTC(),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("failed to create screenshare session: %v", err)
	}

	var kicked []string
	n := endActiveScreenShares(db, nil, "user-1", room.ID, "user_left", func(uid string) {
		kicked = append(kicked, uid)
	})
	if n != 1 {
		t.Errorf("expected 1 session ended, got %d", n)
	}
	if len(kicked) != 1 || kicked[0] != "user-1" {
		t.Errorf("expected kicker invoked once with user-1, got %v", kicked)
	}

	// 无活跃共享时不应调用 kicker
	kicked = nil
	if n := endActiveScreenShares(db, nil, "user-1", room.ID, "user_left", func(uid string) {
		kicked = append(kicked, uid)
	}); n != 0 || len(kicked) != 0 {
		t.Errorf("expected 0 ended and no kicker call, got n=%d kicked=%v", n, kicked)
	}
}

// TestLeaveVoiceDoesNotEndOthersScreenShare 验证：级联清理只结束离开者自己的共享，
// 不影响同频道其他用户的活跃共享。
func TestLeaveVoiceDoesNotEndOthersScreenShare(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)

	room := model.VoiceRoom{ID: idgen.NextString(), SpaceID: idgen.NextString()}
	if err := db.Create(&room).Error; err != nil {
		t.Fatalf("failed to create room: %v", err)
	}
	if err := svc.JoinVoice("user-1", room.ID); err != nil {
		t.Fatalf("join failed: %v", err)
	}
	other := model.ScreenShareSession{
		ID:        idgen.NextString(),
		UserID:    "user-2",
		SpaceID:   room.SpaceID,
		ChannelID: room.ID,
		Status:    "active",
		Active:    true,
		StartedAt: time.Now().UTC(),
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("failed to create other's session: %v", err)
	}

	if err := svc.LeaveVoice("user-1", room.ID); err != nil {
		t.Fatalf("leave failed: %v", err)
	}

	var got model.ScreenShareSession
	if err := db.Where("id = ?", other.ID).First(&got).Error; err != nil {
		t.Fatalf("failed to reload other's session: %v", err)
	}
	if !got.Active {
		t.Error("expected other user's screenshare to remain active")
	}
}
