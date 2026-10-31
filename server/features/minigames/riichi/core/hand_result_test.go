package core

import (
	"testing"

	"github.com/okki-0417/mahjong/kyoku"
	"github.com/okki-0417/mahjong/ruleset"
	"github.com/okki-0417/mahjong/tile"
)

// task-93：每一手结束（荣和/自摸/流局）都要能拿到本手 handResult，而不是只有整场末手。
//
// 这些用例直接驱动 core.Match（真牌山 + 真规则库 + golden 真实对局），断言的是
// `View(seat)["handResult"]`，也就是客户端 `RiichiMahjongGame.tsx:363` 读的那个字段。

// newGoldenMatch 用一条 golden 记录重建一手牌局（与 golden_test.go 同构，便于复用）。
//
// override 可选：覆盖起始点数（必须在 kyoku.Deal 之前注入，Deal 会把点数拷进牌局内部，
// 事后改 m.pos.Scores 不会生效）。
func newGoldenMatch(t *testing.T, g goldenLine, override ...*[4]int) *Match {
	t.Helper()
	tiles, err := tile.ParseAll(g.Wall)
	if err != nil {
		t.Fatalf("ParseAll: %v", err)
	}
	w, err := kyoku.NewWall(tiles)
	if err != nil {
		t.Fatalf("NewWall: %v", err)
	}
	wind, err := parseWind(g.Setup.RoundWind)
	if err != nil {
		t.Fatalf("parseWind: %v", err)
	}
	rs := ruleset.Default().
		WithKuitan(g.RuleSet.Kuitan).
		WithRoundUpMangan(g.RuleSet.RoundUpMangan).
		WithNagashiMangan(g.RuleSet.NagashiMangan)

	var scores [4]int
	copy(scores[:], g.Setup.Scores)
	if len(override) > 0 && override[0] != nil {
		scores = *override[0]
	}
	pos := Position{
		RoundWind:    g.Setup.RoundWind,
		Kyoku:        g.Setup.KyokuNumber,
		Honba:        g.Setup.Honba,
		RiichiSticks: g.Setup.RiichiSticks,
		DealerSeat:   g.Setup.DealerSeat,
		Scores:       scores,
	}
	s := scores
	k, err := kyoku.Deal(kyoku.Setup{
		Wall:         &w,
		DealerSeat:   pos.DealerSeat,
		RoundWind:    wind,
		KyokuNumber:  pos.Kyoku,
		Honba:        pos.Honba,
		RiichiSticks: pos.RiichiSticks,
		Scores:       &s,
		RuleSet:      rs,
	})
	if err != nil {
		t.Fatalf("Deal: %v", err)
	}
	return &Match{cfg: Config{MatchType: "east"}, rules: rs, pos: pos, cur: k}
}

func replayGolden(t *testing.T, m *Match, g goldenLine) {
	t.Helper()
	for i, st := range g.Steps {
		called := ""
		if st.Chosen.Called != nil {
			called = *st.Chosen.Called
		}
		if err := m.Apply(st.Chosen.Seat, st.Chosen.Kind, st.Chosen.Tiles, called); err != nil {
			t.Fatalf("第 %d 步（%s seat=%d tiles=%v）应用失败：%v", i, st.Chosen.Kind, st.Chosen.Seat, st.Chosen.Tiles, err)
		}
	}
}

// TestHandResultAttachedForEveryHandEnding 覆盖 golden 里全部 7 手（荣和/自摸/九种九牌/荒牌流局）：
// 四家视图都必须带本手 handResult，且字段与 fixture 结算一致。
func TestHandResultAttachedForEveryHandEnding(t *testing.T) {
	golden := loadGolden(t)
	if len(golden) < 7 {
		t.Fatalf("golden 手数不足（期望 >=7，实际 %d）", len(golden))
	}
	seen := map[string]bool{}
	for idx, g := range golden {
		idx, g := idx, g
		t.Run(fixtureName(idx), func(t *testing.T) {
			m := newGoldenMatch(t, g)
			replayGolden(t, m, g)
			seen[g.Result.Kind] = true

			for seat := 0; seat < Seats; seat++ {
				view := m.View(seat)
				raw, ok := view["handResult"]
				if !ok {
					t.Fatalf("seat %d 视图缺少 handResult（本手已结束，kind=%s）", seat, g.Result.Kind)
				}
				hr, ok := raw.(HandResult)
				if !ok {
					t.Fatalf("seat %d handResult 类型异常：%T", seat, raw)
				}
				if hr.Kind != g.Result.Kind {
					t.Fatalf("handResult.kind 应为 %q，实际 %q", g.Result.Kind, hr.Kind)
				}
				for i := 0; i < Seats; i++ {
					if hr.Deltas[i] != g.Result.Deltas[i] {
						t.Fatalf("handResult.deltas[%d] 应为 %d，实际 %d", i, g.Result.Deltas[i], hr.Deltas[i])
					}
					if hr.Scores[i] != g.Result.Scores[i] {
						t.Fatalf("handResult.scores[%d] 应为 %d，实际 %d", i, g.Result.Scores[i], hr.Scores[i])
					}
				}
				switch hr.Kind {
				case "ron", "tsumo":
					if hr.Total <= 0 || hr.Han <= 0 {
						t.Fatalf("和牌结算应含番/点数：han=%d fu=%d total=%d", hr.Han, hr.Fu, hr.Total)
					}
					if len(hr.Yaku) == 0 {
						t.Fatalf("和牌结算应含役种：%+v", hr)
					}
				case "ryukyoku", "kyushukyuhai":
					sum := 0
					for _, d := range hr.Deltas {
						sum += d
					}
					if sum != 0 {
						t.Fatalf("流局类结算点数增减应守恒（和为 0），实际 %v", hr.Deltas)
					}
				}
			}

			// 观战视角（SpectatorView）同样要能看到结算，否则观战端不渲染面板。
			sp := m.SpectatorView()
			if _, ok := sp["handResult"]; !ok {
				t.Fatalf("观战视角缺少 handResult（kind=%s）", g.Result.Kind)
			}
		})
	}
	for _, kind := range []string{"ron", "tsumo", "ryukyoku"} {
		if !seen[kind] {
			t.Fatalf("golden 未覆盖 %q 结算类型，用例覆盖面不足", kind)
		}
	}
}

