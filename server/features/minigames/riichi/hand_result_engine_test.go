package riichi

import (
	"encoding/json"
	"testing"

	"ridgericetalk/features/minigames/riichi/core"
)

// engineIDs 返回引擎测试用的四名玩家 userId（座位=入座顺序）。
func engineIDs() []string { return []string{"alice", "bob", "carol", "dave"} }

// enginePlayers 构造引擎 Init 需要的玩家列表。
func enginePlayers() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, 4)
	for _, id := range engineIDs() {
		out = append(out, map[string]interface{}{"userId": id, "username": id})
	}
	return out
}

// viewsOf 取 state 的四个座位视角。
func viewsOf(t *testing.T, state map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := state["views"].([]interface{})
	if !ok {
		t.Fatalf("state.views 类型异常：%T", state["views"])
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for i, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			t.Fatalf("state.views[%d] 类型异常：%T", i, v)
		}
		out = append(out, m)
	}
	return out
}

// handIndexOf 取 state 的局次索引。
func handIndexOf(t *testing.T, state map[string]interface{}) int {
	t.Helper()
	switch v := state["handIndex"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			t.Fatalf("handIndex 解析失败：%v", err)
		}
		return int(i)
	}
	t.Fatalf("handIndex 类型异常：%T", state["handIndex"])
	return 0
}

// actOnce 从当前 state 的合法动作里挑第一个应用到引擎，返回新 state。
//
// 注意：合法动作只在「轮到行动的座位」的视角里存在（state.awaiting[0]），
// 开局是亲家（seat 0），之后可能是任一家（含鸣牌窗口中的多家），因此不能固定读 views[0]。
func actOnce(t *testing.T, e *Engine, ids []string, state map[string]interface{}) map[string]interface{} {
	t.Helper()
	views := viewsOf(t, state)
	seat := -1
	switch raw := state["awaiting"].(type) {
	case []int:
		if len(raw) > 0 {
			seat = raw[0]
		}
	case []interface{}:
		if len(raw) > 0 {
			if f, ok := raw[0].(float64); ok {
				seat = int(f)
			}
		}
	}
	if seat < 0 {
		t.Fatalf("state.awaiting 为空（对局可能已结束）：%v", state["awaiting"])
	}
	if seat >= len(ids) {
		t.Fatalf("awaiting 座位异常：%d", seat)
	}
	rawLegal, ok := views[seat]["legal_actions"].([]interface{})
	if !ok || len(rawLegal) == 0 {
		t.Fatalf("seat 0 没有合法动作（state 可能已异常）：%v", views[0]["legal_actions"])
	}
	act, ok := rawLegal[0].(map[string]interface{})
	if !ok {
		t.Fatalf("合法动作类型异常：%T", rawLegal[0])
	}
	kind, _ := act["kind"].(string)
	if actSeat, _ := act["seat"].(float64); int(actSeat) != seat {
		t.Fatalf("合法动作座位与 awaiting 不一致：act=%v awaiting=%d", act["seat"], seat)
	}
	payload := map[string]interface{}{}
	if tiles, ok := act["tiles"].([]interface{}); ok && len(tiles) > 0 {
		payload["tiles"] = tiles
	}
	if called, ok := act["called"].(string); ok && called != "" {
		payload["called"] = called
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("payload 序列化失败：%v", err)
	}
	next, err := e.ValidateMove(state, ids[seat], kind, raw)
	if err != nil {
		t.Fatalf("应用动作失败（seat=%d kind=%s payload=%s）：%v", seat, kind, raw, err)
	}
	return next
}

// TestStateCarriesHandResultAfterHandSettles 引擎层：普通和牌/流局后，四家与观战的
// state 都必须带 handResult（task-93 的核心回归，客户端 RiichiMahjongGame.tsx 读
// boardView.handResult 渲染结算面板）。
func TestStateCarriesHandResultAfterHandSettles(t *testing.T) {
	e := New()
	ids := engineIDs()
	state, err := e.Init(enginePlayers())
	if err != nil {
		t.Fatalf("Init 失败：%v", err)
	}
	if _, ok := viewsOf(t, state)[0]["handResult"]; ok {
		t.Fatalf("开局不应下发 handResult")
	}

	var settled map[string]interface{}
	for i := 0; i < 4000; i++ {
		state = actOnce(t, e, ids, state)
		if _, ok := viewsOf(t, state)[0]["handResult"]; ok {
			settled = state
			break
		}
	}
	if settled == nil {
		t.Fatalf("4000 步内本手未结算，无法验证 handResult")
	}

	views := viewsOf(t, settled)
	for seat := 0; seat < len(ids); seat++ {
		raw, ok := views[seat]["handResult"]
		if !ok {
			t.Fatalf("本手结算后 seat %d 视角应含 handResult", seat)
		}
		res, ok := raw.(core.HandResult)
		if !ok {
			t.Fatalf("handResult 类型异常：%T", raw)
		}
		if res.Kind == "" {
			t.Fatalf("seat %d 的 handResult.Kind 为空", seat)
		}
	}
	spectator, ok := settled["spectator"].(map[string]interface{})
	if !ok {
		t.Fatalf("state.spectator 类型异常：%T", settled["spectator"])
	}
	if _, ok := spectator["handResult"]; !ok {
		t.Fatalf("观战视角也应含 handResult")
	}
}

// TestHandResultClearedWhenNextHandActs 引擎层：下一手首个动作落库后 handResult 消失、
// 局次索引 +1（结算面板随之关闭）。
func TestHandResultClearedWhenNextHandActs(t *testing.T) {
	e := New()
	ids := engineIDs()
	state, err := e.Init(enginePlayers())
	if err != nil {
		t.Fatalf("Init 失败：%v", err)
	}

	var settled map[string]interface{}
	for i := 0; i < 4000; i++ {
		state = actOnce(t, e, ids, state)
		if _, ok := viewsOf(t, state)[0]["handResult"]; ok {
			settled = state
			break
		}
	}
	if settled == nil {
		t.Fatalf("4000 步内本手未结算，无法验证续手")
	}
	before := handIndexOf(t, settled)

	next := actOnce(t, e, ids, settled)
	after := handIndexOf(t, next)
	if after != before+1 {
		// 连庄（亲家和牌）时局次不变，但 handResult 仍应消失；两种都接受，只锁「不减少」。
		if after < before {
			t.Fatalf("续手后局次索引不应减少：before=%d after=%d", before, after)
		}
	}
	for seat, view := range viewsOf(t, next) {
		if _, ok := view["handResult"]; ok {
			t.Fatalf("下一手已出牌，seat %d 视角不应再带 handResult", seat)
		}
	}
}
