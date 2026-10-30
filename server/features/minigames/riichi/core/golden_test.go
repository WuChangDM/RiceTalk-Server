package core

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/okki-0417/mahjong/kyoku"
	"github.com/okki-0417/mahjong/ruleset"
	"github.com/okki-0417/mahjong/tile"
)

// 与库自带 testdata/kyoku.jsonl 同构（截取 6 手，见 testdata/golden_kyoku.jsonl）。
type goldenLine struct {
	Wall  []string `json:"wall"`
	Setup struct {
		DealerSeat   int    `json:"dealer_seat"`
		RoundWind    string `json:"round_wind"`
		KyokuNumber  int    `json:"kyoku_number"`
		Honba        int    `json:"honba"`
		RiichiSticks int    `json:"riichi_sticks"`
		Scores       []int  `json:"scores"`
	} `json:"setup"`
	RuleSet struct {
		Kuitan        bool `json:"kuitan"`
		RoundUpMangan bool `json:"round_up_mangan"`
		NagashiMangan bool `json:"nagashi_mangan"`
	} `json:"rule_set"`
	Steps []struct {
		Chosen struct {
			Seat   int      `json:"seat"`
			Kind   string   `json:"kind"`
			Tiles  []string `json:"tiles"`
			Called *string  `json:"called"`
		} `json:"chosen"`
	} `json:"steps"`
	Result struct {
		Kind            string `json:"kind"`
		Winner          *int   `json:"winner"`
		Deltas          []int  `json:"deltas"`
		Scores          []int  `json:"scores"`
		DealerContinues bool   `json:"dealer_continues"`
	} `json:"result"`
	Next *struct {
		DealerSeat   int    `json:"dealer_seat"`
		RoundWind    string `json:"round_wind"`
		KyokuNumber  int    `json:"kyoku_number"`
		Honba        int    `json:"honba"`
		RiichiSticks int    `json:"riichi_sticks"`
		Scores       []int  `json:"scores"`
	} `json:"next"`
}

func loadGolden(t *testing.T) []goldenLine {
	t.Helper()
	f, err := os.Open("testdata/golden_kyoku.jsonl")
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer f.Close()
	var out []goldenLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 32*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var g goldenLine
		if err := json.Unmarshal(line, &g); err != nil {
			t.Fatalf("unmarshal golden line: %v", err)
		}
		out = append(out, g)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan golden: %v", err)
	}
	return out
}

// TestGoldenFixturesReplay 用库自带的真实对局 golden 数据回放我们自己的动作映射与结算推进：
//  1. wall + setup 重建牌局；
//  2. 逐条 steps[].chosen 通过 core.Match.Apply 走我方 buildAction 路径；
//  3. 断言本局结束、结算 kind 与 fixture result.kind 一致；
//  4. 断言我方 succeeding() 复刻出的下一局 setup（庄家/风/局号/本场/立直棒/点数）与 fixture next 一致。
func TestGoldenFixturesReplay(t *testing.T) {
	golden := loadGolden(t)
	if len(golden) < 5 {
		t.Fatalf("golden 用例不足 5 条，实际 %d", len(golden))
	}
	for idx, g := range golden {
		g := g
		t.Run(fixtureName(idx), func(t *testing.T) {
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
			m := &Match{cfg: Config{MatchType: "east"}, rules: rs, pos: pos, cur: k}

			for i, st := range g.Steps {
				called := ""
				if st.Chosen.Called != nil {
					called = *st.Chosen.Called
				}
				if err := m.Apply(st.Chosen.Seat, st.Chosen.Kind, st.Chosen.Tiles, called); err != nil {
					t.Fatalf("第 %d 步（%s seat=%d tiles=%v）应用失败：%v",
						i, st.Chosen.Kind, st.Chosen.Seat, st.Chosen.Tiles, err)
				}
			}
			hist := m.History()
			if len(hist) != 1 {
				t.Fatalf("本局结束后应记录 1 条历史，实际 %d", len(hist))
			}
			if want := g.Result.Kind; hist[0].Kind != want {
				t.Fatalf("结算类型应为 %q，实际 %q", want, hist[0].Kind)
			}
			if g.Next == nil {
				return // 该手为整场最后一局，无下一局可对照
			}
			if m.Finished() {
				return // 击飞/终局不再开新局
			}
			got := m.Position()
			if got.DealerSeat != g.Next.DealerSeat ||
				got.RoundWind != g.Next.RoundWind ||
				got.Kyoku != g.Next.KyokuNumber ||
				got.Honba != g.Next.Honba ||
				got.RiichiSticks != g.Next.RiichiSticks {
				t.Fatalf("下一局 setup 不一致：got %+v want dealer=%d wind=%s kyoku=%d honba=%d sticks=%d",
					got, g.Next.DealerSeat, g.Next.RoundWind, g.Next.KyokuNumber, g.Next.Honba, g.Next.RiichiSticks)
			}
			for i := 0; i < 4; i++ {
				if got.Scores[i] != g.Next.Scores[i] {
					t.Fatalf("下一局点数与 fixture next 不一致：seat %d got %d want %d（got=%v want=%v）",
						i, got.Scores[i], g.Next.Scores[i], got.Scores, g.Next.Scores)
				}
			}
		})
	}
}

func fixtureName(i int) string {
	names := []string{"hand0", "hand1", "hand2", "hand3", "hand4", "hand5"}
	if i < len(names) {
		return names[i]
	}
	return "hand" + string(rune('0'+i%10))
}
