// Package gobang 实现五子棋（15x15 Gomoku）的权威规则引擎。
//
// action 协议（1.9.4 task-122 补齐对局控制）：
//   - move_made:    payload {"r":..,"c":..}，落子
//   - resign:       认输，对手获胜，endReason=resign
//   - draw_offer / draw_accept / draw_reject：求和双确认（60s 无响应视为拒绝）
//   - undo_request / undo_accept / undo_reject：悔棋双确认（每局最多 3 次成功）
//
// 控制动作的共享记账（pending 字段、超时、次数上限、终局标记）在
// features/minigames/internal/util/control.go；本包只负责棋盘回退。
package gobang

import (
	"encoding/json"
	"time"

	"ridgericetalk/features/minigames/internal/util"
)

const gobangSize = 15

// movesKey 落子历史在 state 中的字段名（悔棋回退依赖它）。
const movesKey = "moves"

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
	state[movesKey] = []interface{}{}
	return state, nil
}

// ValidateMove 按 action 分发：落子走棋，控制类动作走 resign/draw/undo 分支。
func (e *Engine) ValidateMove(state map[string]interface{}, userID string, action string, payload json.RawMessage) (map[string]interface{}, error) {
	// 复制入参：控制动作会就地改 pending/undoCount 字段，不能污染调用方手里的旧 state。
	state = util.ShallowCopy(state)
	now := time.Now().Unix()
	// 求和请求超时惰性清理：60s 无响应视为拒绝（不阻断对局，不引入后台定时器）。
	util.ExpireOffer(state, util.PendingDrawKey, now)

	switch action {
	case "move_made":
		return e.applyMove(state, userID, payload)
	case "resign":
		return e.applyResign(state, userID)
	case "draw_offer", "draw_accept", "draw_reject":
		return e.applyDraw(state, userID, action, now)
	case "undo_request", "undo_accept", "undo_reject":
		return e.applyUndo(state, userID, action, now)
	default:
		return nil, util.ErrInvalidAction
	}
}

