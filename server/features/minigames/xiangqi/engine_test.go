package xiangqi

import (
	"encoding/json"
	"strings"
	"testing"
)

// ===== 测试辅助 =====

func twoPlayers() []map[string]interface{} {
	return []map[string]interface{}{{"userId": "alice"}, {"userId": "bob"}}
}

// movePayload 构造 {from:{x,y}, to:{x,y}} 走子 payload。
func movePayload(fx, fy, tx, ty int) json.RawMessage {
	b, _ := json.Marshal(map[string]interface{}{
		"from": map[string]int{"x": fx, "y": fy},
		"to":   map[string]int{"x": tx, "y": ty},
	})
	return b
}

// boardFromDiagram 由图示构造棋盘，便于测试摆放局面。
// 共 10 行（第一行为 y=0 黑方底线），每行 9 格：
// 大写=红方，小写=黑方：K帅/将 A仕/士 B相/象 N马 R车 C炮 P兵/卒，"." 为空。
func boardFromDiagram(t *testing.T, diagram string) [][]*piece {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(diagram), "\n")
	if len(lines) != boardRows {
		t.Fatalf("diagram must have %d rows, got %d", boardRows, len(lines))
	}
	b := make([][]*piece, boardRows)
	for y, line := range lines {
		cells := []rune(strings.TrimSpace(line))
		if len(cells) != boardCols {
			t.Fatalf("diagram row %d must have %d cells, got %d", y, boardCols, len(cells))
		}
		b[y] = make([]*piece, boardCols)
		for x, ch := range cells {
			var typ string
			color := black
			if ch >= 'A' && ch <= 'Z' {
				color = red
			}
			switch ch | 0x20 {
			case 'k':
				typ = "king"
			case 'a':
				typ = "advisor"
			case 'b':
				typ = "bishop"
			case 'n':
				typ = "knight"
			case 'r':
				typ = "rook"
			case 'c':
				typ = "cannon"
			case 'p':
				typ = "pawn"
			case '.':
				continue
			default:
				t.Fatalf("diagram row %d has invalid char %q", y, string(ch))
			}
			b[y][x] = &piece{Type: typ, Color: color}
		}
	}
	return b
}

// stateWithBoard 构造一个 alice（红）执先、指定棋盘的 state。
func stateWithBoard(t *testing.T, diagram string) map[string]interface{} {
	t.Helper()
	st, err := New().Init(twoPlayers())
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	st["board"] = boardFromDiagram(t, diagram)
	return st
}

// pieceAt 断言棋盘某点为指定棋子。
func pieceAt(t *testing.T, state map[string]interface{}, x, y int, typ, color string) {
	t.Helper()
	board, ok := state["board"].([][]*piece)
	if !ok {
		t.Fatalf("state board has unexpected type %T", state["board"])
	}
	p := board[y][x]
	if p == nil || p.Type != typ || p.Color != color {
		t.Fatalf("expected %s %s at (%d,%d), got %+v", color, typ, x, y, p)
	}
}

// ===== 基础走子与回合 =====

func TestEngineBasicMoveAndTurn(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// 红炮 (1,7) -> (4,7)：平移三格，路径 (2,7)(3,7) 为空。
	state, err := engine.ValidateMove(state, "alice", "move_made", movePayload(1, 7, 4, 7))
	if err != nil {
		t.Fatalf("cannon move failed: %v", err)
	}
	if cur, _ := state["currentPlayer"].(string); cur != "bob" {
		t.Fatalf("expected currentPlayer=bob, got %v", cur)
	}
	pieceAt(t, state, 4, 7, "cannon", "red")
	// 黑方应一手：黑马 (7,0) -> (6,2)。
	state, err = engine.ValidateMove(state, "bob", "move_made", movePayload(7, 0, 6, 2))
	if err != nil {
		t.Fatalf("knight move failed: %v", err)
	}
	if cur, _ := state["currentPlayer"].(string); cur != "alice" {
		t.Fatalf("expected currentPlayer=alice, got %v", cur)
	}
	pieceAt(t, state, 6, 2, "knight", "black")
}

