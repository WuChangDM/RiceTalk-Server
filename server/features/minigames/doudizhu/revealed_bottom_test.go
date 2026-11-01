package doudizhu

import (
	"encoding/json"
	"testing"
)

// 1.9.4（task-119）：斗地主「底牌翻明」服务端契约。
// 叫地主前不得暴露底牌；地主确定后 3 张底牌以 revealedBottomCards 下发给
// 所有对局视角（玩家/观战者），且内部 bottomCards 仍按原逻辑清空。

// newRevealSeededState 起一局三人斗地主（确定性视角，用真实 Init 发牌）。
func newRevealSeededState(t *testing.T) (engine *Engine, state map[string]interface{}) {
	t.Helper()
	engine = New()
	state, err := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	if err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	return engine, state
}

func bid(t *testing.T, engine *Engine, state map[string]interface{}, userID string, score int) map[string]interface{} {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{"score": score})
	next, err := engine.ValidateMove(state, userID, "bid", payload)
	if err != nil {
		t.Fatalf("%s 叫分 %d 失败: %v", userID, score, err)
	}
	return next
}

// revealLandlord 走完叫分：a 叫 3 分成为地主，b/c 不叫，返回进入 play 阶段的 state。
func revealLandlord(t *testing.T, engine *Engine, state map[string]interface{}) map[string]interface{} {
	t.Helper()
	state = bid(t, engine, state, "a", 3)
	state = bid(t, engine, state, "b", 0)
	state = bid(t, engine, state, "c", 0)
	if phase, _ := state["phase"].(string); phase != "play" {
		t.Fatalf("叫分结束后应进入 play 阶段，实际 phase=%v", state["phase"])
	}
	return state
}

// TestDoudizhuRevealedBottomCardsAbsentBeforeBid 叫地主前不得出现 revealedBottomCards。
func TestDoudizhuRevealedBottomCardsAbsentBeforeBid(t *testing.T) {
	engine, state := newRevealSeededState(t)

	if _, ok := state["revealedBottomCards"]; ok {
		raw, _ := json.Marshal(state["revealedBottomCards"])
		t.Fatalf("叫地主前不得下发 revealedBottomCards，实际=%s", raw)
	}
	if cards := engine.revealedBottomCards(state); len(cards) != 0 {
		t.Fatalf("叫地主前 revealedBottomCards 应为空，实际 %d 张", len(cards))
	}
	// 玩家视角在叫地主前同样不得出现该字段（防止顺手翻明）。
	view := engine.PlayerState(state, "a")
	if _, ok := view["revealedBottomCards"]; ok {
		t.Fatalf("叫地主前玩家视角不得出现 revealedBottomCards")
	}
}

