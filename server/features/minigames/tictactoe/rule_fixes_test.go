package tictactoe

import (
	"encoding/json"
	"testing"
)

// TestEngineIgnoresClientSymbol 回归：棋子必须由服务端按座位裁决。
// 修复前 bob（后手）只要在 payload 里提交 symbol="X" 就能以先手身份落子，
// 甚至连下两子/抢到不属于自己的胜局。
func TestEngineIgnoresClientSymbol(t *testing.T) {
	engine := New()
	state, err := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// alice（先手）落 0
	payload, _ := json.Marshal(map[string]interface{}{"index": 0})
	state, err = engine.ValidateMove(state, "alice", "move_made", payload)
	if err != nil {
		t.Fatalf("alice move failed: %v", err)
	}

	// bob（后手）伪造 symbol="X"
	payload, _ = json.Marshal(map[string]interface{}{"index": 3, "symbol": "X"})
	state, err = engine.ValidateMove(state, "bob", "move_made", payload)
	if err != nil {
		t.Fatalf("bob move failed: %v", err)
	}

	board, _ := state["board"].([]interface{})
	if got := board[3]; got != "O" {
		t.Fatalf("后手棋子应被服务端裁决为 O，实际为 %v（客户端 symbol 未被忽略）", got)
	}
	if got := board[0]; got != "X" {
		t.Fatalf("先手棋子应为 X，实际为 %v", got)
	}
}

// TestEngineDrawIsReported 回归：满盘平局必须让 IsGameOver 返回 draw=true。
// 修复前 winner 被写成空串，IsGameOver 只认 winner=="draw"，
// 平局永不结束 → 房间永久 playing、无历史、无计分。
func TestEngineDrawIsReported(t *testing.T) {
	engine := New()
	state, err := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// 已知无三连的平局终局：X O X / X O O / O X X
	moves := []struct {
		user string
		idx  int
	}{
		{"alice", 0}, {"bob", 1}, {"alice", 2},
		{"bob", 4}, {"alice", 3}, {"bob", 5},
		{"alice", 7}, {"bob", 6}, {"alice", 8},
	}
	for _, m := range moves {
		payload, _ := json.Marshal(map[string]interface{}{"index": m.idx})
		state, err = engine.ValidateMove(state, m.user, "move_made", payload)
		if err != nil {
			t.Fatalf("move by %s to %d failed: %v", m.user, m.idx, err)
		}
	}

	if winner := state["winner"]; winner != "draw" {
		t.Fatalf("满盘平局应写入 winner=draw，实际为 %v", winner)
	}
	winnerID, draw := engine.IsGameOver(state)
	if !draw || winnerID != "" {
		t.Fatalf("平局应返回 (\"\", true)，实际为 (%q, %v)", winnerID, draw)
	}
}
