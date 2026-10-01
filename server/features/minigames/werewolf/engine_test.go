package werewolf

import (
	"encoding/json"
	"testing"
)

func TestEngineRolesAndNightKill(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	phase, _ := state["phase"].(string)
	if phase != "night" {
		t.Fatalf("expected night phase, got %v", phase)
	}
	roles, _ := state["roles"].(map[string]string)
	if roles["a"] != "werewolf" {
		t.Fatalf("expected a to be werewolf, got %v", roles["a"])
	}
	// Werewolf kills player b.
	payload, _ := json.Marshal(map[string]interface{}{"target": "b"})
	state, err := engine.ValidateMove(state, "a", "werewolf_kill", payload)
	if err != nil {
		t.Fatalf("kill failed: %v", err)
	}
	if state["phase"] != "day" {
		t.Fatalf("expected phase to advance to day, got %v", state["phase"])
	}
}
