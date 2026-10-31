package minigames

import (
	"encoding/json"
	"strings"
	"testing"

	"ridgericetalk/features/minigames/doudizhu"
	"ridgericetalk/features/minigames/werewolf"
	"ridgericetalk/internal/model"

	"gorm.io/gorm"
)

// task-109（RUBRIC 红线 #1）：斗地主与狼人杀的玩家视角必须脱敏。
//
// 修复前：handler.go GetState 只在引擎实现 SpectatorStateProvider 且「非玩家」时脱敏，
// 玩家分支直返 session.State → 斗地主玩家能读到三家手牌（state.hands）、狼人杀玩家能
// 读到全部身份（state.roles）与夜间行动（state.nightActions），一条 curl 即可作弊。
// 修复后：两家引擎都实现 PlayerStateProvider，玩家视角只给最小可见集。
//
// 这些用例在 handler 层跑（真 SQLite + 真引擎 + 真路由），断言的是**下发给客户端的
// JSON 字符串解出来的结构**，而不是引擎内部 map —— 只有这样才能证明脱敏真的生效。

func seedDoudizhuSession(t *testing.T, db *gorm.DB, sessionID string) {
	t.Helper()
	ids := []string{"alice", "bob", "carol"}
	players := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		players = append(players, map[string]interface{}{"userId": id, "displayName": strings.ToUpper(id[:1]) + id[1:]})
		if err := db.Create(&model.Membership{ID: "ddz-m-" + id, UserID: id, SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
			t.Fatalf("seed membership %s: %v", id, err)
		}
	}
	playersJSON, _ := json.Marshal(players)

	state, err := doudizhu.New().Init(players)
	if err != nil {
		t.Fatalf("doudizhu engine init failed: %v", err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal doudizhu state failed: %v", err)
	}
	session := model.MinigameSession{
		ID:             sessionID,
		GameType:       "doudizhu",
		SpaceID:        "space-1",
		HostID:         "alice",
		State:          string(stateJSON),
		Status:         "playing",
		PlayersJSON:    string(playersJSON),
		SpectatorsJSON: `[{"userId":"spectator-ddz"}]`,
		IsActive:       true,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed doudizhu session: %v", err)
	}
	if err := db.Create(&model.Membership{ID: "ddz-m-spec", UserID: "spectator-ddz", SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("seed doudizhu spectator membership: %v", err)
	}
}

func seedWerewolfSession(t *testing.T, db *gorm.DB, sessionID string) {
	t.Helper()
	ids := []string{"alice", "bob", "carol", "dave", "erin", "frank"}
	players := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		players = append(players, map[string]interface{}{"userId": id, "displayName": strings.ToUpper(id[:1]) + id[1:]})
		if err := db.Create(&model.Membership{ID: "ww-m-" + id, UserID: id, SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
			t.Fatalf("seed membership %s: %v", id, err)
		}
	}
	playersJSON, _ := json.Marshal(players)

	state, err := werewolf.New().Init(players)
	if err != nil {
		t.Fatalf("werewolf engine init failed: %v", err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal werewolf state failed: %v", err)
	}
	session := model.MinigameSession{
		ID:             sessionID,
		GameType:       "werewolf",
		SpaceID:        "space-1",
		HostID:         "alice",
		State:          string(stateJSON),
		Status:         "playing",
		PlayersJSON:    string(playersJSON),
		SpectatorsJSON: `[{"userId":"spectator-ww"}]`,
		IsActive:       true,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed werewolf session: %v", err)
	}
	if err := db.Create(&model.Membership{ID: "ww-m-spec", UserID: "spectator-ww", SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("seed werewolf spectator membership: %v", err)
	}
}

func decodeStateMap(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode state failed: %v raw=%s", err, truncateForLog(raw))
	}
	return m
}

// TestDoudizhuPlayerStateHidesOtherHands 斗地主玩家只应看到自己的手牌明细。
func TestDoudizhuPlayerStateHidesOtherHands(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedDoudizhuSession(t, db, "s109-ddz-alice")

	raw := getStateRaw(t, r, "s109-ddz-alice", "alice")
	state := decodeStateMap(t, raw)

	hands, _ := state["hands"].(map[string]interface{})
	if len(hands) != 1 {
		t.Fatalf("玩家视角 hands 应只含本人 1 项，实际 %d 项: %s", len(hands), truncateForLog(raw))
	}
	if _, ok := hands["alice"]; !ok {
		t.Fatalf("玩家视角 hands 应含本人手牌，实际=%s", truncateForLog(raw))
	}
	if _, leaked := hands["bob"]; leaked {
		t.Fatalf("玩家视角不得暴露 bob 的手牌明细")
	}
	if _, leaked := hands["carol"]; leaked {
		t.Fatalf("玩家视角不得暴露 carol 的手牌明细")
	}
	// 本人手牌张数正确（叫地主前 17 张）。
	if own, ok := hands["alice"].([]interface{}); !ok || len(own) != 17 {
		t.Fatalf("本人手牌应为 17 张，实际=%v", hands["alice"])
	}
	// 对手只给张数，客户端 DoudizhuGame.tsx 用它算剩余牌。
	counts, _ := state["handCounts"].(map[string]interface{})
	if len(counts) != 3 {
		t.Fatalf("handCounts 应含 3 名玩家，实际=%v", counts)
	}
	for _, uid := range []string{"alice", "bob", "carol"} {
		if v, ok := counts[uid].(float64); !ok || int(v) != 17 {
			t.Fatalf("handCounts[%s] 应为 17，实际=%v", uid, counts[uid])
		}
	}
	// 公共字段必须保留，否则客户端无法渲染局面。
	for _, key := range []string{"phase", "currentPlayer", "players", "landlord", "bottomCards", "lastPlay", "pendingScore", "universals"} {
		if _, ok := state[key]; !ok {
			t.Fatalf("玩家视角缺少公共字段 %q", key)
		}
	}
}

// TestDoudizhuSpectatorAndOutsiderGetSpectatorView 观战者/非玩家仍是脱敏观战视角。
func TestDoudizhuSpectatorAndOutsiderGetSpectatorView(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedDoudizhuSession(t, db, "s109-ddz-view")
	if err := db.Create(&model.Membership{ID: "ddz-m-out", UserID: "outsider-ddz", SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("seed outsider membership: %v", err)
	}

	for _, user := range []string{"spectator-ddz", "outsider-ddz"} {
		raw := getStateRaw(t, r, "s109-ddz-view", user)
		if strings.Contains(raw, `"hands"`) {
			t.Fatalf("%s 不应拿到 hands 明细: %s", user, truncateForLog(raw))
		}
		state := decodeStateMap(t, raw)
		counts, ok := state["handCounts"].(map[string]interface{})
		if !ok || len(counts) != 3 {
			t.Fatalf("%s 应拿到 handCounts 公开张数，实际=%s", user, truncateForLog(raw))
		}
		// 观战视角仍可看到公共局面字段（斗地主无独立的 spectator 标记对象）。
		for _, key := range []string{"phase", "currentPlayer", "players", "bottomCards"} {
			if _, exists := state[key]; !exists {
				t.Fatalf("%s 观战视角缺少公共字段 %q", user, key)
			}
		}
	}
}

// TestWerewolfPlayerStateHidesOtherRoles 狼人杀玩家不得读到他人身份与夜间行动细节。
func TestWerewolfPlayerStateHidesOtherRoles(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedWerewolfSession(t, db, "s109-ww")

	// 6 人局固定分角色（assignRoles）：alice/bob=狼人、dave=预言家、其余平民。
	cases := []struct {
		user      string
		wantRoles map[string]string
	}{
		{"carol", map[string]string{"carol": "villager"}},
		{"dave", map[string]string{"dave": "seer"}},
		{"alice", map[string]string{"alice": "werewolf", "bob": "werewolf"}},
		{"bob", map[string]string{"bob": "werewolf", "alice": "werewolf"}},
	}
	for _, tc := range cases {
		raw := getStateRaw(t, r, "s109-ww", tc.user)
		state := decodeStateMap(t, raw)
		roles, _ := state["roles"].(map[string]interface{})
		if len(roles) != len(tc.wantRoles) {
			t.Fatalf("%s 视角 roles 应只有 %d 项，实际 %d 项: %s", tc.user, len(tc.wantRoles), len(roles), truncateForLog(raw))
		}
		for uid, want := range tc.wantRoles {
			if got, _ := roles[uid].(string); got != want {
				t.Fatalf("%s 视角 roles[%s] 应为 %q，实际 %q", tc.user, uid, want, got)
			}
		}
		// 未开局夜行动为空时字段可直接不存在；存在也只能是自己可见的键。
		if na, ok := state["nightActions"].(map[string]interface{}); ok {
			for key := range na {
				allowed := map[string]bool{"seerTarget": true, "seerResult": true, "werewolfTarget": true}
				if !allowed[key] {
					t.Fatalf("%s 视角 nightActions 出现不允许的键 %q", tc.user, key)
				}
			}
		}
		// 公共字段保留。
		for _, key := range []string{"phase", "day", "alive", "votes", "currentPlayer"} {
			if _, ok := state[key]; !ok {
				t.Fatalf("%s 视角缺少公共字段 %q", tc.user, key)
			}
		}
	}
}

// TestWerewolfOutsiderCannotReadRoles 非玩家（空间成员）走观战视角，读不到任何身份。
func TestWerewolfOutsiderCannotReadRoles(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedWerewolfSession(t, db, "s109-ww-view")
	if err := db.Create(&model.Membership{ID: "ww-m-out", UserID: "outsider-ww", SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("seed outsider membership: %v", err)
	}

	for _, user := range []string{"spectator-ww", "outsider-ww"} {
		raw := getStateRaw(t, r, "s109-ww-view", user)
		for _, banned := range []string{`"roles"`, `"nightActions"`} {
			if strings.Contains(raw, banned) {
				t.Fatalf("%s 不应读到 %s: %s", user, banned, truncateForLog(raw))
			}
		}
		if !strings.Contains(raw, `"alive"`) {
			t.Fatalf("%s 应仍能看到存活名单等公开信息", user)
		}
	}
}
