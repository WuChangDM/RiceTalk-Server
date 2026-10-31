package werewolf

import (
	"testing"
)

// task-109：狼人杀 PlayerState 的最小可见集（引擎层，直接断言 nightActions/roles）。
//
// 6 人局 assignRoles 固定：alice/bob=werewolf、dave=seer、其余 villager。
// 夜间行动结构：狼人各自的 userID→target + 汇总 werewolfTarget；预言家 seerTarget/seerResult。
func newTestState(t *testing.T) map[string]interface{} {
	t.Helper()
	ids := []string{"alice", "bob", "carol", "dave", "erin", "frank"}
	players := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		players = append(players, map[string]interface{}{"userId": id, "displayName": id})
	}
	state, err := New().Init(players)
	if err != nil {
		t.Fatalf("werewolf init failed: %v", err)
	}
	state["nightActions"] = map[string]interface{}{
		"alice":          "dave",
		"bob":            "dave",
		"werewolfTarget": "dave",
		"seerTarget":     "alice",
		"seerResult":     true,
	}
	return state
}

func TestPlayerStateVillagerSeesOnlySelf(t *testing.T) {
	state := newTestState(t)
	got := New().PlayerState(state, "carol")

	roles, _ := got["roles"].(map[string]string)
	if len(roles) != 1 || roles["carol"] != "villager" {
		t.Fatalf("平民视角 roles 应只有本人且为 villager，实际=%v", got["roles"])
	}
	if _, leaked := got["nightActions"]; leaked {
		t.Fatalf("平民不得看到任何夜间行动，实际=%v", got["nightActions"])
	}
}

func TestPlayerStateSeerSeesOwnCheckOnly(t *testing.T) {
	state := newTestState(t)
	got := New().PlayerState(state, "dave")

	roles, _ := got["roles"].(map[string]string)
	if len(roles) != 1 || roles["dave"] != "seer" {
		t.Fatalf("预言家视角 roles 应只有本人且为 seer，实际=%v", got["roles"])
	}
	na, _ := got["nightActions"].(map[string]interface{})
	if len(na) != 2 {
		t.Fatalf("预言家 nightActions 应只含 seerTarget/seerResult，实际=%v", got["nightActions"])
	}
	if na["seerTarget"] != "alice" || na["seerResult"] != true {
		t.Fatalf("预言家应看到自己的查验结果，实际=%v", na)
	}
	if _, leaked := na["werewolfTarget"]; leaked {
		t.Fatalf("预言家不得看到狼队刀口")
	}
}

func TestPlayerStateWerewolfSeesTeamAndKillTarget(t *testing.T) {
	state := newTestState(t)
	got := New().PlayerState(state, "alice")

	roles, _ := got["roles"].(map[string]string)
	if len(roles) != 2 || roles["alice"] != "werewolf" || roles["bob"] != "werewolf" {
		t.Fatalf("狼人视角应看到自己与狼队友，实际=%v", got["roles"])
	}
	na, _ := got["nightActions"].(map[string]interface{})
	if len(na) != 1 || na["werewolfTarget"] != "dave" {
		t.Fatalf("狼人应只看到狼队刀口，实际=%v", got["nightActions"])
	}
	if _, leaked := na["seerResult"]; leaked {
		t.Fatalf("狼人不得看到预言家查验结果")
	}
}

func TestSpectatorStateStillStripsEverything(t *testing.T) {
	state := newTestState(t)
	got := New().SpectatorState(state)
	if _, ok := got["roles"]; ok {
		t.Fatalf("观战视角不得含 roles")
	}
	if _, ok := got["nightActions"]; ok {
		t.Fatalf("观战视角不得含 nightActions")
	}
}
