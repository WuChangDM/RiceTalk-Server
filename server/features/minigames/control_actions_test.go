package minigames

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ridgericetalk/features/minigames/gobang"
	"ridgericetalk/features/minigames/internal/util"
	"ridgericetalk/features/minigames/xiangqi"
)

// 本文件覆盖 1.9.4 task-122：联机棋类（象棋 / 五子棋）的对局控制动作。
//
// 只测引擎层（ValidateMove / IsGameOver），不经过 HTTP 与数据库：
// 控制动作的规则语义（双确认、超时、次数上限、回退盘面）全在引擎里，
// handler 只负责持久化 + 结算 + 广播（见 control_history_test.go）。
// 两个棋类引擎共用同一套控制协议，因此除了「回退盘面」用例外都表驱动跑两遍。

// ctlEngine 一个待测引擎。
type ctlEngine struct {
	name string
	eng  GameEngine
}

// ctlEngines 返回参与控制动作测试的两个棋类引擎。
func ctlEngines() []ctlEngine {
	return []ctlEngine{
		{name: "xiangqi", eng: xiangqi.New()},
		{name: "gobang", eng: gobang.New()},
	}
}

// ctlInit 初始化一局 alice（先手）/ bob 的对局。
func ctlInit(t *testing.T, eng GameEngine) map[string]interface{} {
	t.Helper()
	state, err := eng.Init([]map[string]interface{}{
		{"userId": "alice", "username": "Alice"},
		{"userId": "bob", "username": "Bob"},
	})
	if err != nil {
		t.Fatalf("初始化对局失败: %v", err)
	}
	return state
}

// ctlAct 以指定用户身份执行一个动作。
func ctlAct(t *testing.T, eng GameEngine, state map[string]interface{}, user, action string, payload interface{}) (map[string]interface{}, error) {
	t.Helper()
	raw := json.RawMessage("{}")
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化 payload 失败: %v", err)
		}
		raw = b
	}
	return eng.ValidateMove(state, user, action, raw)
}

// ctlMustAct 执行动作并要求成功。
func ctlMustAct(t *testing.T, eng GameEngine, state map[string]interface{}, user, action string, payload interface{}) map[string]interface{} {
	t.Helper()
	next, err := ctlAct(t, eng, state, user, action, payload)
	if err != nil {
		t.Fatalf("%s 执行 %s 应成功，实际失败: %v", user, action, err)
	}
	return next
}

// ctlMustFail 执行动作并要求失败，且错误文案包含 want。
func ctlMustFail(t *testing.T, eng GameEngine, state map[string]interface{}, user, action string, want string) {
	t.Helper()
	_, err := ctlAct(t, eng, state, user, action, nil)
	if err == nil {
		t.Fatalf("%s 执行 %s 应被拒绝，实际成功", user, action)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s 执行 %s 的错误文案应包含 %q，实际: %v", user, action, want, err)
	}
}

// ctlJSON 把 state 做一次 JSON 往返，模拟「落库后从数据库读回」的形态
// （board 会从 [][]*piece / [][]interface{} 变成 []interface{}，数值变 float64）。
func ctlJSON(t *testing.T, state map[string]interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("序列化 state 失败: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("反序列化 state 失败: %v", err)
	}
	return out
}

// ctlCell 读取棋盘 (x, y) 位置的单元格（JSON 往返后的形态）。
func ctlCell(t *testing.T, state map[string]interface{}, x, y int) interface{} {
	t.Helper()
	rows, ok := state["board"].([]interface{})
	if !ok {
		t.Fatalf("state.board 不是数组: %T", state["board"])
	}
	if y < 0 || y >= len(rows) {
		t.Fatalf("棋盘行越界: y=%d, rows=%d", y, len(rows))
	}
	row, ok := rows[y].([]interface{})
	if !ok {
		t.Fatalf("state.board[%d] 不是数组: %T", y, rows[y])
	}
	if x < 0 || x >= len(row) {
		t.Fatalf("棋盘列越界: x=%d, cols=%d", x, len(row))
	}
	return row[x]
}

