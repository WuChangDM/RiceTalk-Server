package riichi

import (
	"encoding/json"
	"errors"
	"testing"

	"ridgericetalk/features/minigames/internal/util"
	"ridgericetalk/features/minigames/riichi/core"
)

func fourPlayers() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, 4)
	for _, id := range []string{"u-east", "u-south", "u-west", "u-north"} {
		out = append(out, map[string]interface{}{"userId": id, "username": id})
	}
	return out
}

func initState(t *testing.T) (map[string]interface{}, []string) {
	t.Helper()
	e := New()
	state, err := e.Init(fourPlayers())
	if err != nil {
		t.Fatalf("Init 失败：%v", err)
	}
	ids := util.GetStringSlice(state, "players")
	if len(ids) != 4 {
		t.Fatalf("玩家列表应为 4 人，实际 %v", ids)
	}
	return state, ids
}

// 非当前行动者提交动作必须被拒，且拒绝后状态与拒绝前逐字节一致（越权不污染状态）。
func TestValidateMoveRejectsOutOfTurnWithoutMutatingState(t *testing.T) {
	e := New()
	state, ids := initState(t)
	current, _ := state["currentPlayer"].(string)
	curSeat := -1
	for i, id := range ids {
		if id == current {
			curSeat = i
		}
	}
	if curSeat < 0 {
		t.Fatalf("currentPlayer %q 不在玩家列表 %v 中", current, ids)
	}
	otherSeat := (curSeat + 1) % 4
	legal, err := currentLegalTiles(state, curSeat)
	if err != nil {
		t.Fatalf("取合法动作失败：%v", err)
	}
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"tiles": legal})
	if _, err := e.ValidateMove(state, ids[otherSeat], "discard", payload); !errors.Is(err, util.ErrInvalidAction) {
		t.Fatalf("非当前玩家出牌应返回 ErrInvalidAction，实际 %v", err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("非法动作不应修改传入的状态")
	}
}

func TestValidateMoveRejectsUnknownUser(t *testing.T) {
	e := New()
	state, _ := initState(t)
	if _, err := e.ValidateMove(state, "u-ghost", "discard", json.RawMessage(`{"tile":"1m"}`)); !errors.Is(err, util.ErrInvalidAction) {
		t.Fatalf("非玩家提交动作应返回 ErrInvalidAction，实际 %v", err)
	}
}

// 状态经 JSON 往返（生产路径是 DB 文本列）后仍可继续对局。
func TestStateSurvivesJSONRoundTrip(t *testing.T) {
	e := New()
	state, ids := initState(t)
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored map[string]interface{}
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	current, _ := restored["currentPlayer"].(string)
	seat := -1
	for i, id := range ids {
		if id == current {
			seat = i
		}
	}
	tiles, err := currentLegalTiles(restored, seat)
	if err != nil {
		t.Fatalf("取合法动作失败：%v", err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"tiles": tiles})
	next, err := e.ValidateMove(restored, ids[seat], "discard", payload)
	if err != nil {
		t.Fatalf("JSON 往返后出牌失败：%v", err)
	}
	snap := snapshotOf(t, next)
	if len(snap.Actions) != 1 {
		t.Fatalf("动作应被记录 1 条，实际 %d", len(snap.Actions))
	}
	if snap.Actions[0].Kind != "discard" {
		t.Fatalf("记录的动作类型应为 discard，实际 %q", snap.Actions[0].Kind)
	}
}

func TestIsGameOverInitialFalse(t *testing.T) {
	e := New()
	state, _ := initState(t)
	if winner, over := e.IsGameOver(state); over || winner != "" {
		t.Fatalf("开局 IsGameOver 应为 false，实际 over=%v winner=%q", over, winner)
	}
}

// 观战视角不得泄露任何一家的手牌与可行动作。
func TestSpectatorStateHidesHandsAndSnapshot(t *testing.T) {
	e := New()
	state, _ := initState(t)
	view := e.SpectatorState(state)
	if _, ok := view["snapshot"]; ok {
		t.Fatal("观战视角不应包含 snapshot")
	}
	views, _ := view["views"].([]interface{})
	if len(views) != 1 {
		t.Fatalf("观战视角应只保留 1 份 spectator 视图，实际 %d", len(views))
	}
	spectator, _ := views[0].(map[string]interface{})
	if spectator == nil {
		t.Fatal("观战视图缺失")
	}
	for _, banned := range []string{"closed_tiles", "drawn", "legal_actions", "seat"} {
		if _, ok := spectator[banned]; ok {
			t.Fatalf("观战视图不应包含 %q", banned)
		}
	}
	if spectator["spectator"] != true {
		t.Fatalf("观战视图应带 spectator=true，实际 %v", spectator["spectator"])
	}
}

