// Package ludo 实现简化版飞行棋（Ludo）多人权威规则引擎。
// 支持 2-4 名玩家，每人 4 架飞机，需绕棋盘一圈抵达终点。
package ludo

import (
	"encoding/json"
	"math/rand"

	"ridgericetalk/features/minigames/internal/util"
)

// Engine 实现简化版飞行棋权威规则。
type Engine struct{}

// New 创建一个飞行棋引擎实例。
func New() *Engine {
	return &Engine{}
}

type ludoColor string

const (
	ludoRed    ludoColor = "red"
	ludoBlue   ludoColor = "blue"
	ludoGreen  ludoColor = "green"
	ludoYellow ludoColor = "yellow"
)

var ludoColors = []ludoColor{ludoRed, ludoBlue, ludoGreen, ludoYellow}

func (e *Engine) Init(players []map[string]interface{}) (map[string]interface{}, error) {
	ids := util.PlayerUserIDs(players)
	playerColors := make(map[string]string)
	for i, id := range ids {
		playerColors[id] = string(ludoColors[i%len(ludoColors)])
	}

	state := make(map[string]interface{})
	state["status"] = "playing"
	state["players"] = ids
	state["currentPlayer"] = ids[0]
	state["winner"] = nil
	state["playerColors"] = playerColors
	state["positions"] = e.initialPositions(ids)
	state["lastRoll"] = 0
	state["rolled"] = false
	return state, nil
}

func (e *Engine) ValidateMove(state map[string]interface{}, userID string, action string, payload json.RawMessage) (map[string]interface{}, error) {
	currentPlayer, _ := state["currentPlayer"].(string)
	if currentPlayer != "" && currentPlayer != userID {
		return nil, util.ErrNotYourTurn
	}

	switch action {
	case "roll_dice":
		roll := e.rollDie()
		newState := util.ShallowCopy(state)
		newState["lastRoll"] = roll
		newState["rolled"] = true
		if !e.canMoveAny(newState, userID, roll) {
			newState["rolled"] = false
			newState["currentPlayer"] = e.nextPlayer(newState, userID)
		}
		return newState, nil
	case "move_pawn":
		rolled, _ := state["rolled"].(bool)
		if !rolled {
			return nil, util.ErrInvalidAction
		}
		// lastRoll may be int (fresh) or float64 (after JSON round-trip)
		var roll int
		switch v := state["lastRoll"].(type) {
		case int:
			roll = v
		case float64:
			roll = int(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				roll = int(n)
			}
		}
		var p struct{ PawnIndex int `json:"pawnIndex"` }
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, util.ErrInvalidAction
		}
		if p.PawnIndex < 0 || p.PawnIndex >= 4 {
			return nil, util.ErrInvalidAction
		}
		newState := util.ShallowCopy(state)
		if !e.movePawn(newState, userID, p.PawnIndex, roll) {
			return nil, util.ErrInvalidAction
		}
		newState["rolled"] = false
		if roll != 6 {
			newState["currentPlayer"] = e.nextPlayer(newState, userID)
		}
		if winner := e.checkWinner(newState); winner != "" {
			newState["winner"] = winner
		}
		return newState, nil
	default:
		return nil, util.ErrInvalidAction
	}
}

func (e *Engine) IsGameOver(state map[string]interface{}) (winnerID string, draw bool) {
	winner := ""
	if w, ok := state["winner"].(string); ok {
		winner = w
	}
	return winner, false
}

func (e *Engine) initialPositions(players []string) map[string][]int {
	positions := make(map[string][]int)
	for _, id := range players {
		positions[id] = []int{-1, -1, -1, -1}
	}
	return positions
}

func (e *Engine) rollDie() int {
	// 修复 P0 bug：原实现硬编码 return 1，导致玩家永远掷不到 6，飞机无法起飞。
	// 改为随机 1-6 的骰子点数。
	return rand.Intn(6) + 1
}

