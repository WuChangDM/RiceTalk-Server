package werewolf

import (
	"encoding/json"
	"testing"
)

// 回归测试：模拟 handler 在两次狼人击杀之间向 state["actions"] 写入动作日志
// （[]interface{}），验证引擎的夜间行动字段（nightActions）不被覆盖，
// 狼全杀 + 预言家验齐活后仍能正确进入白天。
func TestTwoWolvesKillWithHandlerActionLog(t *testing.T) {
	e := New()
	players := []map[string]interface{}{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		players = append(players, map[string]interface{}{"userId": id})
	}
	state, _ := e.Init(players)
	roles := state["roles"].(map[string]string)
	wolves := []string{}
	seer := ""
	victim := ""
	for id, r := range roles {
		if r == "werewolf" {
			wolves = append(wolves, id)
		} else if r == "seer" {
			seer = id
		} else if victim == "" {
			victim = id
		}
	}

	// 模拟 handler 每次动作后向 state["actions"] 写入日志（覆盖引擎的旧 actions 字段）
	writeHandlerLog := func(s map[string]interface{}, uid string) map[string]interface{} {
		log, _ := s["actions"].([]interface{})
		log = append(log, map[string]interface{}{"userId": uid, "action": "werewolf_kill"})
		s["actions"] = log
		return s
	}

	// wolf1 kills
	p1, _ := json.Marshal(map[string]interface{}{"target": victim})
	s1, err := e.ValidateMove(state, wolves[0], "werewolf_kill", p1)
	if err != nil {
		t.Fatalf("wolf1 kill failed: %v", err)
	}
	s1 = writeHandlerLog(s1, wolves[0])
	if ph, _ := s1["phase"].(string); ph != "night" {
		t.Fatalf("after wolf1 should still be night, got %v", ph)
	}

	// wolf2 kills (same victim) — 仍夜晚（还需预言家验人）
	p2, _ := json.Marshal(map[string]interface{}{"target": victim})
	s2, err := e.ValidateMove(s1, wolves[1], "werewolf_kill", p2)
	if err != nil {
		t.Fatalf("wolf2 kill failed: %v", err)
	}
	s2 = writeHandlerLog(s2, wolves[1])
	if ph, _ := s2["phase"].(string); ph != "night" {
		t.Fatalf("after both wolves acted (seer 未验) should still be night, got %v", ph)
	}

	// seer checks — 齐活立即天亮
	pc, _ := json.Marshal(map[string]interface{}{"target": wolves[0]})
	s3, err := e.ValidateMove(s2, seer, "seer_check", pc)
	if err != nil {
		t.Fatalf("seer_check failed: %v", err)
	}
	if ph, _ := s3["phase"].(string); ph != "day" {
		t.Fatalf("after wolves killed + seer checked should be day, got %v (nightActions 被 handler 日志覆盖)", ph)
	}
	if len(e.aliveSlice(s3)) != 5 {
		t.Fatalf("after kill alive should be 5, got %d", len(e.aliveSlice(s3)))
	}
	t.Logf("handler 日志与引擎 nightActions 无冲突，狼全杀+预言家验齐活后正确进入 day/5")
}
