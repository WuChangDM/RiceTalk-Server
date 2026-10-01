package doudizhu

import "testing"

func TestSpectatorStateMasksHands(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	masked := engine.SpectatorState(state)
	if _, ok := masked["hands"]; ok {
		t.Fatal("expected hands to be masked for spectators")
	}
	counts, ok := masked["handCounts"].(map[string]int)
	if !ok {
		t.Fatal("expected handCounts to be present for spectators")
	}
	if counts["a"] != 17 || counts["b"] != 17 || counts["c"] != 17 {
		t.Fatalf("expected 17 cards each in handCounts, got %v", counts)
	}
	// 公开字段应保留
	if masked["phase"] != "bid" {
		t.Fatalf("expected phase preserved, got %v", masked["phase"])
	}
	if masked["currentPlayer"] != "a" {
		t.Fatalf("expected currentPlayer preserved, got %v", masked["currentPlayer"])
	}
}