// ctlPiece 读取象棋 (x, y) 处的棋子类型与颜色；空位返回 ok=false。
func ctlPiece(t *testing.T, state map[string]interface{}, x, y int) (typ, color string, ok bool) {
	t.Helper()
	cell := ctlCell(t, state, x, y)
	if cell == nil {
		return "", "", false
	}
	m, ok := cell.(map[string]interface{})
	if !ok {
		t.Fatalf("象棋棋子不是对象: %T", cell)
	}
	typ, _ = m["type"].(string)
	color, _ = m["color"].(string)
	return typ, color, true
}

// ctlStr 读取 state 中的字符串字段。
func ctlStr(state map[string]interface{}, key string) string {
	s, _ := state[key].(string)
	return s
}

// ctlUndoCount 读取 state 中的悔棋次数。
func ctlUndoCount(state map[string]interface{}) int64 {
	return util.Int64FromState(state[util.UndoCountKey])
}

// ctlAssertNoPending 断言没有挂起的求和/悔棋请求。
func ctlAssertNoPending(t *testing.T, state map[string]interface{}) {
	t.Helper()
	if _, ok := util.OfferFrom(state, util.PendingDrawKey); ok {
		t.Fatalf("求和请求应已清空，实际仍挂起")
	}
	if _, ok := util.OfferFrom(state, util.PendingUndoKey); ok {
		t.Fatalf("悔棋请求应已清空，实际仍挂起")
	}
}

// TestControlResignEndsGameForBothEngines 认输：对手获胜、endReason=resign、对局结束。
func TestControlResignEndsGameForBothEngines(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			next := ctlMustAct(t, te.eng, state, "alice", "resign", nil)

			winner, draw := te.eng.IsGameOver(next)
			if winner != "bob" || draw {
				t.Fatalf("alice 认输后应判 bob 获胜，实际 winner=%q draw=%v", winner, draw)
			}
			if got := ctlStr(next, "endReason"); got != "resign" {
				t.Fatalf("endReason 应为 resign，实际 %q", got)
			}
		})
	}
}

// TestControlResignRejectsSpectator 不在座位的玩家不能认输。
func TestControlResignRejectsSpectator(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			ctlMustFail(t, te.eng, state, "carol", "resign", "座位")
		})
	}
}

// TestControlDrawAcceptedIsDraw 求和双确认：接受即和棋。
func TestControlDrawAcceptedIsDraw(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			state = ctlMustAct(t, te.eng, state, "alice", "draw_offer", nil)
			state = ctlMustAct(t, te.eng, state, "bob", "draw_accept", nil)

			winner, draw := te.eng.IsGameOver(state)
			if !draw || winner != "" {
				t.Fatalf("接受求和后应为和棋，实际 winner=%q draw=%v", winner, draw)
			}
			if got := ctlStr(state, "endReason"); got != "draw_agreed" {
				t.Fatalf("endReason 应为 draw_agreed，实际 %q", got)
			}
			ctlAssertNoPending(t, state)
		})
	}
}

// TestControlDrawRejectedKeepsPlaying 拒绝求和：对局继续，挂起请求清空。
func TestControlDrawRejectedKeepsPlaying(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			state = ctlMustAct(t, te.eng, state, "alice", "draw_offer", nil)
			state = ctlMustAct(t, te.eng, state, "bob", "draw_reject", nil)

			if winner, draw := te.eng.IsGameOver(state); draw || winner != "" {
				t.Fatalf("拒绝求和后对局应继续，实际 winner=%q draw=%v", winner, draw)
			}
			ctlAssertNoPending(t, state)
		})
	}
}

// TestControlDrawOfferExpiresAfter60Seconds 求和请求 60s 无响应视为拒绝：
// 超时后对手再接受必须报「没有待处理的请求」（惰性清理在动作入口触发）。
func TestControlDrawOfferExpiresAfter60Seconds(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			// 手工构造一个 61 秒前发起的求和请求，模拟对手长时间未响应。
			state[util.PendingDrawKey] = map[string]interface{}{
				"from": "alice",
				"at":   float64(time.Now().Unix() - util.DrawOfferTTLSeconds - 1),
			}
			ctlMustFail(t, te.eng, state, "bob", "draw_accept", "没有待处理")
		})
	}
}

// TestControlDrawSelfAcceptRejected 不能响应自己发起的求和。
func TestControlDrawSelfAcceptRejected(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			state = ctlMustAct(t, te.eng, state, "alice", "draw_offer", nil)
			ctlMustFail(t, te.eng, state, "alice", "draw_accept", "自己发起")
		})
	}
}

