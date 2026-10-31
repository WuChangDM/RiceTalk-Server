package riichi

import (
	"regexp"
	"testing"

	"ridgericetalk/features/minigames/riichi/core"
)

// 赤五（task-103）引擎层回归：默认配置必须启用赤五，且固定 seed 下能确定性看到赤宝牌码。

var redFiveCode = regexp.MustCompile(`^0[mps]$`)

// collectSeatTiles 汇总四家视角里可见的牌（手牌 closed_tiles + 刚摸的 drawn），
// 用于「固定 seed 下起手含赤五」的确定性断言。
func collectSeatTiles(t *testing.T, state map[string]interface{}) []string {
	t.Helper()
	views, ok := state["views"].([]interface{})
	if !ok {
		t.Fatalf("state.views 类型异常：%T", state["views"])
	}
	out := make([]string, 0, 64)
	for _, v := range views {
		view, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		for _, key := range []string{"closed_tiles", "drawn"} {
			raw, ok := view[key].([]interface{})
			if !ok {
				continue
			}
			for _, item := range raw {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func countRedFiveCodes(codes []string) int {
	n := 0
	for _, c := range codes {
		if redFiveCode.MatchString(c) {
			n++
		}
	}
	return n
}

// Init 走默认配置（Config{}）：归一化后必须启用赤五。
func TestInitDefaultConfigEnablesRedFives(t *testing.T) {
	state, _ := initState(t)
	snap, ok := snapshotFromState(state)
	if !ok {
		t.Fatalf("state.snapshot 缺失或不可解析")
	}
	if !snap.Config.UseRedFives() {
		t.Fatalf("Engine.Init 默认配置应启用赤五，实际关闭（Config.RedFives 归一化缺失）")
	}
}

// 固定 seed + 默认配置：走真实 Engine.Init 路径，起手（四家 13 张 + 亲家摸牌）
// 确定含赤五；显式关闭时确定不含（同一 seed 对照，不做概率断言）。
func TestFixedSeedHandsContainRedFive(t *testing.T) {
	const seed = int64(20261031)
	withFixedSeed(t, seed)

	state, _ := initState(t)
	codes := collectSeatTiles(t, state)
	if n := countRedFiveCodes(codes); n < 1 {
		t.Fatalf("seed=%d 起手应至少含 1 张赤五（0m/0p/0s），实际 0 张；四家可见牌=%v", seed, codes)
	}

	off, err := core.NewMatch(seed, core.Config{}.WithRedFives(false))
	if err != nil {
		t.Fatalf("NewMatch(关闭赤五): %v", err)
	}
	offState, err := buildState([]string{"u-east", "u-south", "u-west", "u-north"}, off)
	if err != nil {
		t.Fatalf("buildState(关闭赤五): %v", err)
	}
	offCodes := collectSeatTiles(t, offState)
	if n := countRedFiveCodes(offCodes); n != 0 {
		t.Fatalf("显式关闭赤五时起手不应含赤宝，实际 %d 张", n)
	}
}

// withFixedSeed 让 Engine.Init 使用固定牌山 seed（仅测试用；t.Cleanup 恢复）。
func withFixedSeed(t *testing.T, seed int64) {
	t.Helper()
	old := newMatchSeed
	newMatchSeed = func() int64 { return seed }
	t.Cleanup(func() { newMatchSeed = old })
}
