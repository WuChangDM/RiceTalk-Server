package sfx

import (
	"bytes"
	"testing"
	"time"
)

// The playback side effect (LiveKit publish) is not exercised here — these tests
// cover trigger's guard decisions (enabled / unbound / empty ids / rapid transitions),
// which all run before and gate playback. Playback itself is verified in
// integration (cgo build + LiveKit).

func TestTrigger_SkipsWhenUnbound(t *testing.T) {
	svc := newTestService(t, 20)
	// No binding for user1 → trigger should be a no-op (no panic, no goroutine error)
	svc.TriggerJoin("user1", "room1")
	svc.TriggerLeave("user1", "room1")
	// Nothing to assert on playback; success = no panic and quick return.
}

func TestTrigger_SkipsWhenDisabled(t *testing.T) {
	svc := newTestService(t, 20)
	data := validWav(512)
	sound, err := svc.UploadSound("user1", "ding.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	disabled := false
	joinID := sound.ID
	if _, err := svc.UpdateSettings("user1", SettingsInput{JoinSoundID: &joinID, Enabled: &disabled}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	// Enabled=false must NOT attempt playback.
	attempts := &recordingPlayer{attempts: make(chan struct{}, 4)}
	svc.player = attempts
	svc.TriggerJoin("user1", "room1")
	select {
	case <-attempts.attempts:
		t.Fatal("trigger attempted playback while disabled")
	case <-time.After(300 * time.Millisecond):
		// no playback — correct
	}
}

// TestUpdateSettings_EnabledFalsePersists guards the GORM zero-value pitfall: a
// struct-based Save() silently drops Enabled=false (bool zero value). Updates must use
// column maps so the user's "关闭出入频道音效" toggle actually sticks.
func TestUpdateSettings_EnabledFalsePersists(t *testing.T) {
	svc := newTestService(t, 20)
	disabled := false
	if _, err := svc.UpdateSettings("user1", SettingsInput{Enabled: &disabled}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	st, err := svc.GetSettings("user1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if st.Enabled {
		t.Fatal("Enabled=false did NOT persist — GORM zero-value bug")
	}
}

func TestTrigger_SkipsEmptyIDs(t *testing.T) {
	svc := newTestService(t, 20)
	// empty userID/roomID must return before any work.
	svc.TriggerJoin("", "room1")
	svc.TriggerJoin("user1", "")
}

// TestTrigger_EveryTransitionPlays verifies that a sequence of rapid, alternating
// transitions (join → leave → rejoin → leave → rejoin — the real "channel switching"
// pattern) fires a playback attempt for EVERY one. Duplicate suppression is handled by
// the caller's DB state (participant row created / soft-deleted), never by a time window,
// so no transition may be silently dropped.
//
// We detect playback attempts by wrapping the player with a recording stub.
func TestTrigger_EveryTransitionPlays(t *testing.T) {
	svc := newTestService(t, 20)
	data := validWav(512)
	sound, err := svc.UploadSound("user1", "ding.wav", "audio/wav", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	joinID := sound.ID
	if _, err := svc.UpdateSettings("user1", SettingsInput{JoinSoundID: &joinID, LeaveSoundID: &joinID}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	attempts := &recordingPlayer{attempts: make(chan struct{}, 16)}
	svc.player = attempts

	seq := []string{"join", "leave", "join", "leave", "join"}
	for _, kind := range seq {
		if kind == "join" {
			svc.TriggerJoin("user1", "room1")
		} else {
			svc.TriggerLeave("user1", "room1")
		}
	}

	for i := range seq {
		select {
		case <-attempts.attempts:
			// a play attempt fired for this transition
		case <-time.After(2 * time.Second):
			t.Fatalf("transition %d of 5 never fired a play — it was dropped", i)
		}
	}
}

// recordingPlayer records each PlayOnce invocation without touching LiveKit.
type recordingPlayer struct {
	attempts chan struct{}
}

func (p *recordingPlayer) PlayOnce(_ string, _ string, _ int) error {
	p.attempts <- struct{}{}
	return nil
}

func (p *recordingPlayer) Disconnect() {}
