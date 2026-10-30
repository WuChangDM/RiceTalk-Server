package xiangqi

import (
	"testing"
)

// TestXiangqiDrawAfterNoCaptureLimit D4：60 回合（120 半步）无吃子 → 和棋。
// 用「只剩双方将/帅 + 已累计 119 半步」的局面，走一步不吃子的合法棋即达上限。
func TestXiangqiDrawAfterNoCaptureLimit(t *testing.T) {
	// 黑将在 (3,0)，红帅在 (4,9)，不同列，不会被将帅照面规则挡住。
	diagram := `
...k.....
.........
.........
.........
.........
.........
.........
.........
.........
....K....`
	st := stateWithBoard(t, diagram)
	st["noCaptureMoves"] = float64(noCaptureHalfMoves - 1)

	next, err := New().ValidateMove(st, "alice", "move_made", movePayload(4, 9, 4, 8))
	if err != nil {
		t.Fatalf("走子应合法，实际错误: %v", err)
	}
	if d, _ := next["draw"].(bool); !d {
		t.Fatalf("120 半步无吃子应判和棋，实际 draw=%v", next["draw"])
	}
	if reason, _ := next["endReason"].(string); reason != "draw_no_capture" {
		t.Fatalf("endReason 期望 draw_no_capture，实际 %q", reason)
	}
	if w, _ := next["winner"].(string); w != "" {
		t.Fatalf("和棋不应有胜者，实际 %q", w)
	}
	if cur, _ := next["currentPlayer"].(string); cur != "" {
		t.Fatalf("和棋后不应再有当前玩家，实际 %q", cur)
	}
	if winnerID, draw := New().IsGameOver(next); winnerID != "" || !draw {
		t.Fatalf("IsGameOver 应报告和棋，实际 winner=%q draw=%v", winnerID, draw)
	}
}

// TestXiangqiDrawWhenNoAttackingPieces D4：双方均无进攻子力（只剩将帅/士象）→ 和棋。
func TestXiangqiDrawWhenNoAttackingPieces(t *testing.T) {
	// 红：帅(4,9)、仕(3,9)、相(2,9)；黑：将(3,0)。双方均无车马炮兵。
	diagram := `
...k.....
.........
.........
.........
.........
.........
.........
.........
.........
..BAK....`
	st := stateWithBoard(t, diagram)

	next, err := New().ValidateMove(st, "alice", "move_made", movePayload(3, 9, 4, 8))
	if err != nil {
		t.Fatalf("仕的走子应合法，实际错误: %v", err)
	}
	if d, _ := next["draw"].(bool); !d {
		t.Fatalf("双方无进攻子力应判和棋，实际 draw=%v", next["draw"])
	}
	if reason, _ := next["endReason"].(string); reason != "draw_no_attacking_pieces" {
		t.Fatalf("endReason 期望 draw_no_attacking_pieces，实际 %q", reason)
	}
	if winnerID, draw := New().IsGameOver(next); winnerID != "" || !draw {
		t.Fatalf("IsGameOver 应报告和棋，实际 winner=%q draw=%v", winnerID, draw)
	}
}

// TestXiangqiNoFalseDraw D4：正常开局走子不得误判和棋；吃子后无吃子计数归零。
func TestXiangqiNoFalseDraw(t *testing.T) {
	st, err := New().Init(twoPlayers())
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// 红兵 (0,6) 前进一步（未过河，合法且不吃子）。
	next, err := New().ValidateMove(st, "alice", "move_made", movePayload(0, 6, 0, 5))
	if err != nil {
		t.Fatalf("兵前进应合法，实际错误: %v", err)
	}
	if d, _ := next["draw"].(bool); d {
		t.Fatal("正常开局不应判和棋")
	}
	if reason, _ := next["endReason"].(string); reason != "" {
		t.Fatalf("正常走子 endReason 应为空，实际 %q", reason)
	}
	if got := intFromState(next["noCaptureMoves"]); got != 1 {
		t.Fatalf("无吃子计数应累计为 1，实际 %d", got)
	}

	// 吃子后计数清零：红炮 (4,9) 越过红马炮架 (4,4) 吃黑炮 (4,0)。
	captureDiagram := `
....ck...
.........
.........
.........
....N....
.........
.........
.........
.........
....C...K`
	cst := stateWithBoard(t, captureDiagram)
	cst["noCaptureMoves"] = float64(50)

	afterCapture, err := New().ValidateMove(cst, "alice", "move_made", movePayload(4, 9, 4, 0))
	if err != nil {
		t.Fatalf("炮吃炮应合法，实际错误: %v", err)
	}
	if got := intFromState(afterCapture["noCaptureMoves"]); got != 0 {
		t.Fatalf("发生吃子后无吃子计数应归零，实际 %d", got)
	}
}