// TestControlDrawDuplicateOfferRejected 同一时刻只允许一个挂起请求。
func TestControlDrawDuplicateOfferRejected(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			state = ctlMustAct(t, te.eng, state, "alice", "draw_offer", nil)
			ctlMustFail(t, te.eng, state, "alice", "draw_offer", "已有待处理")
		})
	}
}

// TestControlMoveClearsPendingOffer 局面推进（落子）后挂起的求和请求自动作废。
func TestControlMoveClearsPendingOffer(t *testing.T) {
	eng := gobang.New()
	state := ctlInit(t, eng)
	state = ctlMustAct(t, eng, state, "alice", "move_made", map[string]int{"r": 7, "c": 7})
	state = ctlMustAct(t, eng, state, "bob", "draw_offer", nil)
	state = ctlMustAct(t, eng, state, "bob", "move_made", map[string]int{"r": 0, "c": 0})

	ctlAssertNoPending(t, state)
}

// TestControlXiangqiUndoRestoresCapturedPiece 象棋悔棋：回退一个半步，
// 被吃子还原、不吃半步计数与走子数回滚、行棋权交还发起方、不写历史。
func TestControlXiangqiUndoRestoresCapturedPiece(t *testing.T) {
	eng := xiangqi.New()
	state := ctlInit(t, eng)

	// 红炮 (1,7) 隔黑炮 (1,2) 打掉黑马 (1,0)：一步合法吃子。
	state = ctlMustAct(t, eng, state, "alice", "move_made", map[string]interface{}{
		"from": map[string]int{"x": 1, "y": 7},
		"to":   map[string]int{"x": 1, "y": 0},
	})
	js := ctlJSON(t, state)
	if typ, color, ok := ctlPiece(t, js, 1, 0); !ok || typ != "cannon" || color != "red" {
		t.Fatalf("吃子后 (1,0) 应为红炮，实际 typ=%q color=%q ok=%v", typ, color, ok)
	}
	if typ, color, ok := ctlPiece(t, js, 1, 7); ok {
		t.Fatalf("吃子后 (1,7) 应为空，实际 %q/%q", typ, color)
	}

	// 悔棋由「上一手落子方」发起：alice 刚走完，由 alice 请求、bob 确认。
	state = ctlMustAct(t, eng, state, "alice", "undo_request", nil)
	state = ctlMustAct(t, eng, state, "bob", "undo_accept", nil)

	js = ctlJSON(t, state)
	if typ, color, ok := ctlPiece(t, js, 1, 0); !ok || typ != "knight" || color != "black" {
		t.Fatalf("悔棋后 (1,0) 应还原为黑马，实际 typ=%q color=%q ok=%v", typ, color, ok)
	}
	if typ, color, ok := ctlPiece(t, js, 1, 7); !ok || typ != "cannon" || color != "red" {
		t.Fatalf("悔棋后 (1,7) 应还原为红炮，实际 typ=%q color=%q ok=%v", typ, color, ok)
	}
	if got := util.Int64FromState(js["moveCount"]); got != 0 {
		t.Fatalf("悔棋后走子数应回滚到 0，实际 %d", got)
	}
	if got := ctlStr(js, "currentPlayer"); got != "alice" {
		t.Fatalf("悔棋后应轮到发起方 alice 重走，实际 %q", got)
	}
	if got := ctlUndoCount(js); got != 1 {
		t.Fatalf("悔棋成功后计数应为 1，实际 %d", got)
	}
	ctlAssertNoPending(t, js)
}

// TestControlXiangqiUndoRejectedKeepsBoard 拒绝悔棋：盘面与计数都不变。
func TestControlXiangqiUndoRejectedKeepsBoard(t *testing.T) {
	eng := xiangqi.New()
	state := ctlInit(t, eng)
	state = ctlMustAct(t, eng, state, "alice", "move_made", map[string]interface{}{
		"from": map[string]int{"x": 1, "y": 7},
		"to":   map[string]int{"x": 1, "y": 0},
	})
	// alice 刚走完（上一手落子方），由 alice 请求、bob 拒绝。
	state = ctlMustAct(t, eng, state, "alice", "undo_request", nil)
	state = ctlMustAct(t, eng, state, "bob", "undo_reject", nil)

	js := ctlJSON(t, state)
	if typ, _, ok := ctlPiece(t, js, 1, 0); !ok || typ != "cannon" {
		t.Fatalf("拒绝悔棋后 (1,0) 应仍是红炮，实际 typ=%q ok=%v", typ, ok)
	}
	if got := ctlUndoCount(js); got != 0 {
		t.Fatalf("拒绝悔棋后计数应仍为 0，实际 %d", got)
	}
}

