package util

import (
	"errors"
	"fmt"
)

// 对局控制类动作（认输 / 求和 / 悔棋）的共享实现。
//
// 为什么放在 util：xiangqi 与 gobang 两个棋类引擎需要完全相同的
// 「双确认 + 超时视为拒绝 + 每局次数上限」语义，复制两份必然漂移。
// 这里只放与具体棋种无关的记账逻辑；真正「回退一手」由各引擎自己实现
// （象棋要还原被吃子与不吃的半步计数，五子棋要清空落子点）。
//
// state 字段（JSON 序列化后仍是 map/float64，下面统一做了类型归一化）：
//   - pendingDraw: {"from": userId, "at": unix 秒}  待响应的求和请求
//   - pendingUndo: {"from": userId, "at": unix 秒}  待响应的悔棋请求
//   - undoCount:   本局已成功悔棋次数
//
// 超时只在「下一次动作进入引擎时」惰性清理（不引入后台定时器）：
// 对局空闲时残留的 pending 字段不会影响任何判定，下一次动作会顺手清掉。

// 控制动作 state 字段名（导出给引擎与测试使用，避免字符串漂移）。
const (
	PendingDrawKey = "pendingDraw"
	PendingUndoKey = "pendingUndo"
	UndoCountKey   = "undoCount"
)

// 下面两个值是 1.9.4 task-122 的产品口径（服务端写死，客户端只做展示与提示）：
// 求和 60s 无响应视为拒绝、每局最多成功 3 次悔棋。若后续要做成可配置项
// （房间级 / Space 级设置），只需把这里换成读配置，两个引擎的调用点无需改动。
const (
	// DrawOfferTTLSeconds 求和请求有效期（秒）：超时视为拒绝，不阻断对局。
	DrawOfferTTLSeconds int64 = 60
	// MaxUndoPerGame 每局最多成功的悔棋次数。
	MaxUndoPerGame = 3
)

// 控制类动作的错误。handler 会把 err.Error() 原样放进响应 message，
// 因此这里的文案要能直接给客户端展示。
var (
	// ErrNoPendingOffer 没有待响应的请求（或已超时被清理）。
	ErrNoPendingOffer = errors.New("当前没有待处理的请求")
	// ErrOfferFromSelf 不能响应自己发起的请求（求和/悔棋都必须对手确认）。
	ErrOfferFromSelf = errors.New("不能响应自己发起的请求")
	// ErrOfferAlreadyOpen 已有待响应请求，同一时刻只允许一个。
	ErrOfferAlreadyOpen = errors.New("已有待处理的请求，请等待对手响应")
	// ErrUndoLimitReached 本局悔棋次数已用完。
	ErrUndoLimitReached = fmt.Errorf("本局悔棋次数已用完（每局最多 %d 次）", MaxUndoPerGame)
	// ErrNoMoveToUndo 没有可回退的棋步。
	ErrNoMoveToUndo = errors.New("当前没有可悔的棋步")
	// ErrNotLastMover 发起人不是上一手落子者：悔棋只能撤回自己刚下的那一手，
	// 不能借「悔棋」抹掉对手的落子。
	ErrNotLastMover = errors.New("只有上一手落子的玩家才能请求悔棋")
	// ErrNotSeatedPlayer 不在对局座位上。
	ErrNotSeatedPlayer = errors.New("不在对局座位上的玩家不能执行该操作")
)

// LastMover 返回对局历史中最后一手的落子者 userID（历史为空返回 ""）。
//
// 悔棋语义（1.9.4 task-122 修正后口径）：由「上一手落子的人」发起、撤回的是
// 自己那一手，由当前回合方（对手）确认；确认后轮次回到发起方重走。
// 因此 undo_request 必须校验 LastMover == 发起人，而不能用 currentPlayer 判断
// （currentPlayer 此时是等待对方确认的对手）。
func LastMover(history []map[string]interface{}) string {
	if len(history) == 0 {
		return ""
	}
	owner, _ := history[len(history)-1]["userId"].(string)
	return owner
}