func TestEngineNotYourTurn(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// bob 执黑，试图先走。
	if _, err := engine.ValidateMove(state, "bob", "move_made", movePayload(7, 0, 6, 2)); err == nil {
		t.Fatal("expected not-your-turn error for bob first move")
	}
	// alice 让帅走出九宫（形状非法）也应被拒。
	if _, err := engine.ValidateMove(state, "alice", "move_made", movePayload(4, 9, 4, 6)); err == nil {
		t.Fatal("expected invalid move for king leaving palace")
	}
}

// ===== 马腿（蹩马腿）=====

func TestEngineKnightLegBlocked(t *testing.T) {
	engine := New()
	// 红马 (3,9) 走 (4,7)（日字），马腿在 (3,8)。
	// 局面一：红兵 (3,8) 塞腿 → 非法。
	state := stateWithBoard(t, strings.Join([]string{
		"....k....", // y0: 黑将
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		"...P.....", // y8: 红兵塞住马腿
		"...NK....", // y9: 红马 (3,9)、红帅 (4,9)
	}, "\n"))
	if _, err := engine.ValidateMove(state, "alice", "move_made", movePayload(3, 9, 4, 7)); err == nil {
		t.Fatal("expected knight move rejected when leg blocked at (3,8)")
	}
	// 局面二：移除塞腿的兵后走法应合法。
	state = stateWithBoard(t, strings.Join([]string{
		"....k....",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		"...NK....",
	}, "\n"))
	state, err := engine.ValidateMove(state, "alice", "move_made", movePayload(3, 9, 4, 7))
	if err != nil {
		t.Fatalf("expected knight move accepted without blocker: %v", err)
	}
	pieceAt(t, state, 4, 7, "knight", "red")
}

// ===== 相/象：塞象眼与不过河 =====

func TestEngineBishopEyeAndRiver(t *testing.T) {
	engine := New()
	// 红帅刻意放在 (3,9)，避免与黑将 (4,0) 同列 —— 否则相离列后触发照面判负，
	// 干扰塞象眼/过河规则的单独验证。
	empty := strings.Join([]string{
		"....k....",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		"...K.....",
	}, "\n")

	mk := func(blocked bool) map[string]interface{} {
		board := boardFromDiagram(t, empty)
		board[9][4] = &piece{Type: "king", Color: red}
		board[9][2] = &piece{Type: "bishop", Color: red}
		if blocked {
			// 相 (2,9) 飞 (4,7)：象眼在 (3,8)。
			board[8][3] = &piece{Type: "pawn", Color: red}
		}
		st, _ := New().Init(twoPlayers())
		st["board"] = board
		return st
	}

	// 1) 塞象眼：相 (2,9) -> (4,7) 被拒。
	if _, err := engine.ValidateMove(mk(true), "alice", "move_made", movePayload(2, 9, 4, 7)); err == nil {
		t.Fatal("expected bishop move rejected when eye blocked at (3,8)")
	}
	// 无塞眼：合法。
	st, err := engine.ValidateMove(mk(false), "alice", "move_made", movePayload(2, 9, 4, 7))
	if err != nil {
		t.Fatalf("expected bishop move accepted: %v", err)
	}
	pieceAt(t, st, 4, 7, "bishop", "red")

	// 2) 相不过河：相 (4,7) 飞 (2,5) 合法（仍在红半场）；相 (2,5) 飞 (0,3) 过河被拒。
	board := boardFromDiagram(t, empty)
	board[7][4] = &piece{Type: "bishop", Color: red}
	st0, _ := New().Init(twoPlayers())
	st0["board"] = board
	st1, err := engine.ValidateMove(st0, "alice", "move_made", movePayload(4, 7, 2, 5))
	if err != nil {
		t.Fatalf("expected bishop move within own half accepted: %v", err)
	}
	if _, err := engine.ValidateMove(st1, "alice", "move_made", movePayload(2, 5, 0, 3)); err == nil {
		t.Fatal("expected bishop move across river rejected")
	}
}

// ===== 炮：隔子打 =====

