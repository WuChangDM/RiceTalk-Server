package minigames

import (
	"net/http"
	"testing"
	"time"

	"ridgericetalk/internal/model"
)

// TestDisconnectGraceBoundary（task-71）宽限边界：59s 不判负 / 61s 判负。
// task-65 D3 已覆盖 30s 与 70s，这里补边界值，防止常量被误改（例如改成 30s 或去掉判断）。
func TestDisconnectGraceBoundary(t *testing.T) {
	now := time.Now().UTC()

	t.Run("59s 未超宽限", func(t *testing.T) {
		h, _, db := newRuleFixesRouter(t)
		seedPresenceSession(t, db, "s-t71-59s", now.Add(-59*time.Second), now.Add(-2*time.Second))

		h.tickTimedGames()

		var got model.MinigameSession
		if err := db.First(&got, "id = ?", "s-t71-59s").Error; err != nil {
			t.Fatalf("reload session failed: %v", err)
		}
		if got.Status != "playing" {
			t.Fatalf("59s 未超 60s 宽限，不应判负，实际 status=%q", got.Status)
		}
		var hist int64
		db.Model(&model.MinigameHistory{}).Where("session_id = ?", "s-t71-59s").Count(&hist)
		if hist != 0 {
			t.Fatalf("宽限内不应写历史，实际 %d 条", hist)
		}
	})

	t.Run("61s 超宽限判负", func(t *testing.T) {
		h, _, db := newRuleFixesRouter(t)
		seedPresenceSession(t, db, "s-t71-61s", now.Add(-61*time.Second), now.Add(-2*time.Second))

		h.tickTimedGames()

		var got model.MinigameSession
		if err := db.First(&got, "id = ?", "s-t71-61s").Error; err != nil {
			t.Fatalf("reload session failed: %v", err)
		}
		if got.Status != "ended" {
			t.Fatalf("61s 已超宽限，应判负结束，实际 status=%q", got.Status)
		}
		if winner, _ := parseState(got.State)["winner"].(string); winner != "bob" {
			t.Fatalf("在线一方 bob 应胜，实际 winner=%q", winner)
		}
		var hist int64
		db.Model(&model.MinigameHistory{}).Where("session_id = ?", "s-t71-61s").Count(&hist)
		if hist != 1 {
			t.Fatalf("应写 1 条历史，实际 %d 条", hist)
		}
	})
}

// TestWaitingHostLeaveDissolvesRoom（task-71）waiting 阶段房主离开 → 房间解散（结束 + 失活），
// 且不产生胜负历史（比赛未开始，不该记战绩）。
func TestWaitingHostLeaveDissolvesRoom(t *testing.T) {
	_, r, db := newRuleFixesRouter(t)

	now := time.Now().UTC()
	session := model.MinigameSession{
		ID:             "s-t71-wait-host",
		GameType:       "tictactoe",
		SpaceID:        "space-1",
		HostID:         "alice",
		Status:         "waiting",
		IsActive:       true,
		PlayersJSON:    `[{"userId":"alice"},{"userId":"bob"}]`,
		SpectatorsJSON: "[]",
		State:          marshalState(map[string]interface{}{"players": []string{"alice", "bob"}}),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session failed: %v", err)
	}

	w := postJSONAs(r, "/api/v1/minigames/leave", "alice", map[string]interface{}{"sessionId": session.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("waiting 阶段房主离开应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var got model.MinigameSession
	if err := db.First(&got, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("reload session failed: %v", err)
	}
	if got.Status != "ended" {
		t.Fatalf("waiting 阶段房主离开应解散房间（status=ended），实际 %q", got.Status)
	}
	if got.IsActive {
		t.Fatal("房间解散后 IsActive 应为 false，实际仍为 true")
	}
	var hist int64
	db.Model(&model.MinigameHistory{}).Where("session_id = ?", session.ID).Count(&hist)
	if hist != 0 {
		t.Fatalf("未开始的比赛不应写历史，实际 %d 条", hist)
	}
}
