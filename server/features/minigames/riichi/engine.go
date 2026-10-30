// Package riichi 实现日本麻将（立直麻将）的 minigames 引擎。
//
// 规则全部委托给 riichi/core 适配层（其内部使用 okki-0417/mahjong v0.1.0）；
// 本包只负责把引擎状态与 GameEngine 接口对接，**不得**直接 import 第三方库
// （DES-20261030-01 §2 隔离约束，验收会 grep 校验）。
package riichi

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"ridgericetalk/features/minigames/internal/util"
	"ridgericetalk/features/minigames/riichi/core"
)

// Engine 是日本麻将引擎。无内部可变状态，可并发复用。
type Engine struct{}

// New 返回日麻引擎实例。
func New() *Engine { return &Engine{} }

// Init 初始化一局东风战：4 名玩家按入座顺序对应东/南/西/北座位。
func (e *Engine) Init(players []map[string]interface{}) (map[string]interface{}, error) {
	return e.InitWithConfig(players, core.Config{})
}

// InitWithConfig 用指定场制初始化（东/半庄），供建房时传入配置。
func (e *Engine) InitWithConfig(players []map[string]interface{}, cfg core.Config) (map[string]interface{}, error) {
	ids := util.PlayerUserIDs(players)
	if len(ids) < core.Seats {
		return nil, errors.New("日本麻将需要 4 名玩家")
	}
	ids = ids[:core.Seats]

	// seed 必须 < 2^53：session.State 以 JSON 存库、读回时数字变 float64，
	// 超过尾数 53 位会被舍入 → 牌山不一致 → 偶发 invalid game action。
	m, err := core.NewMatch(time.Now().UnixNano()&(core.MaxSeed-1), cfg)
	if err != nil {
		return nil, err
	}
	return buildState(ids, m)
}

// ValidateMove 校验并应用一个动作。
//
// 动作词表（客户端 action 字段）：discard / riichi / chi / pon / minkan /
// ankan / kakan / tsumo / ron / pass / kyushukyuhai。
// payload：{"tiles":["1m",...], "called":"5p"}（chi/pon 需 called；minkan 只需 called）。
func (e *Engine) ValidateMove(state map[string]interface{}, userID string, action string, payload json.RawMessage) (map[string]interface{}, error) {
	ids := util.GetStringSlice(state, "players")
	seat := seatOf(ids, userID)
	if seat < 0 {
		return nil, util.ErrInvalidAction
	}
	snap, ok := snapshotFromState(state)
	if !ok {
		return nil, util.ErrInvalidAction
	}
	m, err := core.Restore(snap)
	if err != nil {
		return nil, util.ErrInvalidAction
	}
	if m.Finished() {
		return nil, util.ErrInvalidAction
	}

	var body struct {
		Tiles  []string `json:"tiles"`
		Tile   string   `json:"tile"`
		Called string   `json:"called"`
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &body)
	}
	tiles := body.Tiles
	if len(tiles) == 0 && body.Tile != "" {
		tiles = []string{body.Tile}
	}
	if err := m.Apply(seat, action, tiles, body.Called); err != nil {
		if errors.Is(err, core.ErrFinished) {
			return nil, util.ErrInvalidAction
		}
		return nil, util.ErrInvalidAction
	}
	return buildState(ids, m)
}

// IsGameOver 报告整场是否结束；winnerID 为空表示流局/并列无头名。
func (e *Engine) IsGameOver(state map[string]interface{}) (string, bool) {
	snap, ok := snapshotFromState(state)
	if !ok || !snap.Finished {
		return "", false
	}
	ids := util.GetStringSlice(state, "players")
	if snap.WinnerSeat >= 0 && snap.WinnerSeat < len(ids) {
		return ids[snap.WinnerSeat], false
	}
	return "", true
}