func TestEngineCannonScreenCapture(t *testing.T) {
	engine := New()
	empty := strings.Join([]string{
		"....k....",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		"....K....",
	}, "\n")
	mk := func(board [][]*piece) map[string]interface{} {
		st, _ := New().Init(twoPlayers())
		st["board"] = board
		return st
	}

	// 1) 隔一个炮架吃子：红炮 (4,4)、炮架黑卒 (4,3)、目标黑卒 (4,1) → 吃 (4,1) 合法。
	board := boardFromDiagram(t, empty)
	board[4][4] = &piece{Type: "cannon", Color: red}
	board[3][4] = &piece{Type: "pawn", Color: black}
	board[1][4] = &piece{Type: "pawn", Color: black}
	st, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 4, 4, 1))
	if err != nil {
		t.Fatalf("expected cannon screen capture accepted: %v", err)
	}
	pieceAt(t, st, 4, 1, "cannon", "red")

	// 2) 无炮架直吃：红炮 (4,4)、黑卒 (4,2) 中间无子 → 不能直接吃。
	board = boardFromDiagram(t, empty)
	board[4][4] = &piece{Type: "cannon", Color: red}
	board[2][4] = &piece{Type: "pawn", Color: black}
	if _, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 4, 4, 2)); err == nil {
		t.Fatal("expected direct capture without screen rejected")
	}
	// 无炮架时平移到目标之前的空格仍合法（走到 (4,3)）。
	if _, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 4, 4, 3)); err != nil {
		t.Fatalf("expected cannon plain move accepted: %v", err)
	}

	// 3) 两个炮架：红炮 (4,4)、炮架 (4,3)(4,2)、目标 (4,1) → 隔两子不能吃。
	board = boardFromDiagram(t, empty)
	board[4][4] = &piece{Type: "cannon", Color: red}
	board[3][4] = &piece{Type: "pawn", Color: black}
	board[2][4] = &piece{Type: "pawn", Color: black}
	board[1][4] = &piece{Type: "pawn", Color: black}
	if _, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 4, 4, 1)); err == nil {
		t.Fatal("expected capture with two screens rejected")
	}
}

// ===== 兵/卒：过河前后走法 =====

func TestEnginePawnRiverRules(t *testing.T) {
	engine := New()
	// 红帅放 (3,9)，避免与黑将 (4,0) 同列（照面会干扰兵过河规则的单独验证）。
	empty := strings.Join([]string{
		"....k....",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		"...K.....",
	}, "\n")
	mk := func(board [][]*piece) map[string]interface{} {
		st, _ := New().Init(twoPlayers())
		st["board"] = board
		return st
	}

	// 未过河红兵 (4,6)：只能前进到 (4,5)，不能横走。
	board := boardFromDiagram(t, empty)
	board[6][4] = &piece{Type: "pawn", Color: red}
	if _, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 6, 4, 5)); err != nil {
		t.Fatalf("expected forward move accepted before river: %v", err)
	}
	if _, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 6, 3, 6)); err == nil {
		t.Fatal("expected sideways move rejected before crossing river")
	}

	// 已过河红兵 (4,4)：可进 (4,3)、可横 (3,4)，不可退 (4,5)。
	board = boardFromDiagram(t, empty)
	board[4][4] = &piece{Type: "pawn", Color: red}
	if _, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 4, 3, 4)); err != nil {
		t.Fatalf("expected sideways move accepted after crossing river: %v", err)
	}
	board = boardFromDiagram(t, empty)
	board[4][4] = &piece{Type: "pawn", Color: red}
	if _, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 4, 4, 3)); err != nil {
		t.Fatalf("expected forward move accepted after crossing river: %v", err)
	}
	board = boardFromDiagram(t, empty)
	board[4][4] = &piece{Type: "pawn", Color: red}
	if _, err := engine.ValidateMove(mk(board), "alice", "move_made", movePayload(4, 4, 4, 5)); err == nil {
		t.Fatal("expected backward move rejected")
	}

	// 黑卒 (4,5) 已过河（黑过河线为 y>=5）：可横走 (3,5)。
	board = boardFromDiagram(t, empty)
	board[5][4] = &piece{Type: "pawn", Color: black}
	st := mk(board)
	st["currentPlayer"] = "bob"
	if _, err := engine.ValidateMove(st, "bob", "move_made", movePayload(4, 5, 3, 5)); err != nil {
		t.Fatalf("expected black pawn sideways move accepted after crossing river: %v", err)
	}
}