// TestHandResultClearedWhenNextHandStartsActing 下一手开始后（首个动作落库）handResult 必须清空，局次已推进。
func TestHandResultClearedWhenNextHandStartsActing(t *testing.T) {
	golden := loadGolden(t)
	var target *goldenLine
	for i := range golden {
		if golden[i].Next != nil && golden[i].Result.Kind == "ron" {
			target = &golden[i]
			break
		}
	}
	if target == nil {
		t.Fatalf("golden 中没有「有下一手」的荣和样本")
	}
	m := newGoldenMatch(t, *target)
	before := m.Position()
	beforeHandIndex := m.HandIndex()
	replayGolden(t, m, *target)

	if m.Finished() {
		t.Fatalf("该样本不应整场结束（应进入下一手）")
	}
	if len(m.actions) != 0 {
		t.Fatalf("刚结算时 actions 应为空，实际 %d", len(m.actions))
	}
	if _, ok := m.View(0)["handResult"]; !ok {
		t.Fatalf("结算过渡态应有 handResult")
	}
	after := m.Position()
	if after.Kyoku == before.Kyoku && m.HandIndex() == beforeHandIndex {
		t.Fatalf("下一手应推进局次：before=%+v after=%+v handIndex %d→%d", before, after, beforeHandIndex, m.HandIndex())
	}

	// 下一手第一个合法动作（真走 ValidateMove 同款 Apply 路径）。
	if m.cur == nil {
		t.Fatalf("下一手牌局未创建")
	}
	acts := m.cur.Kyokumen().LegalActions()
	if len(acts) == 0 {
		t.Fatalf("下一手应立即有合法动作")
	}
	act := acts[0]
	if err := m.Apply(act.Seat(), act.Kind().String(), tile.Labels(act.Tiles()), calledLabel(act)); err != nil {
		t.Fatalf("下一手首个动作应用失败：%v", err)
	}
	for seat := 0; seat < Seats; seat++ {
		if _, ok := m.View(seat)["handResult"]; ok {
			t.Fatalf("下一手已有动作后 seat %d 仍在下发上一手 handResult（结算面板不会关闭）", seat)
		}
	}
}

// TestHandResultKeptAtMatchEnd 整场结束行为不变：仍下发最后一手结算且 finished 为真。
//
// 用「击飞」构造终局：把放铳者（fixture 里 deltas 为负的座位）开局点数压到 3000，
// 这一手被荣和后跌破 0 → matchOver() 判定整场结束（比构造「东 4 局不连庄」更稳定，
// golden 里只有南 4 局的样本，直接改局号会与 fixture 的风/局不自洽）。
func TestHandResultKeptAtMatchEnd(t *testing.T) {
	golden := loadGolden(t)
	var target *goldenLine
	for i := range golden {
		if golden[i].Result.Kind != "ron" || golden[i].Result.Winner == nil {
			continue
		}
		loser := -1
		for s, d := range golden[i].Result.Deltas {
			if d < 0 {
				loser = s
				break
			}
		}
		if loser >= 0 {
			target = &golden[i]
			break
		}
	}
	if target == nil {
		t.Fatalf("golden 中没有可用的荣和样本")
	}
	var scores [4]int
	copy(scores[:], target.Setup.Scores)
	for s, d := range target.Result.Deltas {
		if d < 0 {
			scores[s] = 2000 // 这一手放铳 -2900 后为 -900 → 击飞
		}
	}
	m := newGoldenMatch(t, *target, &scores)
	m.pos.Kyoku = 1
	replayGolden(t, m, *target)

	if !m.Finished() {
		t.Fatalf("放铳者点数跌破 0 应触发击飞终局，实际未结束（pos=%+v history=%d）", m.Position(), len(m.history))
	}
	for seat := 0; seat < Seats; seat++ {
		view := m.View(seat)
		if _, ok := view["handResult"]; !ok {
			t.Fatalf("整场结束时 seat %d 仍应下发最后一手 handResult", seat)
		}
		if match, _ := view["match"].(map[string]interface{}); match != nil {
			if fin, ok := match["finished"].(bool); !ok || !fin {
				t.Fatalf("整场结束时 match.finished 应为 true，实际 %v", match["finished"])
			}
		}
	}
}
