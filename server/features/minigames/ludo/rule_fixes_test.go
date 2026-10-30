package ludo

import (
	"testing"

	"ridgericetalk/features/minigames/internal/util"
)

// TestEngineRejectsDoubleRoll 回归：掷骰后必须先走子才能再次掷骰。
// 修复前 roll_dice 不检查 state["rolled"]，玩家可反复掷骰直到 6 再起飞（作弊）。
func TestEngineRejectsDoubleRoll(t *testing.T) {
	engine := New()
	state, err := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// 构造「已掷骰、尚未走子」的状态
	state["rolled"] = true
	state["lastRoll"] = 3
	state["currentPlayer"] = "alice"

	if _, err := engine.ValidateMove(state, "alice", "roll_dice", []byte("{}")); err != util.ErrInvalidAction {
		t.Fatalf("已掷骰时重复掷骰应返回 ErrInvalidAction，实际为 %v", err)
	}
}

// TestEngineAllowsRollAfterMove 保证修复不过度收紧：走子后 rolled 复位，可再次掷骰。
func TestEngineAllowsRollAfterMove(t *testing.T) {
	engine := New()
	state, err := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// 直接构造「已掷出 6、rolled=true」的局面，走一架飞机后应能再掷骰。
	state["rolled"] = true
	state["lastRoll"] = 6
	state["currentPlayer"] = "alice"
	state, err = engine.ValidateMove(state, "alice", "move_pawn", []byte(`{"pawnIndex":0}`))
	if err != nil {
		t.Fatalf("move_pawn failed: %v", err)
	}
	if rolled, _ := state["rolled"].(bool); rolled {
		t.Fatal("走子后 rolled 应复位为 false")
	}
	if _, err := engine.ValidateMove(state, "alice", "roll_dice", []byte("{}")); err != nil {
		t.Fatalf("走子后应允许再次掷骰，实际错误: %v", err)
	}
}