// SpectatorState 返回观战视角：去掉四家视角（含手牌），仅保留公开信息。
func (e *Engine) SpectatorState(state map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(state))
	for k, v := range state {
		out[k] = v
	}
	delete(out, "views")
	delete(out, "snapshot")
	if v, ok := out["spectator"]; ok {
		out["views"] = []interface{}{v}
	}
	return out
}

// PlayerState 返回玩家视角（M14）：剥离 snapshot，并把其余三家的视角降级为公开信息。
//
// 为什么必须剥：state.snapshot 含牌山 seed 与全部历史动作，而核心库
// `core.Match` 的牌山由 `wallFor(seed, handIndex)` 确定性生成 —— 拿到 seed 的
// 玩家可离线复算四家手牌与后续摸牌顺序（作弊，known_issues M14）。
// 同时 views[i]（i != 自己座位）里有别家 closed_tiles/drawn/legal_actions，
// 一并对其他座位做遮蔽，只留牌河/副露/点数/立直等公开信息。
//
// 保留：本座位完整视图（含 closed_tiles/drawn/legal_actions）、match/handIndex/
// 剩余张数/宝牌指示牌等公共信息 —— 客户端 task-64 只用 views[seat]，不读 snapshot。
func (e *Engine) PlayerState(state map[string]interface{}, userID string) map[string]interface{} {
	out := make(map[string]interface{}, len(state))
	for k, v := range state {
		out[k] = v
	}
	delete(out, "snapshot")

	ids := util.GetStringSlice(state, "players")
	seat := -1
	for i, id := range ids {
		if id == userID {
			seat = i
			break
		}
	}
	if seat < 0 {
		// 防御性兜底（M14 补漏，task-87）：正常路径已由 handler 用「是否本局玩家」拦住，
		// 不应走到这里；但即使调用方传错，也绝不返回未遮蔽的 views —— 全部座位按公开
		// 信息处理，等价于观战视角（否则任意空间成员可读 4 家手牌）。
		if views, ok := state["views"].([]interface{}); ok {
			masked := make([]interface{}, 0, len(views))
			for _, raw := range views {
				masked = append(masked, maskOtherSeatView(raw))
			}
			out["views"] = masked
		}
		return out
	}

	if views, ok := state["views"].([]interface{}); ok {
		masked := make([]interface{}, 0, len(views))
		for i, raw := range views {
			if i == seat {
				masked = append(masked, raw)
				continue
			}
			masked = append(masked, maskOtherSeatView(raw))
		}
		out["views"] = masked
	}
	out["seat"] = seat
	return out
}

// maskOtherSeatView 把别家座位视角降级为公开信息（去掉手牌/摸牌/合法动作）。
func maskOtherSeatView(raw interface{}) interface{} {
	view, ok := raw.(map[string]interface{})
	if !ok {
		return raw
	}
	masked := make(map[string]interface{}, len(view))
	for k, v := range view {
		masked[k] = v
	}
	delete(masked, "closed_tiles")
	delete(masked, "closedTiles")
	delete(masked, "hand")
	delete(masked, "drawn")
	delete(masked, "legal_actions")
	delete(masked, "legalActions")
	return masked
}

// buildState 把对局状态打包为 session.State。
func buildState(ids []string, m *core.Match) (map[string]interface{}, error) {
	snap := m.Snapshot()
	views := make([]interface{}, 0, core.Seats)
	for s := 0; s < core.Seats; s++ {
		views = append(views, m.View(s))
	}
	awaiting := m.AwaitingSeats()
	current := ""
	if len(awaiting) > 0 && awaiting[0] >= 0 && awaiting[0] < len(ids) {
		current = ids[awaiting[0]]
	}
	status := "playing"
	var winner interface{}
	if m.Finished() {
		status = "ended"
		if w := m.WinnerSeat(); w >= 0 && w < len(ids) {
			winner = ids[w]
		}
	}
	return map[string]interface{}{
		"game":          "riichi",
		"status":        status,
		"players":       ids,
		"currentPlayer": current,
		"winner":        winner,
		"snapshot":      snap,
		"views":         views,
		"spectator":     m.SpectatorView(),
		"awaiting":      awaiting,
		"handIndex":     m.HandIndex(),
	}, nil
}