// applyMove 校验并执行一次落子。
func (e *Engine) applyMove(state map[string]interface{}, userID string, payload json.RawMessage) (map[string]interface{}, error) {
	currentPlayer, _ := state["currentPlayer"].(string)
	if currentPlayer != "" && currentPlayer != userID {
		return nil, util.ErrNotYourTurn
	}
	if w, _ := state["winner"].(string); w != "" {
		return nil, util.ErrInvalidAction
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
	if draw {
		// 满盘无五连必须写 "draw" 标记：IsGameOver 只认 winner=="draw"，
		// 否则棋盘下满后房间永久停在 playing（无历史、无计分）。
		newState["winner"] = "draw"
	} else {
		newState["winner"] = winner
	}
	newState["lastMove"] = map[string]int{"r": p.R, "c": p.C}
	// 记录落子历史（悔棋回退用）。只记公开信息，观战/重连无隐私问题。
	history := moveHistoryFromState(state)
	history = append(history, map[string]interface{}{
		"r": p.R, "c": p.C, "userId": userID, "color": color,
	})
	newState[movesKey] = history
	// 落子意味着局面推进，之前挂起的求和/悔棋请求自动作废。
	delete(newState, util.PendingDrawKey)
	delete(newState, util.PendingUndoKey)
	return newState, nil
}

// applyResign 认输：对手获胜，交给 handler 统一结算（ended + 历史 + 广播）。
func (e *Engine) applyResign(state map[string]interface{}, userID string) (map[string]interface{}, error) {
	if w, _ := state["winner"].(string); w != "" {
		return nil, util.ErrInvalidAction
	}
	players := util.GetStringSlice(state, "players")
	if err := util.MarkResigned(state, players, userID); err != nil {
		return nil, err
	}
	return state, nil
}

// applyDraw 求和：发起 → 对手接受/拒绝，接受即和棋结算。
func (e *Engine) applyDraw(state map[string]interface{}, userID, action string, now int64) (map[string]interface{}, error) {
	players := util.GetStringSlice(state, "players")
	if !util.Contains(players, userID) {
		return nil, util.ErrNotSeatedPlayer
	}
	if w, _ := state["winner"].(string); w != "" {
		return nil, util.ErrInvalidAction
	}
	switch action {
	case "draw_offer":
		if from, ok := util.OfferFrom(state, util.PendingDrawKey); ok {
			if from == userID {
				return nil, util.ErrOfferAlreadyOpen
			}
			return nil, util.ErrOfferAlreadyOpen
		}
		util.SetOffer(state, util.PendingDrawKey, userID, now)
		return state, nil
	case "draw_accept":
		from, ok := util.OfferFrom(state, util.PendingDrawKey)
		if !ok {
			return nil, util.ErrNoPendingOffer
		}
		if from == userID {
			return nil, util.ErrOfferFromSelf
		}
		util.MarkDrawAgreed(state)
		return state, nil
	case "draw_reject":
		from, ok := util.OfferFrom(state, util.PendingDrawKey)
		if !ok {
			return nil, util.ErrNoPendingOffer
		}
		if from == userID {
			return nil, util.ErrOfferFromSelf
		}
		util.ClearOffer(state, util.PendingDrawKey)
		return state, nil
	}
	return nil, util.ErrInvalidAction
}

// applyUndo 悔棋：发起 → 对手接受/拒绝；接受回退最近一子并轮到发起方。
func (e *Engine) applyUndo(state map[string]interface{}, userID, action string, now int64) (map[string]interface{}, error) {
	players := util.GetStringSlice(state, "players")
	if !util.Contains(players, userID) {
		return nil, util.ErrNotSeatedPlayer
	}
	if w, _ := state["winner"].(string); w != "" {
		return nil, util.ErrInvalidAction
	}
	switch action {
	case "undo_request":
		// 悔棋只能由「上一手落子的人」发起：撤回的是自己那一手，由当前回合方
		// （对手）确认；不能用 currentPlayer 判断（那时已经是等待确认的对手）。
		history := moveHistoryFromState(state)
		if len(history) == 0 {
			return nil, util.ErrNoMoveToUndo
		}
		if util.LastMover(history) != userID {
			return nil, util.ErrNotLastMover
		}
		if _, ok := util.OfferFrom(state, util.PendingUndoKey); ok {
			return nil, util.ErrOfferAlreadyOpen
		}
		if util.UndoCount(state) >= util.MaxUndoPerGame {
			return nil, util.ErrUndoLimitReached
		}
		util.SetOffer(state, util.PendingUndoKey, userID, now)
		return state, nil
	case "undo_reject":
		from, ok := util.OfferFrom(state, util.PendingUndoKey)
		if !ok {
			return nil, util.ErrNoPendingOffer
		}
		if from == userID {
			return nil, util.ErrOfferFromSelf
		}
		util.ClearOffer(state, util.PendingUndoKey)
		return state, nil
	case "undo_accept":
		from, ok := util.OfferFrom(state, util.PendingUndoKey)
		if !ok {
			return nil, util.ErrNoPendingOffer
		}
		if from == userID {
			return nil, util.ErrOfferFromSelf
		}
		if util.UndoCount(state) >= util.MaxUndoPerGame {
			return nil, util.ErrUndoLimitReached
		}
		return e.revertLastMove(state, from)
	}
	return nil, util.ErrInvalidAction
}

// revertLastMove 回退最近一子：清空落点、轮到发起方、清掉挂起请求。
//
// 语义：回退「最近一手」= 撤销对手刚走的那一子；currentPlayer 显式交还发起方，
// 保证客户端「悔棋后轮到我重新走」的预期（见 task-122 验收）。
func (e *Engine) revertLastMove(state map[string]interface{}, requester string) (map[string]interface{}, error) {
	history := moveHistoryFromState(state)
	if len(history) == 0 {
		return nil, util.ErrNoMoveToUndo
	}
	last := history[len(history)-1]
	r := int(util.Int64FromState(last["r"]))
	c := int(util.Int64FromState(last["c"]))
	if !inBounds(r, c, gobangSize) {
		return nil, util.ErrInvalidAction
	}
	board := e.boardFromState(state)
	nb := make([][]interface{}, len(board))
	for i := range board {
		nb[i] = make([]interface{}, len(board[i]))
		copy(nb[i], board[i])
	}
	nb[r][c] = nil

	newState := util.ShallowCopy(state)
	newState["board"] = nb
	newState[movesKey] = history[:len(history)-1]
	newState["currentPlayer"] = requester
	newState["winner"] = nil
	if len(history) > 1 {
		prev := history[len(history)-2]
		newState["lastMove"] = map[string]int{
			"r": int(util.Int64FromState(prev["r"])),
			"c": int(util.Int64FromState(prev["c"])),
		}
	} else {
		newState["lastMove"] = nil
	}
	util.BumpUndoCount(newState)
	delete(newState, util.PendingUndoKey)
	delete(newState, util.PendingDrawKey)
	return newState, nil
}

// moveHistoryFromState 归一化 state 里的落子历史（JSON 反序列化后是 []interface{}）。
func moveHistoryFromState(state map[string]interface{}) []map[string]interface{} {
	raw, ok := state[movesKey]
	if !ok || raw == nil {
		return nil
	}
	switch h := raw.(type) {
	case []map[string]interface{}:
		return h
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(h))
		for _, item := range h {
			if m, ok := item.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func (e *Engine) IsGameOver(state map[string]interface{}) (winnerID string, draw bool) {
	winner := ""
	if w, ok := state["winner"].(string); ok {
		winner = w
	}
	if winner == "" {
		return "", false
	}
	if winner == "draw" {
		return "", true
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
