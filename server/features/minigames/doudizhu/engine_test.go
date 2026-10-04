package doudizhu

import (
	"encoding/json"
	"testing"

	"ridgericetalk/features/minigames/internal/util"
)

func TestEngineDealAndBid(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	phase, _ := state["phase"].(string)
	if phase != "bid" {
		t.Fatalf("expected bid phase, got %v", phase)
	}
	hands := state["hands"].(map[string][]card)
	if len(hands["a"]) != 17 {
		t.Fatalf("expected 17 cards for each player, got %d", len(hands["a"]))
	}
	if bottom := state["bottomCards"].([]card); len(bottom) != 3 {
		t.Fatalf("expected 3 bottom cards, got %d", len(bottom))
	}

	payload, _ := json.Marshal(map[string]interface{}{"score": 3})
	state, err := engine.ValidateMove(state, "a", "bid", payload)
	if err != nil {
		t.Fatalf("bid failed: %v", err)
	}
	if state["landlord"] != "a" {
		t.Fatalf("expected landlord=a, got %v", state["landlord"])
	}
	// Complete bidding so play starts.
	payload, _ = json.Marshal(map[string]interface{}{"score": 0})
	state, err = engine.ValidateMove(state, "b", "bid", payload)
	if err != nil {
		t.Fatalf("bid by b failed: %v", err)
	}
	state, err = engine.ValidateMove(state, "c", "bid", payload)
	if err != nil {
		t.Fatalf("bid by c failed: %v", err)
	}
	if state["phase"] != "play" {
		t.Fatalf("expected play phase after bidding, got %v", state["phase"])
	}
	hands = state["hands"].(map[string][]card)
	if len(hands["a"]) != 20 {
		t.Fatalf("expected landlord to have 20 cards after receiving bottom cards, got %d", len(hands["a"]))
	}
}

func TestEnginePlayBeats(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	state["phase"] = "play"
	state["currentPlayer"] = "a"
	state["lastPlayOwner"] = "b"
	state["lastPlay"] = []card{{Rank: 3, Value: "3"}}
	hands := state["hands"].(map[string][]card)
	// Play a card that a actually holds and beats the last play.
	play := []card{hands["a"][0]}
	if play[0].Rank <= 3 {
		// Ensure we beat rank 3.
		for _, c := range hands["a"] {
			if c.Rank > 3 {
				play[0] = c
				break
			}
		}
	}
	payload, _ := json.Marshal(map[string]interface{}{"cards": play})
	state, err := engine.ValidateMove(state, "a", "play", payload)
	if err != nil {
		t.Fatalf("play failed: %v", err)
	}
	if state["lastPlayOwner"] != "a" {
		t.Fatalf("expected lastPlayOwner=a, got %v", state["lastPlayOwner"])
	}
}

func TestEnginePass(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	state["phase"] = "play"
	state["currentPlayer"] = "a"
	state["lastPlayOwner"] = "b"
	state["lastPlay"] = []card{{Rank: 3, Value: "3"}}

	payload, _ := json.Marshal(map[string]interface{}{"cards": []card{}})
	state, err := engine.ValidateMove(state, "a", "play", payload)
	if err != nil {
		t.Fatalf("pass failed: %v", err)
	}
	if state["currentPlayer"] != "b" {
		t.Fatalf("expected currentPlayer=b after pass, got %v", state["currentPlayer"])
	}
	if owner := state["lastPlayOwner"]; owner != "b" {
		t.Fatalf("expected lastPlayOwner unchanged, got %v", owner)
	}
}

func TestEnginePassNotAllowedWhenLeading(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	state["phase"] = "play"
	state["currentPlayer"] = "a"
	state["lastPlayOwner"] = ""

	payload, _ := json.Marshal(map[string]interface{}{"cards": []card{}})
	_, err := engine.ValidateMove(state, "a", "play", payload)
	if err != util.ErrInvalidAction {
		t.Fatalf("expected ErrInvalidAction when passing without active play, got %v", err)
	}
}

func TestEngineBombBeatsChain(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	state["phase"] = "play"
	state["currentPlayer"] = "a"
	state["lastPlayOwner"] = "b"
	state["lastPlay"] = []card{
		{Rank: 3}, {Rank: 4}, {Rank: 5}, {Rank: 6}, {Rank: 7},
	}
	hands := state["hands"].(map[string][]card)
	// Replace a's hand with a bomb so the play is valid.
	hands["a"] = []card{{Rank: 9}, {Rank: 9}, {Rank: 9}, {Rank: 9}}

	payload, _ := json.Marshal(map[string]interface{}{"cards": hands["a"]})
	state, err := engine.ValidateMove(state, "a", "play", payload)
	if err != nil {
		t.Fatalf("bomb play failed: %v", err)
	}
	if state["lastPlayOwner"] != "a" {
		t.Fatalf("expected lastPlayOwner=a, got %v", state["lastPlayOwner"])
	}
}
