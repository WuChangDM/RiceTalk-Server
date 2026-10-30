package core

import (
	"errors"
	"testing"

	"github.com/okki-0417/mahjong/kyoku"
)

// 动作词表覆盖：客户端 action 字符串 ↔ 库 Action 构造器。这里逐条断言映射可用，
// 防止后续改动把某个动作名写错（写错只会在真实对局里才暴露）。
func TestBuildActionCoversVocabulary(t *testing.T) {
	cases := []struct {
		kind   string
		tiles  []string
		called string
	}{
		{"discard", []string{"1m"}, ""},
		{"riichi", []string{"2p"}, ""},
		{"ankan", []string{"3s"}, ""},
		{"kakan", []string{"4m"}, ""},
		{"chi", []string{"5m", "6m"}, "4m"},
		{"pon", []string{"7z", "7z"}, "7z"},
		{"minkan", []string{"8p"}, "8p"},
		{"tsumo", nil, ""},
		{"ron", nil, ""},
		{"pass", nil, ""},
		{"handoff", nil, ""},
		{"rinshan", nil, ""},
		{"kyushukyuhai", nil, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.kind, func(t *testing.T) {
			a, err := buildAction(tc.kind, 2, tc.tiles, tc.called)
			if err != nil {
				t.Fatalf("buildAction(%q) 失败：%v", tc.kind, err)
			}
			if got := a.Kind().String(); got != tc.kind {
				t.Fatalf("kind 往返应为 %q，实际 %q", tc.kind, got)
			}
			if got := a.Seat(); got != 2 {
				t.Fatalf("seat 应为 2，实际 %d", got)
			}
		})
	}
}

// 牌数不合法必须被拒（服务端不信任客户端给的牌数）。
func TestBuildActionRejectsWrongTileCount(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		tiles []string
	}{
		{"chi 只给 1 张", "chi", []string{"5m"}},
		{"pon 给 3 张", "pon", []string{"7z", "7z", "7z"}},
		{"discard 给 2 张", "discard", []string{"1m", "2m"}},
		{"ankan 给 0 张", "ankan", nil},
		{"tsumo 给 1 张", "tsumo", []string{"1m"}},
		{"未知动作名", "teleport", nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildAction(tc.kind, 0, tc.tiles, ""); err == nil {
				t.Fatalf("buildAction(%q, %v) 应返回错误", tc.kind, tc.tiles)
			}
		})
	}
}

// 牌山确定性：同 seed 必须复现同一副牌，不同 seed 必须不同。
func TestDeterministicWallAcrossSeeds(t *testing.T) {
	m1, err := NewMatch(12345, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	m2, err := NewMatch(12345, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	m3, err := NewMatch(54321, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	want, ok := m1.View(0)["closed_tiles"].([]interface{})
	if !ok {
		t.Fatal("庄家视图缺少 closed_tiles")
	}
	got, ok := m2.View(0)["closed_tiles"].([]interface{})
	if !ok {
		t.Fatal("同 seed 第二局庄家视图缺少 closed_tiles")
	}
	if len(want) != len(got) {
		t.Fatalf("同 seed 手牌数量不一致：%d vs %d", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("同 seed 手牌不一致：第 %d 张 %v vs %v", i, want[i], got[i])
		}
	}
	if len(want) > 0 {
		if other, ok := m3.View(0)["closed_tiles"].([]interface{}); ok && len(other) == len(want) {
			same := true
			for i := range want {
				if want[i] != other[i] {
					same = false
					break
				}
			}
			if same {
				t.Fatal("不同 seed 生成了完全相同的起手牌（确定性牌山异常）")
			}
		}
	}
}

// 一局结束后继续提交动作必须被拒（引擎层防重复结算的入口）。
func TestApplyAfterHandFinishedRejected(t *testing.T) {
	m, err := NewMatch(2026, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	// 用「有 Pass 先 Pass，否则取第一个合法动作」的简单策略推进到本局结束。
	for i := 0; i < 500 && len(m.History()) == 0; i++ {
		var pick *kyoku.Action
		for _, a := range m.cur.Kyokumen().LegalActions() {
			if a.Kind() == kyoku.ActionPass {
				aa := a
				pick = &aa
				break
			}
		}
		if pick == nil {
			acts := m.cur.Kyokumen().LegalActions()
			if len(acts) == 0 {
				t.Fatalf("第 %d 步：当前无合法动作但本局未结束", i)
			}
			aa := acts[0]
			pick = &aa
		}
		if err := m.applyKyoku(*pick); err != nil {
			t.Fatalf("第 %d 步应用 %s 失败：%v", i, pick.Kind().String(), err)
		}
	}
	if len(m.History()) == 0 {
		t.Fatal("自动对局 500 步内未产生结算")
	}

	// 继续自动对局直到整场结束，之后再提交动作必须被拒（防重复结算）。
	for i := 0; i < 20000 && !m.Finished(); i++ {
		acts := m.cur.Kyokumen().LegalActions()
		if len(acts) == 0 {
			t.Fatalf("第 %d 步：当前无合法动作但整场未结束", i)
		}
		if err := m.applyKyoku(acts[0]); err != nil {
			t.Fatalf("第 %d 步应用动作失败：%v", i, err)
		}
	}
	if !m.Finished() {
		t.Fatal("自动对局 20000 步内未结束整场")
	}
	if err := m.Apply(0, "discard", []string{"1m"}, ""); !errors.Is(err, ErrFinished) {
		t.Fatalf("整场结束后提交动作应返回 ErrFinished，实际 %v", err)
	}
}
