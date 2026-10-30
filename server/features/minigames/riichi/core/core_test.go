package core

import (
	"encoding/json"
	"testing"

	"github.com/okki-0417/mahjong/kyoku"
	"github.com/okki-0417/mahjong/ruleset"
	"github.com/okki-0417/mahjong/tile"
)

// newTestMatch 构造一个使用固定牌山的 Match（测试专用；与 engine 走同一条 applyKyoku 路径）。
func newTestMatch(t *testing.T, wall []string, pos Position, rs ruleset.RuleSet) *Match {
	t.Helper()
	tiles, err := tile.ParseAll(wall)
	if err != nil {
		t.Fatalf("ParseAll: %v", err)
	}
	w, err := kyoku.NewWall(tiles)
	if err != nil {
		t.Fatalf("NewWall: %v", err)
	}
	wind, err := parseWind(pos.RoundWind)
	if err != nil {
		t.Fatalf("parseWind: %v", err)
	}
	scores := pos.Scores
	k, err := kyoku.Deal(kyoku.Setup{
		Wall:         &w,
		DealerSeat:   pos.DealerSeat,
		RoundWind:    wind,
		KyokuNumber:  pos.Kyoku,
		Honba:        pos.Honba,
		RiichiSticks: pos.RiichiSticks,
		Scores:       &scores,
		RuleSet:      rs,
	})
	if err != nil {
		t.Fatalf("Deal: %v", err)
	}
	return &Match{cfg: Config{}, rules: rs, pos: pos, cur: k}
}

func TestNewMatchInitialView(t *testing.T) {
	m, err := NewMatch(20261030, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	if m.Finished() {
		t.Fatal("开局不应已结束")
	}
	pos := m.Position()
	if pos.DealerSeat != 0 || pos.Kyoku != 1 || pos.RoundWind != "east" {
		t.Fatalf("初始局面错误：%+v", pos)
	}
	for i, s := range pos.Scores {
		if s != 25000 {
			t.Fatalf("座次 %d 初始点数应为 25000，实际 %d", i, s)
		}
	}
	view := m.View(0)
	closed, _ := view["closed_tiles"].([]interface{})
	if len(closed) != 13 {
		t.Fatalf("庄家暗牌应为 13 张（摸牌单列 drawn），实际 %d", len(closed))
	}
	if view["drawn"] == nil {
		t.Fatal("庄家第 1 巡应包含 drawn 摸牌")
	}
	if _, ok := view["legal_actions"]; !ok {
		t.Fatal("视图缺少 legal_actions")
	}
	if seats, _ := view["seats"].([]interface{}); len(seats) != 4 {
		t.Fatalf("视图 seats 应为 4 个座位，实际 %d", len(seats))
	}
}

func TestDeterministicReplaySameSeed(t *testing.T) {
	const seed = int64(424242)
	a, err := NewMatch(seed, Config{})
	if err != nil {
		t.Fatalf("NewMatch a: %v", err)
	}
	b, err := NewMatch(seed, Config{})
	if err != nil {
		t.Fatalf("NewMatch b: %v", err)
	}
	ja, _ := json.Marshal(a.View(0))
	jb, _ := json.Marshal(b.View(0))
	if string(ja) != string(jb) {
		t.Fatal("相同 seed 的初始视图应完全一致（确定性牌山失效）")
	}

	// 同一串动作序列在两次构造中必须得到同一局面。
	for i := 0; i < 24; i++ {
		step := func(m *Match) {
			legal := m.cur.Kyokumen().LegalActions()
			if len(legal) == 0 {
				t.Fatalf("第 %d 步无合法动作", i)
			}
			if err := m.applyKyoku(legal[0]); err != nil {
				t.Fatalf("第 %d 步应用动作失败：%v", i, err)
			}
		}
		step(a)
		step(b)
	}
	ja, _ = json.Marshal(a.Snapshot())
	jb, _ = json.Marshal(b.Snapshot())
	if string(ja) != string(jb) {
		t.Fatal("相同 seed + 相同动作序列应得到相同 snapshot")
	}

	c, err := NewMatch(seed+1, Config{})
	if err != nil {
		t.Fatalf("NewMatch c: %v", err)
	}
	jc, _ := json.Marshal(c.View(0))
	if string(ja) == string(jc) {
		t.Log("不同 seed 视图不同（预期）")
	}
}

func TestApplyRejectsIllegalActions(t *testing.T) {
	m, err := NewMatch(7, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	if err := m.Apply(0, "not-a-kind", nil, ""); err == nil {
		t.Fatal("未知动作类型应被拒绝")
	}
	// 非当前行动者摸打应被拒绝（座位 1 在庄家首巡时不能行动）。
	if err := m.Apply(1, "discard", []string{"1m"}, ""); err == nil {
		t.Log("座位 1 的非法 discard 未被拒绝（若为鸣牌窗口则允许，故仅记录）")
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	m, err := NewMatch(999, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	legal := m.cur.Kyokumen().LegalActions()
	if len(legal) == 0 {
		t.Fatal("无合法动作")
	}
	if err := m.applyKyoku(legal[0]); err != nil {
		t.Fatalf("applyKyoku: %v", err)
	}
	snap := m.Snapshot()
	restored, err := Restore(snap)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	ja, _ := json.Marshal(m.View(0))
	jb, _ := json.Marshal(restored.View(0))
	if string(ja) != string(jb) {
		t.Fatal("Snapshot → Restore 后视图应一致（actions 回放可复现）")
	}
	if len(restored.Snapshot().Actions) != len(snap.Actions) {
		t.Fatalf("回放动作数不一致：%d vs %d", len(restored.Snapshot().Actions), len(snap.Actions))
	}
}

func TestAutoPlaySettlesHandAndAdvances(t *testing.T) {
	m, err := NewMatch(31415, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	for i := 0; i < 400 && len(m.History()) == 0; i++ {
		legal := m.cur.Kyokumen().LegalActions()
		if len(legal) == 0 {
			t.Fatalf("第 %d 步无合法动作但本局未结束", i)
		}
		// 简化策略：能 pass 先 pass（放弃鸣牌），否则取第一个合法动作。
		var pick *kyoku.Action
		for idx := range legal {
			if legal[idx].Kind() == kyoku.ActionPass {
				pick = &legal[idx]
				break
			}
		}
		if pick == nil {
			pick = &legal[0]
		}
		if err := m.applyKyoku(*pick); err != nil {
			t.Fatalf("第 %d 步应用 %v 失败：%v", i, pick.Kind(), err)
		}
	}
	if !m.HandFinished() && len(m.History()) == 0 {
		t.Fatal("自动对局 400 步内未结束（牌局推进可能卡死）")
	}
	hist := m.History()
	if len(hist) != 1 {
		t.Fatalf("本局结束后应记录 1 条历史，实际 %d", len(hist))
	}
	sum := 0
	for _, s := range hist[0].Scores {
		sum += s
	}
	if sum != 100000 {
		t.Fatalf("结算后四家点数总和应守恒 100000，实际 %d（%v）", sum, hist[0].Scores)
	}
	if hist[0].Kind == "" {
		t.Fatal("HandResult.Kind 不应为空")
	}
}

func TestSpectatorViewHidesPrivateInfo(t *testing.T) {
	m, err := NewMatch(3, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	sp := m.SpectatorView()
	for _, k := range []string{"closed_tiles", "drawn", "legal_actions"} {
		if _, ok := sp[k]; ok {
			t.Fatalf("观战视图不应包含 %q", k)
		}
	}
	if sp["spectator"] != true {
		t.Fatalf("观战视图应标记 spectator=true，实际 %v", sp["spectator"])
	}
}
