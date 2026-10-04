package werewolf

import (
	"encoding/json"
	"testing"
	"time"
)

func mkPlayers(ids ...string) []map[string]interface{} {
	out := []map[string]interface{}{}
	for _, id := range ids {
		out = append(out, map[string]interface{}{"userId": id})
	}
	return out
}

func rolesOf(state map[string]interface{}) map[string]string {
	if m, ok := state["roles"].(map[string]string); ok {
		return m
	}
	out := map[string]string{}
	if raw, ok := state["roles"].(map[string]interface{}); ok {
		for k, v := range raw {
			out[k], _ = v.(string)
		}
	}
	return out
}

// 夜晚齐活（狼全杀 + 预言家全验）立即天亮。
func TestNightCompleteAdvancesImmediately(t *testing.T) {
	e := New()
	state, _ := e.Init(mkPlayers("a", "b", "c", "d", "e", "f"))
	roles := rolesOf(state)
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
	kill := func(uid string) {
		p, _ := json.Marshal(map[string]interface{}{"target": victim})
		var err error
		state, err = e.ValidateMove(state, uid, "werewolf_kill", p)
		if err != nil {
			t.Fatalf("kill failed: %v", err)
		}
	}
	for _, w := range wolves {
		kill(w)
	}
	// 狼全杀但预言家未验：仍夜晚
	if ph, _ := state["phase"].(string); ph != "night" {
		t.Fatalf("wolves killed but seer not checked, expect night, got %v", ph)
	}
	// 预言家验人 → 立即天亮
	pc, _ := json.Marshal(map[string]interface{}{"target": wolves[0]})
	state, err := e.ValidateMove(state, seer, "seer_check", pc)
	if err != nil {
		t.Fatalf("seer_check failed: %v", err)
	}
	if ph, _ := state["phase"].(string); ph != "day" {
		t.Fatalf("night complete should advance to day, got %v", ph)
	}
	if len(e.aliveSlice(state)) != 5 {
		t.Fatalf("expected 5 alive after kill, got %d", len(e.aliveSlice(state)))
	}
}

// 系统法官：夜晚倒计时到期自动进入白天（空刀）。
func TestTickTimeoutNightToDay(t *testing.T) {
	e := New()
	state, _ := e.Init(mkPlayers("a", "b", "c", "d", "e", "f"))
	state["phaseStartedAt"] = time.Now().UTC().Unix() - NightDuration - 1
	newState, changed := e.TickTimeout(state)
	if !changed {
		t.Fatal("expected night timeout to advance")
	}
	if ph, _ := newState["phase"].(string); ph != "day" {
		t.Fatalf("night timeout should advance to day, got %v", ph)
	}
	if len(e.aliveSlice(newState)) != 6 {
		t.Fatalf("empty kill should keep 6 alive, got %d", len(e.aliveSlice(newState)))
	}
}

// 系统法官：白天倒计时到期自动进入投票。
func TestTickTimeoutDayToVote(t *testing.T) {
	e := New()
	state, _ := e.Init(mkPlayers("a", "b", "c", "d", "e", "f"))
	state["phase"] = "day"
	state["phaseStartedAt"] = time.Now().UTC().Unix() - DayDuration - 1
	newState, changed := e.TickTimeout(state)
	if !changed {
		t.Fatal("expected day timeout to advance")
	}
	if ph, _ := newState["phase"].(string); ph != "vote" {
		t.Fatalf("day timeout should advance to vote, got %v", ph)
	}
}

// 系统法官：投票倒计时到期自动计票（未投视为弃票）。
func TestTickTimeoutVoteResolve(t *testing.T) {
	e := New()
	state, _ := e.Init(mkPlayers("a", "b", "c", "d", "e", "f"))
	state["phase"] = "vote"
	state["phaseStartedAt"] = time.Now().UTC().Unix() - VoteDuration - 1
	newState, changed := e.TickTimeout(state)
	if !changed {
		t.Fatal("expected vote timeout to resolve")
	}
	if ph, _ := newState["phase"].(string); ph != "night" {
		t.Fatalf("vote timeout should return to night, got %v", ph)
	}
	if day, _ := newState["day"].(int); day != 2 {
		t.Fatalf("expected day to increment to 2, got %v", day)
	}
}

// 平票：平安日，无人放逐。
func TestVoteTieIsPeacefulDay(t *testing.T) {
	e := New()
	state, _ := e.Init(mkPlayers("a", "b", "c", "d"))
	state["phase"] = "vote"
	// a,b 互投 c,d 各一票 → 平票
	state["votes"] = map[string]string{"a": "c", "b": "d", "c": "c", "d": "d"}
	e.resolveVote(state)
	if len(e.aliveSlice(state)) != 4 {
		t.Fatalf("tie vote should eliminate nobody, alive=%d", len(e.aliveSlice(state)))
	}
	if le, _ := state["lastEliminated"].(string); le != "" {
		t.Fatalf("tie vote lastEliminated should be empty, got %v", le)
	}
	if ph, _ := state["phase"].(string); ph != "night" {
		t.Fatalf("after vote should be night, got %v", ph)
	}
}