// ===== 将帅照面 =====

func TestEngineFlyingGeneral(t *testing.T) {
	engine := New()
	// 红帅 (4,9)、黑将 (4,0) 同列，红车 (4,5) 位于中间 → 当前合法（有子遮挡）。
	st := stateWithBoard(t, strings.Join([]string{
		"....k....",
		".........",
		".........",
		".........",
		".........",
		"....R....",
		".........",
		".........",
		".........",
		"....K....",
	}, "\n"))

	// 车离开 4 列 → 双王照面 → 非法。
	if _, err := engine.ValidateMove(st, "alice", "move_made", movePayload(4, 5, 0, 5)); err == nil {
		t.Fatal("expected move causing flying-general facing rejected")
	}
	// 车仍在 4 列内移动 → 合法。
	state, err := engine.ValidateMove(st, "alice", "move_made", movePayload(4, 5, 4, 3))
	if err != nil {
		t.Fatalf("expected rook move within same file accepted: %v", err)
	}
	// 车在 (4,3) 直指黑将 → 应标记将军。
	if check, _ := state["check"].(bool); !check {
		t.Fatal("expected check=true after rook checking black king")
	}
}

// ===== 将军标记与应将 =====

func TestEngineCheckFlagAndRespond(t *testing.T) {
	engine := New()
	// 红车 (0,1)、黑将 (4,0)、黑卒 (4,2)（可垫将）、红帅 (4,9)。
	st := stateWithBoard(t, strings.Join([]string{
		"....k....",
		".........",
		"....p....",
		".........",
		".........",
		".........",
		".........",
		".........",
		".........",
		"....K....",
	}, "\n"))
	// 图示未能表达红车，代码补上。
	board := boardFromState(st)
	board[1][0] = &piece{Type: "rook", Color: red}

	// 红车 (0,1) -> (4,1)：贴脸将军。
	state, err := engine.ValidateMove(st, "alice", "move_made", movePayload(0, 1, 4, 1))
	if err != nil {
		t.Fatalf("rook check move failed: %v", err)
	}
	if check, _ := state["check"].(bool); !check {
		t.Fatal("expected check=true after rook gives check")
	}
	// 黑方被将军时走不解将的子（黑卒 (4,2) 前进）应被拒。
	if _, err := engine.ValidateMove(state, "bob", "move_made", movePayload(4, 2, 4, 3)); err == nil {
		t.Fatal("expected move not resolving check rejected")
	}
	// 黑将避将： (4,0) -> (3,0) 合法，且此后 check 复位为 false。
	state, err = engine.ValidateMove(state, "bob", "move_made", movePayload(4, 0, 3, 0))
	if err != nil {
		t.Fatalf("expected king escape accepted: %v", err)
	}
	if check, _ := state["check"].(bool); check {
		t.Fatal("expected check=false after king escapes")
	}
	_ = board
}

// ===== 绝杀（将死）=====

func TestEngineCheckmate(t *testing.T) {
	engine := New()
	// 局面（红走一步成杀）：
	//   黑：将 (4,0) 单子。
	//   红：帅 (3,9)、车 (0,1)（保护红兵 (3,1)）、车 (8,1)（保护红兵 (5,1)）、
	//       红兵 (3,1)（控制 (3,0)）、红兵 (5,1)（控制 (5,0)）、车 (3,5)。
	// 红车 (3,5) -> (4,5) 后沿 4 列将军：黑将五个落点全部受控 → 绝杀。
	board := boardFromDiagram(t, strings.Join([]string{
		"....k....", // y0: 黑将
		".........", // y1: 红车/红兵由代码摆放
		".........",
		".........",
		".........",
		".........", // y5: 红车 (3,5) 由代码摆放
		".........",
		".........",
		".........",
		"..K......", // y9: 红帅 (3,9)
	}, "\n"))
	board[1][0] = &piece{Type: "rook", Color: red}
	board[1][3] = &piece{Type: "pawn", Color: red}
	board[1][5] = &piece{Type: "pawn", Color: red}
	board[1][8] = &piece{Type: "rook", Color: red}
	board[5][3] = &piece{Type: "rook", Color: red}
	st, _ := New().Init(twoPlayers())
	st["board"] = board

	state, err := engine.ValidateMove(st, "alice", "move_made", movePayload(3, 5, 4, 5))
	if err != nil {
		t.Fatalf("mating rook move failed: %v", err)
	}
	if reason, _ := state["endReason"].(string); reason != "checkmate" {
		t.Fatalf("expected endReason=checkmate, got %v", state["endReason"])
	}
	winner, draw := engine.IsGameOver(state)
	if draw || winner != "alice" {
		t.Fatalf("expected winner=alice, got winner=%v draw=%v", winner, draw)
	}
}

