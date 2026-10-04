package doudizhu

import (
	"testing"
	"time"
)

func TestTickTimeoutAutoPass(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	state["phase"] = "play"
	state["currentPlayer"] = "b"
	state["lastPlayOwner"] = "a" // b 可 pass
	state["lastPlay"] = []card{{Rank: 3, Value: "3"}}
	// 设置超时
	state["turnStartedAt"] = time.Now().UTC().Unix() - PlayTimeout - 1

	newState, changed := engine.TickTimeout(state)
	if !changed {
		t.Fatal("expected TickTimeout to apply a timeout action")
	}
	if newState["currentPlayer"] != "c" {
		t.Fatalf("expected auto-pass advancing to c, got %v", newState["currentPlayer"])
	}
	// lastPlayOwner 不应被 pass 改变
	if newState["lastPlayOwner"] != "a" {
		t.Fatalf("expected lastPlayOwner unchanged after auto-pass, got %v", newState["lastPlayOwner"])
	}
}

func TestTickTimeoutNoTimeout(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	state["phase"] = "play"
	state["currentPlayer"] = "b"
	state["turnStartedAt"] = time.Now().UTC().Unix() // 未超时
	_, changed := engine.TickTimeout(state)
	if changed {
		t.Fatal("expected no timeout action when not timed out")
	}
}

func TestTickTimeoutAutoPlayWhenLeading(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	state["phase"] = "play"
	state["currentPlayer"] = "a"
	state["lastPlayOwner"] = "" // 首家
	state["lastPlay"] = nil
	state["turnStartedAt"] = time.Now().UTC().Unix() - PlayTimeout - 1

	newState, changed := engine.TickTimeout(state)
	if !changed {
		t.Fatal("expected TickTimeout to auto-play for leading player")
	}
	// 首家自动出最小牌后，lastPlayOwner 应为 a
	if newState["lastPlayOwner"] != "a" {
		t.Fatalf("expected lastPlayOwner=a after auto-play, got %v", newState["lastPlayOwner"])
	}
	hands := newState["hands"].(map[string][]card)
	if len(hands["a"]) != 16 {
		t.Fatalf("expected a to have 16 cards after auto-play, got %d", len(hands["a"]))
	}
}
