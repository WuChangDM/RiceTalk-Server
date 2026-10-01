// Package gobang 实现五子棋（15x15 Gomoku）的权威规则引擎。
package gobang

import (
	"encoding/json"

	"ridgericetalk/features/minigames/internal/util"
)

const gobangSize = 15

// Engine 实现五子棋权威规则。
type Engine struct{}

// New 创建一个五子棋引擎实例。
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
	state["board"] = e.emptyBoard()
	state["lastMove"] = nil
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

	board := e.boardFromState(state)

	var p struct {
		R int `json:"r"`
		C int `json:"c"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, util.ErrInvalidAction
	}
	if !inBounds(p.R, p.C, gobangSize) || board[p.R][p.C] != nil {
		return nil, util.ErrInvalidAction
	}

	players := util.GetStringSlice(state, "players")
	color := "black"
	if len(players) > 1 && players[1] == userID {
		color = "white"
	}
	board[p.R][p.C] = color

	winner := ""
	if e.checkWinner(board, p.R, p.C, color) {
		winner = userID
	}
	draw := winner == "" && e.isBoardFull(board)

	next := ""
	if winner == "" && !draw && len(players) > 0 {
		next = util.NextPlayer(players, userID)
	}

	newState := util.ShallowCopy(state)
	newState["board"] = board
	newState["currentPlayer"] = next
	newState["winner"] = winner
	newState["lastMove"] = map[string]int{"r": p.R, "c": p.C}
	return newState, nil
}

func (e *Engine) IsGameOver(state map[string]interface{}) (winnerID string, draw bool) {
	winner := ""
	if w, ok := state["winner"].(string); ok {
		winner = w
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

func (e *Engine) emptyBoard() [][]interface{} {
	b := make([][]interface{}, gobangSize)
	for i := range b {
		b[i] = make([]interface{}, gobangSize)
	}
	return b
}

func (e *Engine) boardFromState(state map[string]interface{}) [][]interface{} {
	if b, ok := state["board"].([][]interface{}); ok {
		return b
	}
	if raw, ok := state["board"].([]interface{}); ok {
		b := make([][]interface{}, gobangSize)
		for r := 0; r < gobangSize && r < len(raw); r++ {
			row, _ := raw[r].([]interface{})
			if row == nil {
				row = make([]interface{}, gobangSize)
			}
			b[r] = row
		}
		for r := len(raw); r < gobangSize; r++ {
			b[r] = make([]interface{}, gobangSize)
		}
		return b
	}
	return e.emptyBoard()
}

func inBounds(r, c, size int) bool {
	return r >= 0 && r < size && c >= 0 && c < size
}

func (e *Engine) checkWinner(board [][]interface{}, r, c int, color string) bool {
	directions := [][2]int{{0, 1}, {1, 0}, {1, 1}, {1, -1}}
	for _, d := range directions {
		count := 1
		for step := 1; step < 5; step++ {
			nr, nc := r+d[0]*step, c+d[1]*step
			if !inBounds(nr, nc, gobangSize) || board[nr][nc] != color {
				break
			}
			count++
		}
		for step := 1; step < 5; step++ {
			nr, nc := r-d[0]*step, c-d[1]*step
			if !inBounds(nr, nc, gobangSize) || board[nr][nc] != color {
				break
			}
			count++
		}
		if count >= 5 {
			return true
		}
	}
	return false
}

func (e *Engine) isBoardFull(board [][]interface{}) bool {
	for _, row := range board {
		for _, cell := range row {
			if cell == nil {
				return false
			}
		}
	}
	return true
}
