package riichi

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"ridgericetalk/features/minigames/riichi/core"
)

// L-1（1.9.4 安全）：牌山 seed 熵相关用例。
//
// 背景：seed 原先取 time.Now().UnixNano()，熵不足——同毫秒并发建房、可预测时钟、
// 进程启动时刻都可能被推断，牌山可被猜中（对局公平性）。现改用 crypto/rand，
// 并强制落在 [0, core.MaxSeed)（JSON float64 往返安全范围，见 core.MaxSeed 注释）。

// TestSeedFromUint64Bounds 校验裁剪函数边界：任何 64 位输入都必须落进 [0, MaxSeed)。
func TestSeedFromUint64Bounds(t *testing.T) {
	cases := []struct {
		name string
		in   uint64
		want int64
	}{
		{"zero", 0, 0},
		{"max-1", uint64(core.MaxSeed - 1), core.MaxSeed - 1},
		{"max", uint64(core.MaxSeed), 0},
		{"max+1", uint64(core.MaxSeed) + 1, 1},
		{"all-ones", math.MaxUint64, core.MaxSeed - 1},
	}
	for _, tc := range cases {
		if got := seedFromUint64(tc.in); got != tc.want {
			t.Errorf("%s: seedFromUint64(%d) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
		if got := seedFromUint64(tc.in); got < 0 || got >= core.MaxSeed {
			t.Errorf("%s: seed %d 越界，必须在 [0, %d)", tc.name, got, core.MaxSeed)
		}
	}
}

// TestRandomMatchSeedInValidRange 校验生产取熵路径的每个 seed 都能被 core.NewMatch 接受。
func TestRandomMatchSeedInValidRange(t *testing.T) {
	for i := 0; i < 2000; i++ {
		seed := newMatchSeed()
		if seed < 0 || seed >= core.MaxSeed {
			t.Fatalf("第 %d 次取熵越界：seed=%d，必须在 [0, %d)", i, seed, core.MaxSeed)
		}
		if _, err := core.NewMatch(seed, core.Config{}); err != nil {
			t.Fatalf("第 %d 次取熵的 seed=%d 被 core.NewMatch 拒绝：%v", i, seed, err)
		}
	}
}

// TestRandomMatchSeedDistinct 校验取熵不退化（连续取值不应重复/呈时间戳序列）。
func TestRandomMatchSeedDistinct(t *testing.T) {
	const n = 200
	seen := make(map[int64]struct{}, n)
	for i := 0; i < n; i++ {
		seed := newMatchSeed()
		if _, dup := seen[seed]; dup {
			t.Fatalf("第 %d 次取熵与前面重复：seed=%d（随机源退化）", i, seed)
		}
		seen[seed] = struct{}{}
	}
}

// TestRandomMatchSeedNotDerivedFromTimestamp 防止取熵退化为 time.Now()：
// 时间戳派生的 seed = UnixNano mod 2^53，在测试执行期间必然落在「当前时间戳裁剪值」
// 附近 ±1 秒的纳秒窄带内（|seed-base| < 1e9）；而 crypto/rand 取值在 [0,2^53) 上均匀，
// 单个落入该带概率约 2e-7，64 次全落空概率极高。用循环距离判定（2^53 环上取最短距离）。
func TestRandomMatchSeedNotDerivedFromTimestamp(t *testing.T) {
	base := seedFromUint64(uint64(time.Now().UnixNano()))
	const narrowWindow int64 = 1_000_000_000 // ±1s 的纳秒窗口
	for i := 0; i < 64; i++ {
		seed := newMatchSeed()
		dist := seed - base
		if dist < 0 {
			dist = -dist
		}
		if dist > core.MaxSeed/2 {
			dist = core.MaxSeed - dist
		}
		if dist < narrowWindow {
			t.Fatalf("第 %d 个随机 seed=%d 落在时间戳裁剪值 %d 的 ±1s 纳秒带内（距离 %d），疑似取熵退化为 time.Now()",
				i, seed, base, dist)
		}
	}
}

// TestRandomSeedSurvivesSnapshotRoundTrip 校验 crypto/rand 生成的 seed 经
// 「Snapshot → JSON → seedFromAny / core.Restore」全链路精确还原（session 落库/读回路径）。
func TestRandomSeedSurvivesSnapshotRoundTrip(t *testing.T) {
	for i := 0; i < 50; i++ {
		seed := newMatchSeed()
		m, err := core.NewMatch(seed, core.Config{})
		if err != nil {
			t.Fatalf("第 %d 次 NewMatch(seed=%d) 失败：%v", i, seed, err)
		}
		raw, err := json.Marshal(m.Snapshot())
		if err != nil {
			t.Fatalf("第 %d 次 marshal snapshot 失败：%v", i, err)
		}
		// 1) 走 seedFromAny（map 形态，engine 实际读回路径）
		var round map[string]interface{}
		if err := json.Unmarshal(raw, &round); err != nil {
			t.Fatalf("第 %d 次 unmarshal snapshot 失败：%v", i, err)
		}
		got, ok := seedFromAny(round["seed"])
		if !ok || got != seed {
			t.Fatalf("第 %d 次 seedFromAny 往返失败：got=%d ok=%v want=%d", i, got, ok, seed)
		}
		// 2) 走 core.Restore（强类型快照恢复路径）
		var back core.Snapshot
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("第 %d 次 unmarshal 到 core.Snapshot 失败：%v", i, err)
		}
		if back.Seed != seed {
			t.Fatalf("第 %d 次 Snapshot.Seed = %d，want %d", i, back.Seed, seed)
		}
		if _, err := core.Restore(back); err != nil {
			t.Fatalf("第 %d 次 core.Restore(seed=%d) 失败：%v", i, seed, err)
		}
	}
}

// TestNewMatchSeedInjectionStillWorks 保护测试注入契约：newMatchSeed 仍是可替换的
// 包级变量（redfives_engine_test.go 等用注入固定 seed 做确定性断言）。
func TestNewMatchSeedInjectionStillWorks(t *testing.T) {
	old := newMatchSeed
	defer func() { newMatchSeed = old }()

	const fixed int64 = 20261031
	newMatchSeed = func() int64 { return fixed }

	players := []map[string]interface{}{
		{"userId": "alice"}, {"userId": "bob"}, {"userId": "carol"}, {"userId": "dave"},
	}
	state, err := (&Engine{}).Init(players)
	if err != nil {
		t.Fatalf("注入 seed 后 Init 失败：%v", err)
	}
	// state 经 JSON 往返（模拟 session.State 落库/读回）后 seed 必须精确还原。
	// seed 存在 state.snapshot.seed（core.Snapshot.Seed），不是顶层字段。
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state 失败：%v", err)
	}
	var round map[string]interface{}
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal state 失败：%v", err)
	}
	snap, ok := round["snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("JSON 往返后 state.snapshot 不是对象：%T", round["snapshot"])
	}
	seed, ok := seedFromAny(snap["seed"])
	if !ok {
		t.Fatalf("JSON 往返后取不到 seed，snapshot.seed=%v", snap["seed"])
	}
	if seed != fixed {
		t.Fatalf("JSON 往返后 seed = %d，want %d（注入的确定性 seed 必须精确还原）", seed, fixed)
	}
}
