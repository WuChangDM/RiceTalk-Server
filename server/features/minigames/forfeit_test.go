package minigames

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"ridgericetalk/internal/model"

	"gorm.io/gorm"
)

// seedPresenceSession 播种一局进行中的 1v1 井字棋会话，并写入指定心跳时间。
func seedPresenceSession(t *testing.T, db *gorm.DB, id string, aliceLast, bobLast time.Time) {
	t.Helper()
	now := time.Now().UTC()
	session := model.MinigameSession{
		ID:             id,
		GameType:       "tictactoe",
		SpaceID:        "space-1",
		HostID:         "alice",
		Status:         "playing",
		IsActive:       true,
		PlayersJSON:    `[{"userId":"alice"},{"userId":"bob"}]`,
		SpectatorsJSON: "[]",
		State: marshalState(map[string]interface{}{
			"players":       []string{"alice", "bob"},
			"currentPlayer": "alice",
			"board":         make([]interface{}, 9),
			"presence": map[string]interface{}{
				"alice": aliceLast.UnixMilli(),
				"bob":   bobLast.UnixMilli(),
			},
		}),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session failed: %v", err)
	}
}

// TestLeaveForfeits1v1Immediately D3：1v1 对局中一方显式离开 → 对手立即判胜，
// 写历史 + 权威计分（胜场 +1），不再「无胜负结束」或「空等 2h 回收」。
func TestLeaveForfeits1v1Immediately(t *testing.T) {
	_, r, db := newRuleFixesRouter(t)

	now := time.Now().UTC()
	session := model.MinigameSession{
		ID:             "s-d3-leave",
		GameType:       "tictactoe",
		SpaceID:        "space-1",
		HostID:         "alice",
		Status:         "playing",
		IsActive:       true,
		PlayersJSON:    `[{"userId":"alice"},{"userId":"bob"}]`,
		SpectatorsJSON: "[]",
		State: marshalState(map[string]interface{}{
			"players":       []string{"alice", "bob"},
			"currentPlayer": "alice",
			"board":         make([]interface{}, 9),
		}),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session failed: %v", err)
	}

	w := postJSONAs(r, "/api/v1/minigames/leave", "alice", map[string]interface{}{"sessionId": session.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("leave 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if winner, _ := resp.Data["winnerId"].(string); winner != "bob" {
		t.Fatalf("离开者判负，胜者应为 bob，实际 %v（响应 %s）", resp.Data["winnerId"], w.Body.String())
	}

	var got model.MinigameSession
	if err := db.First(&got, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("reload session failed: %v", err)
	}
	if got.Status != "ended" {
		t.Fatalf("会话应结束，实际 status=%q", got.Status)
	}
	if winner, _ := parseState(got.State)["winner"].(string); winner != "bob" {
		t.Fatalf("state.winner 应为 bob，实际 %q", winner)
	}

	var hist int64
	db.Model(&model.MinigameHistory{}).Where("session_id = ?", session.ID).Count(&hist)
	if hist != 1 {
		t.Fatalf("应写 1 条历史，实际 %d", hist)
	}
	var row model.MinigameLeaderboard
	if err := db.Where("game_type = ? AND user_id = ?", "tictactoe", "bob").First(&row).Error; err != nil {
		t.Fatalf("胜者应获得权威计分: %v", err)
	}
	if row.BestScore != 1 {
		t.Fatalf("胜场应为 1，实际 %d", row.BestScore)
	}
}

// TestDisconnectTimeoutForfeits D3：超过 60s 宽限未心跳 → 判负、写历史、计分。
func TestDisconnectTimeoutForfeits(t *testing.T) {
	h, _, db := newRuleFixesRouter(t)

	now := time.Now().UTC()
	seedPresenceSession(t, db, "s-d3-timeout", now.Add(-70*time.Second), now.Add(-2*time.Second))

	h.tickTimedGames()

	var got model.MinigameSession
	if err := db.First(&got, "id = ?", "s-d3-timeout").Error; err != nil {
		t.Fatalf("reload session failed: %v", err)
	}
	if got.Status != "ended" {
		t.Fatalf("断线超时应结束对局，实际 status=%q（state=%s）", got.Status, got.State)
	}
	state := parseState(got.State)
	if winner, _ := state["winner"].(string); winner != "bob" {
		t.Fatalf("在线一方 bob 应判胜，实际 winner=%q", winner)
	}
	if reason, _ := state["endReason"].(string); reason != "disconnect_timeout" {
		t.Fatalf("endReason 应为 disconnect_timeout，实际 %q", reason)
	}
	if loser, _ := state["forfeitBy"].(string); loser != "alice" {
		t.Fatalf("forfeitBy 应为 alice，实际 %q", loser)
	}
	var hist int64
	db.Model(&model.MinigameHistory{}).Where("session_id = ?", "s-d3-timeout").Count(&hist)
	if hist != 1 {
		t.Fatalf("应写 1 条历史，实际 %d", hist)
	}
	var row model.MinigameLeaderboard
	if err := db.Where("game_type = ? AND user_id = ?", "tictactoe", "bob").First(&row).Error; err != nil {
		t.Fatalf("胜者应获得权威计分: %v", err)
	}
	if row.BestScore != 1 {
		t.Fatalf("胜场应为 1，实际 %d", row.BestScore)
	}
}

// TestDisconnectGraceReconnectNoForfeit D3：60s 宽限内（30s 未心跳）不判负。
func TestDisconnectGraceReconnectNoForfeit(t *testing.T) {
	h, _, db := newRuleFixesRouter(t)

	now := time.Now().UTC()
	seedPresenceSession(t, db, "s-d3-grace", now.Add(-30*time.Second), now.Add(-2*time.Second))

	h.tickTimedGames()

	var got model.MinigameSession
	if err := db.First(&got, "id = ?", "s-d3-grace").Error; err != nil {
		t.Fatalf("reload session failed: %v", err)
	}
	if got.Status != "playing" {
		t.Fatalf("宽限期内不应判负，实际 status=%q", got.Status)
	}
	var hist int64
	db.Model(&model.MinigameHistory{}).Where("session_id = ?", "s-d3-grace").Count(&hist)
	if hist != 0 {
		t.Fatalf("宽限期内不应写历史，实际 %d", hist)
	}
}

// TestMultiplayerLeaveUnaffected D3：1v1 之外的多人房保持既有行为（不判负、不写胜负历史）。
func TestMultiplayerLeaveUnaffected(t *testing.T) {
	_, r, db := newRuleFixesRouter(t)

	now := time.Now().UTC()
	session := model.MinigameSession{
		ID:             "s-d3-multi",
		GameType:       "doudizhu",
		SpaceID:        "space-1",
		HostID:         "alice",
		Status:         "playing",
		IsActive:       true,
		PlayersJSON:    `[{"userId":"alice"},{"userId":"bob"},{"userId":"carol"}]`,
		SpectatorsJSON: "[]",
		State:          marshalState(map[string]interface{}{"currentPlayer": "alice"}),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session failed: %v", err)
	}

	w := postJSONAs(r, "/api/v1/minigames/leave", "alice", map[string]interface{}{"sessionId": session.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("leave 应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var got model.MinigameSession
	if err := db.First(&got, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("reload session failed: %v", err)
	}
	if winner, _ := parseState(got.State)["winner"].(string); winner != "" {
		t.Fatalf("多人房离开不应产生判负胜者，实际 winner=%q", winner)
	}
	var hist int64
	db.Model(&model.MinigameHistory{}).Where("session_id = ?", session.ID).Count(&hist)
	if hist != 0 {
		t.Fatalf("多人房离开不应写胜负历史，实际 %d", hist)
	}
}