// TestDoudizhuRevealedBottomCardsAfterLandlordConfirmed 地主确定后恰好 3 张，且与发牌一致、不与牌堆重复。
func TestDoudizhuRevealedBottomCardsAfterLandlordConfirmed(t *testing.T) {
	engine, state := newRevealSeededState(t)

	dealtBottom := append([]card{}, engine.bottomCards(state)...)
	if len(dealtBottom) != 3 {
		t.Fatalf("发牌后底牌应为 3 张，实际 %d", len(dealtBottom))
	}

	state = revealLandlord(t, engine, state)

	revealed := engine.revealedBottomCards(state)
	if len(revealed) != 3 {
		t.Fatalf("地主确定后 revealedBottomCards 应为 3 张，实际 %d: %+v", len(revealed), revealed)
	}
	// 内部 bottomCards 仍按原逻辑清空，出牌/校验逻辑不受影响。
	if internal := engine.bottomCards(state); len(internal) != 0 {
		t.Fatalf("内部 bottomCards 应被清空，实际 %d 张: %+v", len(internal), internal)
	}
	// 翻明的就是发牌时留的 3 张（顺序可不同，按牌面集合比较）。
	if !sameCardSet(revealed, dealtBottom) {
		t.Fatalf("翻明底牌与发牌底牌不一致：revealed=%+v dealt=%+v", revealed, dealtBottom)
	}
	// 3 张之间互不重复。
	if dup := duplicateCard(revealed); dup != "" {
		t.Fatalf("翻明底牌内部出现重复牌: %s (%+v)", dup, revealed)
	}
	// 全牌堆仍是完整 54 张（地主手牌已并入底牌，翻明字段只是视图副本，不改变牌堆）。
	hands := engine.handsMap(state)
	if len(hands) != 3 {
		t.Fatalf("应有三家手牌，实际 %d", len(hands))
	}
	if n := len(hands["a"]) + len(hands["b"]) + len(hands["c"]); n != 54 {
		t.Fatalf("三家手牌合计应为 54 张，实际 %d", n)
	}
	deck := make([]card, 0, 54)
	for _, uid := range []string{"a", "b", "c"} {
		deck = append(deck, hands[uid]...)
	}
	if dup := duplicateCard(deck); dup != "" {
		t.Fatalf("牌堆出现重复牌: %s", dup)
	}
	// 底牌已给地主：地主手牌必须包含这 3 张。
	landlordHand := hands["a"]
	for _, want := range revealed {
		found := false
		for _, got := range landlordHand {
			if got.Rank == want.Rank && got.Suit == want.Suit {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("地主手牌缺少翻明底牌 %+v", want)
		}
	}
}

// TestDoudizhuRevealedBottomCardsJSONRoundTrip 状态落库/跨进程（JSON 往返）后仍能解析出翻明底牌。
func TestDoudizhuRevealedBottomCardsJSONRoundTrip(t *testing.T) {
	engine, state := newRevealSeededState(t)
	state = revealLandlord(t, engine, state)
	want := engine.revealedBottomCards(state)

	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("state 序列化失败: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("state 反序列化失败: %v", err)
	}
	got := engine.revealedBottomCards(decoded)
	if len(got) != 3 {
		t.Fatalf("JSON 往返后 revealedBottomCards 应为 3 张，实际 %d，原文=%s", len(got), raw)
	}
	if !sameCardSet(got, want) {
		t.Fatalf("JSON 往返后翻明底牌不一致：got=%+v want=%+v", got, want)
	}
	// 客户端拿到的就是 `revealedBottomCards` 这个 JSON key，字段结构需稳定。
	var probe struct {
		RevealedBottomCards []struct {
			Rank  int    `json:"rank"`
			Suit  string `json:"suit"`
			Value string `json:"value"`
		} `json:"revealedBottomCards"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("按客户端结构解析失败: %v", err)
	}
	if len(probe.RevealedBottomCards) != 3 {
		t.Fatalf("客户端结构解析 revealedBottomCards 应为 3 项，实际 %d", len(probe.RevealedBottomCards))
	}
	for _, c := range probe.RevealedBottomCards {
		if c.Rank < 3 || c.Rank > 17 || c.Suit == "" || c.Value == "" {
			t.Fatalf("翻明底牌字段不完整: %+v", c)
		}
	}
}

// TestDoudizhuRevealedBottomCardsVisibleInPlayerAndSpectatorViews 翻明后玩家/观战者都能看到，且手牌脱敏不变。
func TestDoudizhuRevealedBottomCardsVisibleInPlayerAndSpectatorViews(t *testing.T) {
	engine, state := newRevealSeededState(t)
	state = revealLandlord(t, engine, state)
	want := engine.revealedBottomCards(state)

	playerView := engine.PlayerState(state, "b")
	spectatorView := engine.SpectatorState(state)
	for name, view := range map[string]map[string]interface{}{"player": playerView, "spectator": spectatorView} {
		got := engine.revealedBottomCards(view)
		if len(got) != 3 || !sameCardSet(got, want) {
			t.Fatalf("%s 视角 revealedBottomCards 应为翻明的 3 张，实际=%+v want=%+v", name, got, want)
		}
	}
	// 脱敏红线不变：玩家 b 只能看自己手牌，看不到 a/c 手牌明细。
	hands, _ := playerView["hands"].(map[string][]card)
	if len(hands) != 1 {
		t.Fatalf("玩家视角 hands 应只含本人，实际 %d 项", len(hands))
	}
	if _, ok := hands["b"]; !ok {
		t.Fatalf("玩家视角 hands 应含本人手牌")
	}
	// 观战者看不到任何手牌明细。
	if _, leaked := spectatorView["hands"]; leaked {
		t.Fatalf("观战视角不得暴露 hands")
	}
}

// TestDoudizhuRedealClearsRevealedBottomCards 无人叫分重新发牌时，翻明底牌必须清空。
func TestDoudizhuRedealClearsRevealedBottomCards(t *testing.T) {
	engine, state := newRevealSeededState(t)

	state = bid(t, engine, state, "a", 0)
	state = bid(t, engine, state, "b", 0)
	state = bid(t, engine, state, "c", 0)

	if landlord, _ := state["landlord"].(string); landlord != "" {
		t.Fatalf("无人叫分时 landlord 应为空，实际=%q", landlord)
	}
	if phase, _ := state["phase"].(string); phase != "bid" {
		t.Fatalf("重新发牌后应回到 bid 阶段，实际 phase=%v", phase)
	}
	if cards := engine.revealedBottomCards(state); len(cards) != 0 {
		t.Fatalf("重新发牌后 revealedBottomCards 应清空，实际 %d 张: %+v", len(cards), cards)
	}
	if internal := engine.bottomCards(state); len(internal) != 3 {
		t.Fatalf("重新发牌后内部 bottomCards 应为新发的 3 张，实际 %d", len(internal))
	}
}

// sameCardSet 按 (rank,suit) 判断两组牌是否相同（忽略顺序）。
func sameCardSet(a, b []card) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(c card) string {
		raw, _ := json.Marshal(c)
		return string(raw)
	}
	counts := make(map[string]int, len(a))
	for _, c := range a {
		counts[key(c)]++
	}
	for _, c := range b {
		counts[key(c)]--
		if counts[key(c)] < 0 {
			return false
		}
	}
	return true
}

// duplicateCard 返回第一个重复牌的可读标识，无重复返回空串。
func duplicateCard(cards []card) string {
	seen := make(map[string]bool, len(cards))
	for _, c := range cards {
		raw, _ := json.Marshal(c)
		if seen[string(raw)] {
			return string(raw)
		}
		seen[string(raw)] = true
	}
	return ""
}
