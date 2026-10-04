// Package chess 实现国际象棋权威规则引擎。
// 完整规则（王车易位、吃过路兵、升变、将死、逼和、三次重复、50 步、子力不足）
// 由 corentings/chess 库提供；本文件仅做 GameEngine 接口适配与 state 序列化。
//
// state 权威字段：
//   - fen: 当前局面 FEN 字符串（断线重连/观战据此重建对局）
//   - board: 由 FEN 重渲染的 8x8 棋盘（[][]*piece，前端展示协议保持不变）
//   - currentPlayer / players / winner / draw / status
package chess

import (
	"encoding/json"
	"fmt"

	chesslib "github.com/corentings/chess"

	"ridgericetalk/features/minigames/internal/util"
)

// Engine 实现国际象棋权威规则（包装 corentings/chess）。
type Engine struct{}

// New 创建一个国际象棋引擎实例。
func New() *Engine {
	return &Engine{}
}

type piece struct {
	Type  string `json:"type"`
	Color string `json:"color"`
}

func (e *Engine) Init(players []map[string]interface{}) (map[string]interface{}, error) {
	ids := util.PlayerUserIDs(players)
	game := chesslib.NewGame(chesslib.UseNotation(chesslib.UCINotation{}))
	state := make(map[string]interface{})
	state["status"] = "playing"
	state["players"] = ids
	state["currentPlayer"] = ids[0] // players[0] 执白先行
	state["winner"] = nil
	state["draw"] = false
	state["fen"] = game.FEN()
	state["board"] = e.boardFromGame(game)
	return state, nil
}

func (e *Engine) ValidateMove(state map[string]interface{}, userID string, action string, payload json.RawMessage) (map[string]interface{}, error) {
	if action != "move_made" {
		return nil, util.ErrInvalidAction
	}
	currentPlayer, _ := state["currentPlayer"].(string)
	if currentPlayer != "" && currentPlayer != userID {
		return nil, util.ErrNotYourTurn
	}
	players := util.GetStringSlice(state, "players")

	// 由权威 FEN 重建对局（校验与规则判定全部交给 chess 库）。
	game, err := e.gameFromState(state)
	if err != nil {
		return nil, util.ErrInvalidAction
	}

	var p struct {
		From struct {
			R int `json:"r"`
			C int `json:"c"`
		} `json:"from"`
		To struct {
			R int `json:"r"`
			C int `json:"c"`
		} `json:"to"`
		Promotion string `json:"promotion"` // 可选：q|r|b|n，默认 q
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, util.ErrInvalidAction
	}
	if !inBounds(p.From.R, p.From.C, 8) || !inBounds(p.To.R, p.To.C, 8) {
		return nil, util.ErrInvalidAction
	}

	uci := squareToUCI(p.From.R, p.From.C) + squareToUCI(p.To.R, p.To.C)
	if promo := normalizePromo(p.Promotion); promo != "" {
		uci += promo
	}
	// 升变走法必须显式给出升变子；非升变走法不带后缀，库会拒绝不合法后缀。
	if err := game.MoveStr(uci); err != nil {
		return nil, util.ErrInvalidAction
	}

	winner := ""
	draw := false
	switch game.Outcome() {
	case chesslib.WhiteWon:
		if len(players) > 0 {
			winner = players[0]
		}
	case chesslib.BlackWon:
		if len(players) > 1 {
			winner = players[1]
		}
	case chesslib.Draw:
		draw = true
	}

	next := ""
	if winner == "" && !draw && len(players) > 0 {
		next = util.NextPlayer(players, userID)
	}

	newState := util.ShallowCopy(state)
	newState["fen"] = game.FEN()
	newState["board"] = e.boardFromGame(game)
	newState["currentPlayer"] = next
	newState["winner"] = winner
	newState["draw"] = draw
	return newState, nil
}

func (e *Engine) IsGameOver(state map[string]interface{}) (winnerID string, draw bool) {
	if d, ok := state["draw"].(bool); ok && d {
		return "", true
	}
	if w, ok := state["winner"].(string); ok && w != "" {
		return w, false
	}
	return "", false
}

// gameFromState 从 state 的权威 FEN 重建对局；无 FEN 时退回新对局。
func (e *Engine) gameFromState(state map[string]interface{}) (*chesslib.Game, error) {
	fen, _ := state["fen"].(string)
	if fen == "" {
		return chesslib.NewGame(chesslib.UseNotation(chesslib.UCINotation{})), nil
	}
	fenOpt, err := chesslib.FEN(fen)
	if err != nil {
		return nil, fmt.Errorf("invalid fen: %w", err)
	}
	return chesslib.NewGame(chesslib.UseNotation(chesslib.UCINotation{}), fenOpt), nil
}

// boardFromGame 把当前局面渲染为项目前端使用的 8x8 棋盘。
// 行 0 = 黑方底线（rank8），行 7 = 白方底线（rank1），列 0..7 = a..h。
func (e *Engine) boardFromGame(game *chesslib.Game) [][]*piece {
	board := make([][]*piece, 8)
	for r := 0; r < 8; r++ {
		board[r] = make([]*piece, 8)
	}
	sqMap := game.Position().Board().SquareMap()
	for sq, p := range sqMap {
		r := 7 - int(sq/8) // rank1(0) -> row7, rank8(7) -> row0
		c := int(sq % 8)
		if r < 0 || r > 7 || c < 0 || c > 7 {
			continue
		}
		board[r][c] = &piece{Type: pieceTypeName(p.Type()), Color: colorName(p.Color())}
	}
	return board
}

// squareToUCI 把 {r,c}（行 0=rank8，列 0=a）转成 UCI 坐标（如 e2）。
func squareToUCI(r, c int) string {
	file := string(rune('a' + c))
	rank := 8 - r
	return fmt.Sprintf("%s%d", file, rank)
}

// normalizePromo 归一化升变子为 UCI 后缀；非法值返回 ""（由库判定）。
func normalizePromo(p string) string {
	switch p {
	case "q", "r", "b", "n":
		return p
	case "queen":
		return "q"
	case "rook":
		return "r"
	case "bishop":
		return "b"
	case "knight":
		return "n"
	}
	return ""
}

func pieceTypeName(t chesslib.PieceType) string {
	switch t {
	case chesslib.King:
		return "king"
	case chesslib.Queen:
		return "queen"
	case chesslib.Rook:
		return "rook"
	case chesslib.Bishop:
		return "bishop"
	case chesslib.Knight:
		return "knight"
	case chesslib.Pawn:
		return "pawn"
	}
	return ""
}

func colorName(c chesslib.Color) string {
	if c == chesslib.White {
		return "white"
	}
	return "black"
}

func inBounds(r, c, size int) bool {
	return r >= 0 && r < size && c >= 0 && c < size
}
