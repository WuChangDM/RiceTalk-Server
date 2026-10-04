package ludo

import (
	"testing"
)

func TestEngineRollAndMove(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})
	// 骰子值为随机 1-6（P0 修复：原实现硬编码为 1，导致飞机永远无法起飞）
	state, err := engine.ValidateMove(state, "alice", "roll_dice", []byte("{}"))
	if err != nil {
		t.Fatalf("roll failed: %v", err)
	}
	roll, ok := state["lastRoll"].(int)
	if !ok {
		t.Fatalf("expected lastRoll to be int, got %T: %v", state["lastRoll"], state["lastRoll"])
	}
	if roll < 1 || roll > 6 {
		t.Fatalf("expected lastRoll in [1,6], got %d", roll)
	}
}
