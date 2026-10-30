package core

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// seed 必须落在 float64 可精确表示的区间内：state 存成 JSON 文本后，
// 生产路径（handler）会反序列化成 map[string]interface{}，所有数字变 float64，
// 超过 2^53 的整数会被舍入，牌山随之改变 —— 表现为「偶发 invalid game action」。
func TestSeedRangeGuard(t *testing.T) {
	if _, err := NewMatch(MaxSeed-1, Config{}); err != nil {
		t.Fatalf("边界内 seed 应可用，实际报错：%v", err)
	}
	if _, err := NewMatch(MaxSeed, Config{}); err == nil {
		t.Fatal("seed == 2^53 应被拒绝（超出 float64 精确整数范围）")
	}
	if _, err := NewMatch(-1, Config{}); err == nil {
		t.Fatal("负数 seed 应被拒绝")
	}
	if _, err := NewMatch(math.MaxInt64, Config{}); err == nil {
		t.Fatal("MaxInt64 seed 应被拒绝")
	}

	m, err := NewMatch(1, Config{})
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	snap := m.Snapshot()
	snap.Seed = -7
	if _, err := Restore(snap); err == nil {
		t.Fatal("Restore 收到负数 seed 应报错")
	}
	// 历史 state 可能存着 >2^53 的 seed：只要值精确可表示，Restore 仍应接受
	// （兼容旧行，避免直接 400；新对局已由 NewMatch 拦在 <MaxSeed）。
	snap.Seed = MaxSeed
	if _, err := Restore(snap); err != nil {
		t.Fatalf("Restore 应接受精确可表示的历史大 seed，实际报错：%v", err)
	}
}

// 模拟生产路径的精度损失：Snapshot → JSON → map[string]interface{} → JSON → Snapshot，
// 还原出的牌局必须与原始牌局完全一致（四家起手牌 + 宝牌指示牌）。
func TestSeedSurvivesFloat64JSONRoundTrip(t *testing.T) {
	for i := 0; i < 50; i++ {
		seed := (time.Now().UnixNano() + int64(i)*7919) & (MaxSeed - 1)
		src, err := NewMatch(seed, Config{})
		if err != nil {
			t.Fatalf("NewMatch(seed=%d): %v", seed, err)
		}
		raw, err := json.Marshal(src.Snapshot())
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		var generic map[string]interface{}
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatalf("unmarshal to map: %v", err)
		}
		again, err := json.Marshal(generic)
		if err != nil {
			t.Fatalf("re-marshal map: %v", err)
		}
		var snap Snapshot
		if err := json.Unmarshal(again, &snap); err != nil {
			t.Fatalf("unmarshal snapshot: %v", err)
		}
		if snap.Seed != seed {
			t.Fatalf("seed 经 map 往返后变化：%d → %d", seed, snap.Seed)
		}
		restored, err := Restore(snap)
		if err != nil {
			t.Fatalf("Restore(seed=%d): %v", seed, err)
		}
		for seat := 0; seat < Seats; seat++ {
			want := src.View(seat)
			got := restored.View(seat)
			if !equalJSON(want["closed_tiles"], got["closed_tiles"]) {
				t.Fatalf("seed=%d seat=%d 暗牌经 map 往返后不一致：%v vs %v",
					seed, seat, want["closed_tiles"], got["closed_tiles"])
			}
			if !equalJSON(want["drawn"], got["drawn"]) {
				t.Fatalf("seed=%d seat=%d 摸牌经 map 往返后不一致：%v vs %v",
					seed, seat, want["drawn"], got["drawn"])
			}
			if !equalJSON(want["dora_indicators"], got["dora_indicators"]) {
				t.Fatalf("seed=%d seat=%d 宝牌指示牌经 map 往返后不一致", seed, seat)
			}
		}
		// 同一动作在两份牌局上的结果必须一致（牌山完全对齐）。
		wantActs := src.cur.Kyokumen().LegalActions()
		if len(wantActs) == 0 {
			t.Fatalf("seed=%d 开局无合法动作", seed)
		}
		if err := restored.applyKyoku(wantActs[0]); err != nil {
			t.Fatalf("seed=%d 还原牌局应用首个合法动作失败：%v", seed, err)
		}
		if err := src.applyKyoku(wantActs[0]); err != nil {
			t.Fatalf("seed=%d 原始牌局应用首个合法动作失败：%v", seed, err)
		}
		if !equalJSON(src.View(0), restored.View(0)) {
			t.Fatalf("seed=%d 应用同一动作后局面不一致", seed)
		}
	}
}

func equalJSON(a, b interface{}) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}