// snapshotFromState 从 state 取回持久化快照。
//
// 生产路径中 state 是 DB 文本列，读回时先经 encoding/json 解成
// map[string]interface{} —— 所有数字降级为 float64，超过 2^53 的整数会被舍入。
// 因此这里对 seed 单独取值：map 形态下把 seed 从原值上取整（int64 / json.Number /
// float64 精确整数 / 十进制字符串），避免再次走「float64 → 再 marshal → int64」
// 的二次精度路径；随后用 json.Decoder.UseNumber() 解码其余字段，确保其它整数
// 字段也不会被 float64 污染。
func snapshotFromState(state map[string]interface{}) (core.Snapshot, bool) {
	raw, ok := state["snapshot"]
	if !ok || raw == nil {
		return core.Snapshot{}, false
	}
	if m, isMap := raw.(map[string]interface{}); isMap {
		seedVal, hasSeed := m["seed"]
		if !hasSeed {
			return core.Snapshot{}, false
		}
		seed, ok := seedFromAny(seedVal)
		if !ok {
			return core.Snapshot{}, false
		}
		rest := make(map[string]interface{}, len(m))
		for k, v := range m {
			if k != "seed" {
				rest[k] = v
			}
		}
		snap, ok := decodeSnapshotJSON(rest)
		if !ok {
			return core.Snapshot{}, false
		}
		snap.Seed = seed
		return snap, true
	}
	// 内存态（Init 刚返回时 snapshot 是 core.Snapshot 结构体）：直接解码。
	b, err := json.Marshal(raw)
	if err != nil {
		return core.Snapshot{}, false
	}
	return decodeSnapshotJSONBytes(b)
}

// decodeSnapshotJSON 把任意 JSON 值的 map 形式解码为 Snapshot（数字走 json.Number）。
func decodeSnapshotJSON(v interface{}) (core.Snapshot, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return core.Snapshot{}, false
	}
	return decodeSnapshotJSONBytes(b)
}

func decodeSnapshotJSONBytes(b []byte) (core.Snapshot, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var snap core.Snapshot
	if err := dec.Decode(&snap); err != nil {
		return core.Snapshot{}, false
	}
	return snap, true
}

// seedFromAny 从 JSON 反序列化后的任意值取 int64 seed，不做浮点二次舍入。
func seedFromAny(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, n >= 0
	case int:
		return int64(n), n >= 0
	case int32:
		return int64(n), n >= 0
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, i >= 0
		}
		if f, err := n.Float64(); err == nil {
			return floatToSeed(f)
		}
		return 0, false
	case float64:
		return floatToSeed(n)
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil && i >= 0
	default:
		return 0, false
	}
}

// floatToSeed 把 float64 取整为 seed；只接受精确整数（小数或越界一律拒绝）。
//
// 历史脏数据（seed 曾 >2^53）只有在其原值恰能被 float64 精确表示时才可恢复；
// 舍入已造成信息丢失的行无法还原，交由 Restore 的牌山校验拒掉。
func floatToSeed(f float64) (int64, bool) {
	// int64 上界 2^63 无法用整数常量与 float64 直接安全比较（会先舍入），
	// 这里显式用 2^63 的浮点值做开区间上界。
	const maxInt64AsFloat = float64(1 << 63)
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f >= maxInt64AsFloat {
		return 0, false
	}
	i := int64(f)
	if float64(i) != f {
		return 0, false
	}
	return i, true
}

// seatOf 返回 userID 在玩家列表中的座位下标（未入座返回 -1）。
func seatOf(ids []string, userID string) int {
	for i, id := range ids {
		if id == userID {
			return i
		}
	}
	return -1
}