// 当前玩家的合法动作列表必须非空，且每个动作的 seat 都等于当前玩家座位。
func TestCurrentPlayerHasLegalActions(t *testing.T) {
	state, ids := initState(t)
	current, _ := state["currentPlayer"].(string)
	seat := -1
	for i, id := range ids {
		if id == current {
			seat = i
		}
	}
	acts := legalActionsOf(t, state, seat)
	if len(acts) == 0 {
		t.Fatal("当前玩家应至少有 1 个合法动作")
	}
	for _, a := range acts {
		if got, _ := a["seat"].(float64); int(got) != seat {
			t.Fatalf("合法动作 seat=%v 与当前座位 %d 不一致", a["seat"], seat)
		}
	}
}

func TestInitRejectsFewerThanFourPlayers(t *testing.T) {
	e := New()
	players := fourPlayers()[:3]
	if _, err := e.Init(players); err == nil {
		t.Fatal("3 人入座应初始化失败")
	}
}

// currentLegalTiles 返回当前座位某个 discard 动作的牌名。
func currentLegalTiles(state map[string]interface{}, seat int) ([]string, error) {
	acts := legalActionsOf(nil, state, seat)
	for _, a := range acts {
		if kind, _ := a["kind"].(string); kind != "discard" {
			continue
		}
		rawTiles, _ := a["tiles"].([]interface{})
		out := make([]string, 0, len(rawTiles))
		for _, rt := range rawTiles {
			if s, ok := rt.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, errors.New("当前座位没有 discard 合法动作")
}

func legalActionsOf(t *testing.T, state map[string]interface{}, seat int) []map[string]interface{} {
	if t != nil {
		t.Helper()
	}
	views, _ := state["views"].([]interface{})
	if seat < 0 || seat >= len(views) {
		return nil
	}
	view, _ := views[seat].(map[string]interface{})
	if view == nil {
		return nil
	}
	raw, _ := view["legal_actions"].([]interface{})
	out := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

type snapshotView struct {
	Actions []struct {
		Kind   string   `json:"kind"`
		Seat   int      `json:"seat"`
		Tiles  []string `json:"tiles"`
		Called string   `json:"called"`
	} `json:"actions"`
	HandIndex int  `json:"handIndex"`
	Finished  bool `json:"finished"`
}

func snapshotOf(t *testing.T, state map[string]interface{}) snapshotView {
	t.Helper()
	raw, err := json.Marshal(state["snapshot"])
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var out snapshotView
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	return out
}

// 纵深防御（A）：历史 state 行里 snapshot.seed 已是大整数（>2^53）时，
// 经 map[string]interface{}（float64）还原不得再走精度路径 —— 只要原值恰能被
// float64 精确表示，就必须取回同一个 seed 并还原出同一副牌山。
func TestSnapshotSeedRecoveredFromFloat64Map(t *testing.T) {
	const bigSeed int64 = 1700000000000000000 // > 2^53，且恰可被 float64 精确表示
	if float64(bigSeed) != float64(bigSeed) || int64(float64(bigSeed)) != bigSeed {
		t.Skipf("本平台无法精确表示 seed %d，跳过", bigSeed)
	}
	scores := [4]int{25000, 25000, 25000, 25000}
	orig, err := core.Restore(core.Snapshot{
		Seed: bigSeed,
		Position: core.Position{
			RoundWind: "east",
			Kyoku:     1,
			Scores:    scores,
		},
	})
	if err != nil {
		t.Fatalf("用大 seed 构造历史牌局失败：%v", err)
	}

	// 模拟 DB 读回：snapshot 先 marshal 再 unmarshal 成 map（数字全部 float64）。
	raw, err := json.Marshal(orig.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var generic map[string]interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if seedFloat, ok := generic["seed"].(float64); !ok || int64(seedFloat) != bigSeed {
		t.Fatalf("用例前提不成立：map 中 seed 应为 float64(%d)，实际 %#v", bigSeed, generic["seed"])
	}

	state := map[string]interface{}{"snapshot": generic}
	snap, ok := snapshotFromState(state)
	if !ok {
		t.Fatal("snapshotFromState 解析历史 state 失败")
	}
	if snap.Seed != bigSeed {
		t.Fatalf("seed 应还原为 %d，实际 %d", bigSeed, snap.Seed)
	}
	restored, err := core.Restore(snap)
	if err != nil {
		t.Fatalf("Restore 历史快照失败：%v", err)
	}
	want, _ := json.Marshal(orig.View(0))
	got, _ := json.Marshal(restored.View(0))
	if string(want) != string(got) {
		t.Fatal("大 seed 历史 state 还原出的牌山与原始不一致")
	}

	// 非法 seed（小数 / 缺失）必须被拒，不能静默使用。
	bad := map[string]interface{}{"snapshot": map[string]interface{}{"seed": 1.5}}
	if _, ok := snapshotFromState(bad); ok {
		t.Fatal("小数 seed 应被拒绝")
	}
	missing := map[string]interface{}{"snapshot": map[string]interface{}{"handIndex": 0}}
	if _, ok := snapshotFromState(missing); ok {
		t.Fatal("缺少 seed 的 snapshot 应被拒绝")
	}
}
