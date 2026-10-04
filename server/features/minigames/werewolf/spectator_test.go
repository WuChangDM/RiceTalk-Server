package werewolf

import "testing"

func TestSpectatorStateMasksRoles(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
		{"userId": "d"}, {"userId": "e"}, {"userId": "f"},
	})
	masked := engine.SpectatorState(state)
	if _, ok := masked["roles"]; ok {
		t.Fatal("expected roles to be masked for spectators")
	}
	// 夜晚行动含狼刀目标与预言家查验结果，观战者不得看到（设计 §15.7.5.2）。
	if _, ok := masked["nightActions"]; ok {
		t.Fatal("expected nightActions (kill target / seer result) to be masked for spectators")
	}
	if masked["phase"] != "night" {
		t.Fatalf("expected phase preserved, got %v", masked["phase"])
	}
	alive := masked["alive"]
	if alive == nil {
		t.Fatal("expected alive to be preserved for spectators")
	}
}