// Int64FromState 把 state 里的数值（float64/int/int64/json.Number 风格）安全转 int64。
func Int64FromState(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	}
	return 0
}

// OfferFrom 返回 pending offer 的发起者；ok=false 表示没有请求。
func OfferFrom(state map[string]interface{}, key string) (string, bool) {
	raw, ok := state[key]
	if !ok || raw == nil {
		return "", false
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return "", false
	}
	from, _ := m["from"].(string)
	if from == "" {
		return "", false
	}
	return from, true
}

// OfferAgeSeconds 返回 pending offer 已经等待的秒数（无法解析时返回 0）。
func OfferAgeSeconds(state map[string]interface{}, key string, now int64) int64 {
	raw, ok := state[key]
	if !ok || raw == nil {
		return 0
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return 0
	}
	at := Int64FromState(m["at"])
	if at <= 0 || now <= at {
		return 0
	}
	return now - at
}

// SetOffer 写入一条待响应请求。
func SetOffer(state map[string]interface{}, key, from string, at int64) {
	state[key] = map[string]interface{}{"from": from, "at": at}
}

// ClearOffer 清空待响应请求（幂等）。
func ClearOffer(state map[string]interface{}, key string) {
	delete(state, key)
}

// ExpireOffer 惰性清理超时的待响应请求，返回是否发生了清理。
//
// 只对求和请求生效（DrawOfferTTLSeconds）；悔棋请求不设超时——
// 悔棋必须由对手显式响应，超时自动撤回会让「对手挂机」变成既成事实。
func ExpireOffer(state map[string]interface{}, key string, now int64) bool {
	if _, ok := OfferFrom(state, key); !ok {
		return false
	}
	if key != PendingDrawKey {
		return false
	}
	if OfferAgeSeconds(state, key, now) < DrawOfferTTLSeconds {
		return false
	}
	delete(state, key)
	return true
}

// UndoCount 返回本局已成功悔棋次数。
func UndoCount(state map[string]interface{}) int {
	return int(Int64FromState(state[UndoCountKey]))
}

// BumpUndoCount 累加并写回本局成功悔棋次数，返回累加后的值。
func BumpUndoCount(state map[string]interface{}) int {
	n := UndoCount(state) + 1
	state[UndoCountKey] = n
	return n
}

// OpponentOf 返回 1v1 对局中 userID 的对手；不在座位上返回空串。
func OpponentOf(players []string, userID string) string {
	for i, id := range players {
		if id != userID {
			continue
		}
		if len(players) != 2 {
			return ""
		}
		return players[(i+1)%2]
	}
	return ""
}

// MarkResigned 把「userID 认输」写入 state：对手获胜、endReason=resign。
//
// 不直接改 session.Status：handler 会读 engine.IsGameOver 统一结算
// （状态置 ended + 写 minigame_history + 广播），保证三条终局路径同一出口。
func MarkResigned(state map[string]interface{}, players []string, userID string) error {
	opponent := OpponentOf(players, userID)
	if opponent == "" {
		return ErrNotSeatedPlayer
	}
	state["winner"] = opponent
	state["draw"] = false
	state["endReason"] = "resign"
	state["currentPlayer"] = ""
	delete(state, PendingDrawKey)
	delete(state, PendingUndoKey)
	return nil
}

// MarkDrawAgreed 把「双方同意和棋」写入 state。
//
// 同时写 draw=true 与 winner="draw"：象棋引擎 IsGameOver 认 draw 布尔，
// 五子棋认 winner=="draw"，两边都能正确结算，无需引擎各自分支。
func MarkDrawAgreed(state map[string]interface{}) {
	state["draw"] = true
	state["winner"] = "draw"
	state["endReason"] = "draw_agreed"
	state["currentPlayer"] = ""
	delete(state, PendingDrawKey)
	delete(state, PendingUndoKey)
}
