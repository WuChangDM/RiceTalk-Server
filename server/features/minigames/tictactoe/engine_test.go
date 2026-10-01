package tictactoe

import (
	"encoding/json"
	"testing"

	"ridgericetalk/features/minigames/internal/util"
)

func TestEngineWin(t *testing.T) {
	engine := New()
	players := []map[string]interface{}{
		{"userId": "alice"},
		{"userId": "bob"},
	}
	state, err := engine.Init(players)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	moves := []struct {
		user string
		idx  int
	}{
		{"alice", 0}, {"bob", 3},
		{"alice", 1}, {"bob", 4},
		{"alice", 2},
	}
	for _, m := range moves {
		payload, _ := json.Marshal(map[string]interface{}{"index": m.idx})
		state, err = engine.ValidateMove(state, m.user, "move_made", payload)
		if err != nil {
			t.Fatalf("move by %s to %d failed: %v", m.user, m.idx, err)
		}
	}
	winner, draw := engine.IsGameOver(state)
	if draw {
		t.Fatal("expected no draw")
	}
	if winner != "alice" {
		t.Fatalf("expected alice to win, got %v", winner)
	}
}

func TestEngineBlocksWrongTurn(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})
	payload, _ := json.Marshal(map[string]interface{}{"index": 0})
	_, err := engine.ValidateMove(state, "bob", "move_made", payload)
	if err != util.ErrNotYourTurn {
		t.Fatalf("expected ErrNotYourTurn, got %v", err)
	}
}

// TestEngineJSONRoundTrip 验证 JSON 序列化/反序列化后引擎仍能正常工作。
// 回归测试：修复 P0 bug - state["players"].([]string) 在 JSON 反序列化后变为
// []interface{}，导致 players=nil、symbol 永远为 "O"、winnerID 永远为空。
func TestEngineJSONRoundTrip(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})

	// Simulate DB round-trip: marshal to JSON, then unmarshal back.
	raw, _ := json.Marshal(state)
	var roundtripped map[string]interface{}
	if err := json.Unmarshal(raw, &roundtripped); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	state = roundtripped

	// After round-trip, players should still be resolved correctly.
	players := util.GetStringSlice(state, "players")
	if len(players) != 2 || players[0] != "alice" || players[1] != "bob" {
		t.Fatalf("players not resolved after JSON round-trip: %v", players)
	}

	moves := []struct {
		user string
		idx  int
	}{
		{"alice", 0}, {"bob", 3},
		{"alice", 1}, {"bob", 4},
		{"alice", 2}, // X wins (row 0,1,2)
	}
	for i, m := range moves {
		payload, _ := json.Marshal(map[string]interface{}{"index": m.idx})
		var err error
		state, err = engine.ValidateMove(state, m.user, "move_made", payload)
		if err != nil {
			t.Fatalf("move %d by %s to %d failed: %v", i+1, m.user, m.idx, err)
		}
		// Re-simulate DB round-trip after each move (handler.go does this).
		raw, _ := json.Marshal(state)
		var rt map[string]interface{}
		if err := json.Unmarshal(raw, &rt); err != nil {
			t.Fatalf("unmarshal after move %d failed: %v", i+1, err)
		}
		state = rt
	}
	winner, draw := engine.IsGameOver(state)
	if draw {
		t.Fatal("expected no draw")
	}
	if winner != "alice" {
		t.Fatalf("expected alice to win after JSON round-trip, got %v (state players type: %T)",
			winner, state["players"])
	}
}
