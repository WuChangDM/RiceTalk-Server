// Package tictactoe 实现井字棋（3x3 Tic-Tac-Toe）的权威规则引擎。
//
// 引擎通过实现 minigames.GameEngine 接口（隐式实现，无需 import 根包）
// 提供落子合法性校验、回合推进、胜负判定能力。
// 共用的状态处理 helper 从 minigames/internal/util 导入。
package tictactoe

import (
	"encoding/json"

	"ridgericetalk/features/minigames/internal/util"
)

// Engine 实现井字棋权威规则。
type Engine struct{}

// New 创建一个井字棋引擎实例。
func New() *Engine {
	return &Engine{}
}

func (e *Engine) Init(players []map[string]interface{}) (map[string]interface{}, error) {
	ids := util.PlayerUserIDs(players)
	state := make(map[string]interface{})
	state["status"] = "playing"
	state["players"] = ids
	state["currentPlayer"] = ids[0]
	state["winner"] = nil
	state["board"] = make([]interface{}, 9)
	state["winLine"] = []int{}
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

	board, _ := state["board"].([]interface{})
	if board == nil {
		board = make([]interface{}, 9)
	}

	var p struct {
		Index  int    `json:"index"`
		Symbol string `json:"symbol"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, util.ErrInvalidAction
	}
	if p.Index < 0 || p.Index >= 9 || board[p.Index] != nil {
		return nil, util.ErrInvalidAction
	}

	players := util.GetStringSlice(state, "players")
	if players == nil {
		players = []string{}
	}

	symbol := p.Symbol
	if symbol == "" {
		if len(players) > 0 && players[0] == userID {
			symbol = "X"
		} else {
			symbol = "O"
		}
	}
	board[p.Index] = symbol

	winnerSymbol, line := e.calculateWinner(board)
	draw := winnerSymbol == "" && e.isBoardFull(board)

	next := ""
	if winnerSymbol == "" && !draw && len(players) > 0 {
		next = util.NextPlayer(players, userID)
	}

	// 修复 P0 bug：O 玩家获胜时 winnerID 被错误赋值为 X 玩家。
	// 正确逻辑：根据 winnerSymbol 直接映射到对应玩家（X=players[0], O=players[1]）。
	winnerID := ""
	if winnerSymbol != "" {
		if winnerSymbol == "X" && len(players) > 0 {
			winnerID = players[0]
		} else if winnerSymbol == "O" && len(players) > 1 {
			winnerID = players[1]
		}
	}

	newState := util.ShallowCopy(state)
	newState["board"] = board
	newState["currentPlayer"] = next
	newState["winner"] = winnerID
	newState["winLine"] = line
	return newState, nil
}

func (e *Engine) IsGameOver(state map[string]interface{}) (winnerID string, draw bool) {
	winner := ""
	if w, ok := state["winner"].(string); ok {
		winner = w
	}
	if winner == "draw" {
		return "", true
	}
	if winner == "" {
		return "", false
	}
	players := util.GetStringSlice(state, "players")
	for _, id := range players {
		if id == winner {
			return id, false
		}
	}
	return "", true
}

func (e *Engine) calculateWinner(board []interface{}) (string, []int) {
	lines := [][]int{
		{0, 1, 2}, {3, 4, 5}, {6, 7, 8},
		{0, 3, 6}, {1, 4, 7}, {2, 5, 8},
		{0, 4, 8}, {2, 4, 6},
	}
	for _, line := range lines {
		a, b, c := line[0], line[1], line[2]
		if board[a] != nil && board[a] == board[b] && board[a] == board[c] {
			if sym, ok := board[a].(string); ok {
				return sym, line
			}
		}
	}
	return "", nil
}

func (e *Engine) isBoardFull(board []interface{}) bool {
	for _, cell := range board {
		if cell == nil {
			return false
		}
	}
	return true
}