func (e *Engine) canMoveAny(state map[string]interface{}, userID string, roll int) bool {
	positions := e.positions(state, userID)
	for i, pos := range positions {
		if e.isValidMove(state, userID, i, pos, roll) {
			return true
		}
	}
	return false
}

func (e *Engine) isValidMove(state map[string]interface{}, userID string, pawnIndex, pos, roll int) bool {
	if pos == -1 {
		return roll == 6
	}
	return pos+roll <= 56
}

func (e *Engine) movePawn(state map[string]interface{}, userID string, pawnIndex, roll int) bool {
	positionsMap := e.positionsMap(state)
	positions := positionsMap[userID]
	pos := positions[pawnIndex]
	if !e.isValidMove(state, userID, pawnIndex, pos, roll) {
		return false
	}
	if pos == -1 {
		positions[pawnIndex] = 0
	} else {
		positions[pawnIndex] = pos + roll
	}
	positionsMap[userID] = positions
	state["positions"] = positionsMap

	// Knock out opponents on the same global position.
	myGlobal := e.playerStart(userID, state) + positions[pawnIndex]
	players := util.GetStringSlice(state, "players")
	for _, other := range players {
		if other == userID {
			continue
		}
		otherPositions := positionsMap[other]
		for i, op := range otherPositions {
			if op < 0 {
				continue
			}
			otherGlobal := e.playerStart(other, state) + op
			if otherGlobal%52 == myGlobal%52 {
				otherPositions[i] = -1
			}
		}
		positionsMap[other] = otherPositions
	}
	state["positions"] = positionsMap
	return true
}

func (e *Engine) playerStart(userID string, state map[string]interface{}) int {
	// playerColors may be map[string]string (fresh) or map[string]interface{} (after JSON)
	color := ""
	if colors, ok := state["playerColors"].(map[string]string); ok {
		color = colors[userID]
	} else if raw, ok := state["playerColors"].(map[string]interface{}); ok {
		if v, ok := raw[userID].(string); ok {
			color = v
		}
	}
	switch color {
	case "red":
		return 0
	case "blue":
		return 13
	case "green":
		return 26
	case "yellow":
		return 39
	}
	return 0
}

func (e *Engine) nextPlayer(state map[string]interface{}, userID string) string {
	players := util.GetStringSlice(state, "players")
	return util.NextPlayer(players, userID)
}

func (e *Engine) positions(state map[string]interface{}, userID string) []int {
	m := e.positionsMap(state)
	if p, ok := m[userID]; ok {
		return p
	}
	return []int{-1, -1, -1, -1}
}

func (e *Engine) positionsMap(state map[string]interface{}) map[string][]int {
	// Fast path: fresh from Init (no JSON round-trip)
	if m, ok := state["positions"].(map[string][]int); ok {
		return m
	}
	// Slow path: after JSON round-trip, becomes map[string]interface{}
	// where each value is []interface{} of float64.
	raw, ok := state["positions"].(map[string]interface{})
	if !ok {
		return make(map[string][]int)
	}
	out := make(map[string][]int, len(raw))
	for uid, v := range raw {
		arr, ok := v.([]interface{})
		if !ok {
			out[uid] = []int{-1, -1, -1, -1}
			continue
		}
		pos := make([]int, len(arr))
		for i, x := range arr {
			if f, ok := x.(float64); ok {
				pos[i] = int(f)
			} else if n, ok := x.(int); ok {
				pos[i] = n
			} else if n, ok := x.(json.Number); ok {
				if v, err := n.Int64(); err == nil {
					pos[i] = int(v)
				}
			}
		}
		out[uid] = pos
	}
	return out
}

func (e *Engine) checkWinner(state map[string]interface{}) string {
	positions := e.positionsMap(state)
	for uid, pos := range positions {
		allHome := true
		for _, p := range pos {
			if p != 56 {
				allHome = false
				break
			}
		}
		if allHome {
			return uid
		}
	}
	return ""
}
