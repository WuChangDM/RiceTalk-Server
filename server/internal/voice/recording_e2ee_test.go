package voice

import (
	"testing"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// DES-2026-0912-06 §4.6：E2EE 房间的加密轨在 LiveKit Egress 侧无法解密，
// 录出来只能是噪声/黑屏，因此 StartRecording 必须直接拒绝并给出明确原因。
func TestStartRecordingRejectedInE2EERoom(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, config.DefaultConfig(), nil)

	room := &model.VoiceRoom{
		ID:          idgen.NextString(),
		SpaceID:     idgen.NextString(),
		E2EEEnabled: true,
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}

	rec, err := svc.StartRecording(idgen.NextString(), room.ID)
	if err == nil {
		t.Fatalf("expected StartRecording to be rejected in E2EE room, got recording %+v", rec)
	}
	if rec != nil {
		t.Errorf("expected nil recording, got %+v", rec)
	}

	appErr, ok := err.(*errors.AppError)
	if !ok {
		t.Fatalf("expected *errors.AppError, got %T", err)
	}
	if appErr.Code != errors.VOICE_E2EE_RECORDING_UNSUPPORTED {
		t.Errorf("expected code %s, got %s", errors.VOICE_E2EE_RECORDING_UNSUPPORTED, appErr.Code)
	}
	if appErr.Status != 403 {
		t.Errorf("expected HTTP 403, got %d", appErr.Status)
	}
	if appErr.ChineseMessage == "" {
		t.Error("expected a Chinese message explaining why recording is refused")
	}

	// 拒绝必须是「什么都没发生」：不能留下半开的录音记录
	var count int64
	db.Model(&model.VoiceRecording{}).Where("room_id = ?", room.ID).Count(&count)
	if count != 0 {
		t.Errorf("expected no recording row to be created, got %d", count)
	}
}

// 反向断言：E2EE 关闭（默认）时录音路径不受影响，行为与接线前一致。
func TestStartRecordingAllowedWhenE2EEDisabled(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db, config.DefaultConfig(), nil)

	room := &model.VoiceRoom{
		ID:          idgen.NextString(),
		SpaceID:     idgen.NextString(),
		E2EEEnabled: false,
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("failed to create voice room: %v", err)
	}

	rec, err := svc.StartRecording(idgen.NextString(), room.ID)
	if err != nil {
		t.Fatalf("expected no error for non-E2EE room, got %v", err)
	}
	if rec == nil {
		t.Fatal("expected a recording record")
	}
	if rec.RoomID != room.ID {
		t.Errorf("expected room %s, got %s", room.ID, rec.RoomID)
	}
	// 不断言 Status：测试环境没有真实 LiveKit，Egress 起不来会被置为 "failed"。
	// 这里只验证「没有被 E2EE 守卫拦截」。
	var count int64
	db.Model(&model.VoiceRecording{}).Where("room_id = ?", room.ID).Count(&count)
	if count != 1 {
		t.Errorf("expected exactly one recording row, got %d", count)
	}
}
