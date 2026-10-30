package minigames

import (
	"encoding/json"
	"strings"
	"testing"

	"ridgericetalk/internal/model"
)

// M14 补漏（task-87）：reviewer 复核发现「非玩家、非观战的空间成员」仍能读到 4 家手牌。
//
// 旧实现的判据是「是否在 SpectatorsJSON 里」，于是「只加入空间、既未入座也未观战」的
// 用户会落到玩家分支；日麻引擎在 seat<0 时提前 return 未遮蔽的 views →
// 任意空间成员一条 curl 就能拿到四家 closed_tiles/drawn/legal_actions。
// 现在判据改为「是否为本局玩家」，非玩家一律走观战视角。

// TestNonPlayerSpaceMemberCannotReadHands 核心回归：空间成员非玩家读不到任何手牌。
func TestNonPlayerSpaceMemberCannotReadHands(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedRiichiSession(t, db, "s87-outsider")
	// erin 只是空间成员：不在 PlayersJSON，也不在 SpectatorsJSON。
	if err := db.Create(&model.Membership{ID: "m-erin", UserID: "erin", SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("seed erin membership: %v", err)
	}

	raw := getStateRaw(t, r, "s87-outsider", "erin")
	for _, banned := range []string{`"closed_tiles"`, `"drawn"`, `"legal_actions"`, `"snapshot"`, `"seed"`, `"wall"`} {
		if strings.Contains(raw, banned) {
			t.Fatalf("空间成员（非玩家）不应读到 %s，实际=%s", banned, truncateForLog(raw))
		}
	}
	// 公开信息仍应有（观战视角：spectator 对象 + 牌河/点数等）。
	if !strings.Contains(raw, `"spectator"`) {
		t.Fatalf("非玩家应拿到观战视角（含 spectator 公开对象），实际=%s", truncateForLog(raw))
	}
	if !strings.Contains(raw, `"discards"`) && !strings.Contains(raw, `"remaining_draws"`) {
		t.Fatalf("观战视角应含牌河/剩余张数等公开信息，实际=%s", truncateForLog(raw))
	}
}

// TestRealSpectatorStateUnchanged 真观战者口径与 M14（task-82）后逐字一致。
func TestRealSpectatorStateUnchanged(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedRiichiSession(t, db, "s87-spectator")

	raw := getStateRaw(t, r, "s87-spectator", "charlie")
	if strings.Contains(raw, `"closed_tiles"`) || strings.Contains(raw, `"snapshot"`) {
		t.Fatalf("观战视角不应含手牌/snapshot，实际=%s", truncateForLog(raw))
	}
	var state map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	views, ok := state["views"].([]interface{})
	if !ok || len(views) != 1 {
		t.Fatalf("观战视角 views 应为单元素（SpectatorState 既有行为），实际=%v", state["views"])
	}
	if sp, _ := state["spectator"].(map[string]interface{}); sp == nil {
		t.Fatalf("观战视角应含 spectator 对象")
	}
}

// TestHostStillReadsOwnHand 房主作为玩家仍能读自己的手牌（行为零变化）。
func TestHostStillReadsOwnHand(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedRiichiSession(t, db, "s87-host")

	raw := getStateRaw(t, r, "s87-host", "alice") // alice 是 HostID 且是玩家
	if !strings.Contains(raw, `"closed_tiles"`) || !strings.Contains(raw, `"legal_actions"`) {
		t.Fatalf("房主（玩家）应能读自己手牌与合法动作，实际=%s", truncateForLog(raw))
	}
	if strings.Contains(raw, `"snapshot"`) || strings.Contains(raw, `"seed"`) {
		t.Fatalf("房主视角也不应含 snapshot/seed，实际=%s", truncateForLog(raw))
	}
}

// TestNonPlayerNonRiichiUnchanged 非日麻（无 SpectatorStateProvider）非玩家路径不变。
func TestNonPlayerNonRiichiUnchanged(t *testing.T) {
	_, r, db := newTask82Router(t)
	players := []map[string]interface{}{
		{"userId": "alice", "displayName": "Alice"},
		{"userId": "bob", "displayName": "Bob"},
	}
	playersJSON, _ := json.Marshal(players)
	stateJSON := `{"game":"tictactoe","status":"playing","players":["alice","bob"],"currentPlayer":"alice","winner":null,"board":[null,null,null,null,null,null,null,null,null]}`
	for _, id := range []string{"alice", "bob", "erin"} {
		if err := db.Create(&model.Membership{ID: "m-" + id, UserID: id, SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
			t.Fatalf("seed membership %s: %v", id, err)
		}
	}
	if err := db.Create(&model.MinigameSession{
		ID: "s87-ttt", GameType: "tictactoe", SpaceID: "space-1", HostID: "alice",
		State: stateJSON, Status: "playing", PlayersJSON: string(playersJSON), IsActive: true,
	}).Error; err != nil {
		t.Fatalf("seed tictactoe session: %v", err)
	}

	raw := getStateRaw(t, r, "s87-ttt", "erin")
	if !strings.Contains(raw, `"board"`) || !strings.Contains(raw, `"tictactoe"`) {
		t.Fatalf("非日麻游戏无 SpectatorStateProvider，非玩家应维持原样下发，实际=%s", truncateForLog(raw))
	}
}
