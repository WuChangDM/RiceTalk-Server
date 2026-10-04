package chess

import (
	"encoding/json"
	"testing"
)

func twoPlayers() []map[string]interface{} {
	return []map[string]interface{}{{"userId": "alice"}, {"userId": "bob"}}
}

func movePayload(fr, fc, tr, tc int, promo string) json.RawMessage {
	m := map[string]interface{}{
		"from": map[string]int{"r": fr, "c": fc},
		"to":   map[string]int{"r": tr, "c": tc},
	}
	if promo != "" {
		m["promotion"] = promo
	}
	b, _ := json.Marshal(m)
	return b
}

func TestEngineBasicMove(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// 白兵 e2(6,4) -> e4(4,4)
	state, err := engine.ValidateMove(state, "alice", "move_made", movePayload(6, 4, 4, 4, ""))
	if err != nil {
		t.Fatalf("pawn move failed: %v", err)
	}
	if cur, _ := state["currentPlayer"].(string); cur != "bob" {
		t.Fatalf("expected currentPlayer=bob, got %v", cur)
	}
	if fen, _ := state["fen"].(string); fen == "" {
		t.Fatal("expected fen to be set")
	}
}

func TestEngineNotYourTurn(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// bob 试图先走
	if _, err := engine.ValidateMove(state, "bob", "move_made", movePayload(1, 4, 3, 4, "")); err == nil {
		t.Fatal("expected not-your-turn error for bob first move")
	}
}

func TestEngineIllegalMove(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// 白兵 e2 -> e5（非法三格）
	if _, err := engine.ValidateMove(state, "alice", "move_made", movePayload(6, 4, 3, 4, "")); err == nil {
		t.Fatal("expected illegal move error for pawn e2->e5")
	}
}

func TestEngineCastling(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// 用 FEN 直接构造一个可王车易位的局面：白方仅王车，易位权完整。
	state["fen"] = "r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1"
	// 白王 e1(7,4) -> g1(7,6) 王翼易位
	state, err := engine.ValidateMove(state, "alice", "move_made", movePayload(7, 4, 7, 6, ""))
	if err != nil {
		t.Fatalf("castling failed: %v", err)
	}
	board := state["board"].([][]*piece)
	// 易位后：g1 应为白王，f1 应为白车
	if board[7][6] == nil || board[7][6].Type != "king" || board[7][6].Color != "white" {
		t.Fatalf("expected white king on g1, got %+v", board[7][6])
	}
	if board[7][5] == nil || board[7][5].Type != "rook" || board[7][5].Color != "white" {
		t.Fatalf("expected white rook on f1, got %+v", board[7][5])
	}
}

func TestEngineEnPassant(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// 白兵 e5，黑兵刚 d7->d5，白可 exd6 e.p.
	state["fen"] = "4k3/8/8/3pP3/8/8/8/4K3 w - d6 0 2"
	// 白兵 e5(3,4) -> d6(2,3) 吃过路兵
	state, err := engine.ValidateMove(state, "alice", "move_made", movePayload(3, 4, 2, 3, ""))
	if err != nil {
		t.Fatalf("en passant failed: %v", err)
	}
	board := state["board"].([][]*piece)
	if board[2][3] == nil || board[2][3].Type != "pawn" || board[2][3].Color != "white" {
		t.Fatalf("expected white pawn on d6, got %+v", board[2][3])
	}
	if board[3][3] != nil {
		t.Fatalf("expected black pawn on d5 captured, got %+v", board[3][3])
	}
}

func TestEnginePromotion(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// 白兵 a7 准备升变，黑王在远处
	state["fen"] = "4k3/P7/8/8/8/8/8/4K3 w - - 0 1"
	// a7(1,0) -> a8(0,0) 升变为车
	state, err := engine.ValidateMove(state, "alice", "move_made", movePayload(1, 0, 0, 0, "r"))
	if err != nil {
		t.Fatalf("promotion failed: %v", err)
	}
	board := state["board"].([][]*piece)
	if board[0][0] == nil || board[0][0].Type != "rook" || board[0][0].Color != "white" {
		t.Fatalf("expected white rook on a8 after promotion, got %+v", board[0][0])
	}
}

func TestEngineFoolsMateCheckmate(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// 傻瓜将杀：1.f3 e5 2.g4 Qh4#
	seq := []struct {
		user           string
		fr, fc, tr, tc int
	}{
		{"alice", 6, 5, 5, 5}, // f2->f3
		{"bob", 1, 4, 3, 4},   // e7->e5
		{"alice", 6, 6, 4, 6}, // g2->g4
		{"bob", 0, 3, 4, 7},   // Qd8->h4
	}
	var err error
	for _, mv := range seq {
		state, err = engine.ValidateMove(state, mv.user, "move_made", movePayload(mv.fr, mv.fc, mv.tr, mv.tc, ""))
		if err != nil {
			t.Fatalf("move %v failed: %v", mv, err)
		}
	}
	winner, draw := engine.IsGameOver(state)
	if draw {
		t.Fatal("expected no draw in fool's mate")
	}
	if winner != "bob" {
		t.Fatalf("expected winner=bob (black), got %v", winner)
	}
}

func TestEngineStalemateDraw(t *testing.T) {
	engine := New()
	state, _ := engine.Init(twoPlayers())
	// 逼和局面：黑王 a8 无子可动且未被将军，白走后成逼和。
	// 白王 c6、白后 b6、黑王 a8，轮到黑走则逼和；这里白后 b7->b6 制造逼和。
	state["fen"] = "k7/1Q6/2K5/8/8/8/8/8 w - - 0 1"
	// 白后 b7(1,1) -> b6(2,1)，黑王 a8 被困但非将军 → 逼和
	state, err := engine.ValidateMove(state, "alice", "move_made", movePayload(1, 1, 2, 1, ""))
	if err != nil {
		t.Fatalf("stalemate move failed: %v", err)
	}
	winner, draw := engine.IsGameOver(state)
	if !draw {
		t.Fatalf("expected draw (stalemate), got winner=%v draw=%v", winner, draw)
	}
}
