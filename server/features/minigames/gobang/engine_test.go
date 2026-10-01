package gobang

import (
	"encoding/json"
	"testing"
)

func TestEngineWin(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})
	// Play first 5 cells in a row for alice; bob plays off-line.
	moves := []struct {
		user string
		r, c int
	}{
		{"alice", 7, 7}, {"bob", 0, 0},
		{"alice", 7, 8}, {"bob", 0, 1},
		{"alice", 7, 9}, {"bob", 0, 2},
		{"alice", 7, 10}, {"bob", 0, 3},
		{"alice", 7, 11},
	}
	for _, m := range moves {
		payload, _ := json.Marshal(map[string]interface{}{"r": m.r, "c": m.c})
		var err error
		state, err = engine.ValidateMove(state, m.user, "move_made", payload)
		if err != nil {
			t.Fatalf("move by %s failed: %v", m.user, err)
		}
	}
	winner, _ := engine.IsGameOver(state)
	if winner != "alice" {
		t.Fatalf("expected alice to win, got %v", winner)
	}
}
