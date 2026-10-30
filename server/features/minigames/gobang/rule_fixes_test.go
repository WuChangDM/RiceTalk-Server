package gobang

import (
	"encoding/json"
	"testing"
)

// drawPatternBoard 生成一个 15x15 全盘且四方向最长同色连 <= 3 的棋盘：
// 颜色由 (r+2*c)%5 < 2 决定，从而保证任何方向都不会出现五连。
func drawPatternBoard() [][]interface{} {
	board := make([][]interface{}, gobangSize)
	for r := 0; r < gobangSize; r++ {
		board[r] = make([]interface{}, gobangSize)
		for c := 0; c < gobangSize; c++ {
			if (r+2*c)%5 < 2 {
				board[r][c] = "black"
			} else {
				board[r][c] = "white"
			}
		}
	}
	return board
}

// TestEngineDrawIsReported 回归：棋盘下满且无五连时必须判定平局。
// 修复前 winner 被写成空串，IsGameOver 只认非空 winner → 满盘后房间永久 playing。
func TestEngineDrawIsReported(t *testing.T) {
	engine := New()
	state, err := engine.Init([]map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"},
	})
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	board := drawPatternBoard()
	// 先自证该布局确实没有五连（否则本用例不成立）
	for r := 0; r < gobangSize; r++ {
		for c := 0; c < gobangSize; c++ {
			color, _ := board[r][c].(string)
			if engine.checkWinner(board, r, c, color) {
				t.Fatalf("测试布局在 (%d,%d) 出现五连，布局构造有误", r, c)
			}
		}
	}

	// 挖空 (0,0)（黑，先手 alice 的颜色）作为最后一手
	board[0][0] = nil
	state["board"] = board
	state["currentPlayer"] = "alice"

	payload, _ := json.Marshal(map[string]interface{}{"r": 0, "c": 0})
	newState, err := engine.ValidateMove(state, "alice", "move_made", payload)
	if err != nil {
		t.Fatalf("last move failed: %v", err)
	}

	if winner := newState["winner"]; winner != "draw" {
		t.Fatalf("满盘平局应写入 winner=draw，实际为 %v", winner)
	}
	winnerID, draw := engine.IsGameOver(newState)
	if !draw || winnerID != "" {
		t.Fatalf("平局应返回 (\"\", true)，实际为 (%q, %v)", winnerID, draw)
	}
}