// TestControlGobangUndoRemovesLastStone 五子棋悔棋：清空最近一子并轮到发起方。
func TestControlGobangUndoRemovesLastStone(t *testing.T) {
	eng := gobang.New()
	state := ctlInit(t, eng)
	state = ctlMustAct(t, eng, state, "alice", "move_made", map[string]int{"r": 7, "c": 7})
	state = ctlMustAct(t, eng, state, "bob", "move_made", map[string]int{"r": 0, "c": 0})
	// bob 是上一手落子方：由 bob 请求撤回自己那一手、alice 确认。
	state = ctlMustAct(t, eng, state, "bob", "undo_request", nil)
	state = ctlMustAct(t, eng, state, "alice", "undo_accept", nil)

	js := ctlJSON(t, state)
	if got := ctlCell(t, js, 0, 0); got != nil {
		t.Fatalf("悔棋后 (0,0) 应被清空，实际 %v", got)
	}
	if got := ctlCell(t, js, 7, 7); got != "black" {
		t.Fatalf("悔棋不应影响 alice 的黑子，实际 %v", got)
	}
	if got := ctlStr(js, "currentPlayer"); got != "bob" {
		t.Fatalf("悔棋后应轮到发起方 bob 重走，实际 %q", got)
	}
	if got := ctlUndoCount(js); got != 1 {
		t.Fatalf("悔棋成功后计数应为 1，实际 %d", got)
	}
}

// TestControlUndoLimitPerGame 每局最多 3 次悔棋，第 4 次发起必须被拒绝。
func TestControlUndoLimitPerGame(t *testing.T) {
	eng := gobang.New()
	state := ctlInit(t, eng)
	mover := "alice"
	other := "bob"
	for i := 0; i < util.MaxUndoPerGame; i++ {
		// 同一方反复「走子 → 自己请求悔棋 → 对手确认」；悔棋后轮次回到 mover。
		state = ctlMustAct(t, eng, state, mover, "move_made", map[string]int{"r": 7, "c": 7})
		state = ctlMustAct(t, eng, state, mover, "undo_request", nil)
		state = ctlMustAct(t, eng, state, other, "undo_accept", nil)
	}
	if got := ctlUndoCount(state); int(got) != util.MaxUndoPerGame {
		t.Fatalf("悔棋计数应为 %d，实际 %d", util.MaxUndoPerGame, got)
	}

	state = ctlMustAct(t, eng, state, mover, "move_made", map[string]int{"r": 7, "c": 7})
	ctlMustFail(t, eng, state, mover, "undo_request", "次数已用完")
}

// TestControlUndoWithoutMovesRejected 没有棋步时不能发起悔棋。
func TestControlUndoWithoutMovesRejected(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			ctlMustFail(t, te.eng, state, "alice", "undo_request", "没有可悔")
		})
	}
}

// TestControlUndoAcceptByOffererRejected 发起方不能自己接受悔棋请求。
func TestControlUndoAcceptByOffererRejected(t *testing.T) {
	eng := gobang.New()
	state := ctlInit(t, eng)
	state = ctlMustAct(t, eng, state, "alice", "move_made", map[string]int{"r": 7, "c": 7})
	state = ctlMustAct(t, eng, state, "alice", "undo_request", nil)
	ctlMustFail(t, eng, state, "alice", "undo_accept", "自己发起")
}