// ===== 困毙（无子可走且未被将军 → 判负）=====

func TestEngineStalemateLoses(t *testing.T) {
	engine := New()
	// 局面（红走一步成困毙）：
	//   黑：将 (4,0)。红：帅 (3,9)（避开黑将逃点所在列，防止照面干扰）、红兵 (3,1)
	//   （控制 (3,0) 与 (4,1)）、车 (7,7)。
	// 走子前黑方仅剩 (5,0) 一个合法落点；红车 (7,7) -> (5,7) 后沿 5 列封死 (5,0)，
	// 黑方无子可动且未被将军 → 困毙，黑负。
	board := boardFromDiagram(t, strings.Join([]string{
		"....k....", // y0: 黑将 (4,0)
		"...P.....", // y1: 红兵 (3,1)
		".........",
		".........",
		".........",
		".........",
		".........",
		".......R.", // y7: 红车 (7,7)
		".........",
		"...K.....", // y9: 红帅 (3,9)
	}, "\n"))
	st, _ := New().Init(twoPlayers())
	st["board"] = board

	// 前置校验：红走子前黑方尚有合法走法（将 (4,0) -> (5,0)）。
	if len(legalMovesForColor(board, black)) == 0 {
		t.Fatal("precondition failed: black should have legal moves before the stalemating move")
	}

	state, err := engine.ValidateMove(st, "alice", "move_made", movePayload(7, 7, 5, 7))
	if err != nil {
		t.Fatalf("stalemating rook move failed: %v", err)
	}
	if reason, _ := state["endReason"].(string); reason != "stalemate" {
		t.Fatalf("expected endReason=stalemate, got %v", state["endReason"])
	}
	if check, _ := state["check"].(bool); check {
		t.Fatal("stalemate must not be in check")
	}
	winner, draw := engine.IsGameOver(state)
	if draw || winner != "alice" {
		t.Fatalf("expected winner=alice (stalemate loses), got winner=%v draw=%v", winner, draw)
	}
}

// ===== 仕（九宫斜一步）=====

func TestEngineAdvisorPalaceOnly(t *testing.T) {
	engine := New()
	// 红仕 (3,9)（开局位）斜走到 (4,8) 合法。
	state, _ := engine.Init(twoPlayers())
	state, err := engine.ValidateMove(state, "alice", "move_made", movePayload(3, 9, 4, 8))
	if err != nil {
		t.Fatalf("expected advisor diagonal move accepted: %v", err)
	}
	pieceAt(t, state, 4, 8, "advisor", "red")
	// 黑应一手（黑仕 (3,0) -> (4,1)）。
	state, err = engine.ValidateMove(state, "bob", "move_made", movePayload(3, 0, 4, 1))
	if err != nil {
		t.Fatalf("expected black advisor diagonal move accepted: %v", err)
	}
	// 红仕 (4,8) 直走 (4,7)（非斜线）被拒。
	if _, err := engine.ValidateMove(state, "alice", "move_made", movePayload(4, 8, 4, 7)); err == nil {
		t.Fatal("expected advisor orthogonal move rejected")
	}
}

// ===== 初始局面 sanity：红方开局合法走法共 44 种 =====

func TestEngineInitialLegalMoveCount(t *testing.T) {
	state, _ := New().Init(twoPlayers())
	board := boardFromState(state)
	n := len(legalMovesForColor(board, red))
	if n != 44 {
		t.Fatalf("expected 44 legal opening moves for red, got %d", n)
	}
}
