package core

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/okki-0417/mahjong/tile"
)

// 赤五（task-103）：配置未显式给出时必须默认启用，且仍可显式关闭。
//
// 背景：Config.RedFives 原为 bool，零值 false 被当作「关闭」，而注释写「默认 true」，
// 引擎 Init 传 Config{} → 牌山永无 0m/0p/0s；客户端赤宝渲染与排序全成死代码。

var redFiveLabel = regexp.MustCompile(`^0[mps]$`)

func countRedFives(labels []string) int {
	n := 0
	for _, l := range labels {
		if redFiveLabel.MatchString(l) {
			n++
		}
	}
	return n
}

// wallLabels 取当前牌局牌山的标签（本包内可直接访问 kyoku.Wall）。
func wallLabels(t *testing.T, m *Match) []string {
	t.Helper()
	if m.cur == nil {
		t.Fatalf("牌局未初始化")
	}
	return labelsOf(m.cur.Wall().Tiles())
}

func labelsOf(tiles []tile.Tile) []string {
	out := make([]string, 0, len(tiles))
	for _, tl := range tiles {
		out = append(out, tl.String())
	}
	return out
}

func TestNormalizeRedFivesDefault(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantUse bool
	}{
		{"空配置默认启用", Config{}, true},
		{"显式 true", Config{}.WithRedFives(true), true},
		{"显式 false 保留关闭", Config{}.WithRedFives(false), false},
		{"旧快照字段缺失（反序列化后为 nil）-> 启用", Config{MatchType: MatchEast, StartingScore: 25000}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Normalize().UseRedFives(); got != tc.wantUse {
				t.Fatalf("UseRedFives()=%v，期望 %v", got, tc.wantUse)
			}
		})
	}
}

func TestNormalizeRedFivesIdempotent(t *testing.T) {
	a := Config{}.Normalize()
	b := a.Normalize()
	if a.RedFives == nil || b.RedFives == nil {
		t.Fatalf("Normalize 后 RedFives 不应为 nil")
	}
	if a.RedFives != b.RedFives {
		t.Fatalf("重复 Normalize 应复用同一默认指针（保证可比较/幂等）")
	}
	if !a.UseRedFives() || !b.UseRedFives() {
		t.Fatalf("重复 Normalize 后仍应启用赤五")
	}
}

func TestWallContainsRedFivesByDefault(t *testing.T) {
	const seed = int64(20261031)
	m, err := NewMatch(seed, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	labels := wallLabels(t, m)
	if len(labels) != 136 {
		t.Fatalf("牌山应为 136 张，实际 %d", len(labels))
	}
	// 库的 FullSet(red=true) 里赤五每种恰好 1 张：0m/0p/0s 共 3 张。
	want := map[string]int{"0m": 1, "0p": 1, "0s": 1}
	for label, wantN := range want {
		got := 0
		for _, l := range labels {
			if l == label {
				got++
			}
		}
		if got != wantN {
			t.Fatalf("牌山中 %s 应有 %d 张，实际 %d（默认配置应启用赤五）", label, wantN, got)
		}
	}
	if total := countRedFives(labels); total != 3 {
		t.Fatalf("牌山赤五总数应为 3，实际 %d", total)
	}
}

func TestWallWithoutRedFivesWhenDisabled(t *testing.T) {
	const seed = int64(20261031)
	m, err := NewMatch(seed, Config{}.WithRedFives(false))
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	labels := wallLabels(t, m)
	if len(labels) != 136 {
		t.Fatalf("牌山应为 136 张，实际 %d", len(labels))
	}
	if n := countRedFives(labels); n != 0 {
		t.Fatalf("显式关闭赤五时牌山不应含 0m/0p/0s，实际 %d 张", n)
	}
}

func TestSnapshotRestoreKeepsRedFives(t *testing.T) {
	const seed = int64(777)
	m, err := NewMatch(seed, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	before := wallLabels(t, m)

	raw, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var back Snapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	restored, err := Restore(back)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !restored.Snapshot().Config.UseRedFives() {
		t.Fatalf("快照往返后仍应启用赤五")
	}
	if got := wallLabels(t, restored); !equalStrings(got, before) {
		t.Fatalf("快照往返后牌山应与原始一致")
	}

	// 继续打一手：确定性重放不因 Config 指针化而破坏。
	legal := restored.cur.Kyokumen().LegalActions()
	if len(legal) == 0 {
		t.Fatalf("还原后应存在合法动作")
	}
	act := legal[0]
	if err := restored.Apply(act.Seat(), act.Kind().String(), labelsOf(act.Tiles()), ""); err != nil {
		t.Fatalf("还原后继续出牌失败：%v", err)
	}
}

func TestConfigJSONBackwardCompatible(t *testing.T) {
	// 旧快照没有 redFives 字段 → 反序列化后应为 nil → UseRedFives()=true。
	var old Config
	if err := json.Unmarshal([]byte(`{"matchType":"east","startingScore":25000}`), &old); err != nil {
		t.Fatalf("unmarshal 旧配置: %v", err)
	}
	if old.RedFives != nil {
		t.Fatalf("旧配置缺失 redFives 字段时应为 nil")
	}
	if !old.Normalize().UseRedFives() {
		t.Fatalf("旧配置应恢复为启用赤五")
	}

	// 显式 false 必须能序列化并在往返后保持关闭。
	raw, err := json.Marshal(Config{}.WithRedFives(false))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rt Config
	if err := json.Unmarshal(raw, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rt.UseRedFives() {
		t.Fatalf("显式 false 往返后应为关闭，实际 %s", raw)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