// TestControlUndoRequestByLastMover 悔棋由「上一手落子方」发起、对手确认：
// 撤回的是发起方自己那一手，确认后轮次回到发起方重走。
func TestControlUndoRequestByLastMover(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			state = ctlMoveFirst(t, te, state, "alice")

			// alice 刚走完，当前回合是 bob；alice 作为上一手落子方可以发起悔棋。
			state = ctlMustAct(t, te.eng, state, "alice", "undo_request", nil)
			js := ctlJSON(t, state)
			if from, ok := util.OfferFrom(js, util.PendingUndoKey); !ok || from != "alice" {
				t.Fatalf("发起悔棋后应挂起来自 alice 的请求，实际 from=%q ok=%v", from, ok)
			}
			if got := ctlStr(js, "currentPlayer"); got != "bob" {
				t.Fatalf("发起悔棋后仍应由 bob 决定（currentPlayer=bob），实际 %q", got)
			}
			// 等待期间 alice 不能再发起第二次（同一时刻只允许一个挂起请求）。
			ctlMustFail(t, te.eng, state, "alice", "undo_request", "已有待处理")

			state = ctlMustAct(t, te.eng, state, "bob", "undo_accept", nil)
			js = ctlJSON(t, state)
			if got := ctlStr(js, "currentPlayer"); got != "alice" {
				t.Fatalf("确认悔棋后应轮到发起方 alice 重走，实际 %q", got)
			}
			if got := ctlUndoCount(js); got != 1 {
				t.Fatalf("悔棋成功后计数应为 1，实际 %d", got)
			}
			ctlAssertNoPending(t, js)
		})
	}
}

// TestControlUndoRequestRejectedWhenNotLastMover 不是上一手落子方不能发起悔棋
// （否则等于「取消对手的落子」，棋力不公平）；观战者同样不能发起。
func TestControlUndoRequestRejectedWhenNotLastMover(t *testing.T) {
	for _, te := range ctlEngines() {
		t.Run(te.name, func(t *testing.T) {
			state := ctlInit(t, te.eng)
			state = ctlMoveFirst(t, te, state, "alice")

			// 上一手是 alice 走的，bob 不能请求撤回 alice 那一手。
			ctlMustFail(t, te.eng, state, "bob", "undo_request", "上一手落子")
			// 观战者（不在座位）连发起资格都没有。
			ctlMustFail(t, te.eng, state, "carol", "undo_request", "不在对局座位上")
		})
	}
}

// ctlMoveFirst 让指定玩家走一步（两个引擎各自的合法首步），返回新 state。
func ctlMoveFirst(t *testing.T, te ctlEngine, state map[string]interface{}, user string) map[string]interface{} {
	t.Helper()
	if te.name == "gobang" {
		return ctlMustAct(t, te.eng, state, user, "move_made", map[string]int{"r": 7, "c": 7})
	}
	return ctlMustAct(t, te.eng, state, user, "move_made", map[string]interface{}{
		"from": map[string]int{"x": 1, "y": 7},
		"to":   map[string]int{"x": 1, "y": 0},
	})
}

// TestControlActionsSurviveJSONRoundTrip 控制动作在 state 落库再读回（JSON 往返）
// 后仍然生效：board 变 []interface{}、数值变 float64 都不能影响判定。
func TestControlActionsSurviveJSONRoundTrip(t *testing.T) {
	eng := xiangqi.New()
	state := ctlInit(t, eng)
	state = ctlMustAct(t, eng, state, "alice", "move_made", map[string]interface{}{
		"from": map[string]int{"x": 1, "y": 7},
		"to":   map[string]int{"x": 1, "y": 0},
	})
	// 模拟持久化往返。
	state = ctlJSON(t, state)
	state = ctlMustAct(t, eng, state, "alice", "undo_request", nil)
	state = ctlJSON(t, state)
	state = ctlMustAct(t, eng, state, "bob", "undo_accept", nil)

	js := ctlJSON(t, state)
	if typ, color, ok := ctlPiece(t, js, 1, 0); !ok || typ != "knight" || color != "black" {
		t.Fatalf("JSON 往返后悔棋应仍能还原黑马，实际 typ=%q color=%q ok=%v", typ, color, ok)
	}
	if got := ctlStr(js, "currentPlayer"); got != "alice" {
		t.Fatalf("JSON 往返后悔棋后应轮到发起方 alice 重走，实际 %q", got)
	}
	if got := ctlUndoCount(js); got != 1 {
		t.Fatalf("JSON 往返后悔棋计数应为 1，实际 %d", got)
	}
}
