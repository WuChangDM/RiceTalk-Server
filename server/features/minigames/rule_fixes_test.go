package minigames

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newRuleFixesRouter 构造一个可用 X-Test-User 指定调用者的路由，
// 便于在同一进程内模拟多个玩家（Leave/Action 的并发与越权路径）。
func newRuleFixesRouter(t *testing.T) (*Handler, *gin.Engine, *gorm.DB) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	h := NewHandler(db, &config.Config{
		JWTSecret:     "test-jwt-secret-for-minigames-32bytes!!",
		EncryptionKey: "test-encryption-key-for-minigames-32bytes!",
	}, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1/minigames")
	auth := func(c *gin.Context) {
		user := c.GetHeader("X-Test-User")
		if user == "" {
			user = "alice"
		}
		c.Set("user_id", user)
		c.Set("username", user)
		c.Set("role", "MEMBER")
		c.Set("space_id", "space-1")
		c.Next()
	}
	g.POST("/leave", auth, h.Leave)
	g.GET("/leaderboard", auth, h.GetLeaderboard)
	return h, r, db
}

func postJSONAs(r *gin.Engine, path, user string, body interface{}) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", user)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestLeaveAdvancesTurnForCurrentPlayer 回归：轮到的玩家中途离开后，
// 回合必须交给仍在座的玩家，否则 currentPlayer 指向已离开的人 → 整局卡死。
func TestLeaveAdvancesTurnForCurrentPlayer(t *testing.T) {
	_, r, db := newRuleFixesRouter(t)

	now := time.Now().UTC()
	session := model.MinigameSession{
		ID:             "s-leave-1",
		GameType:       "doudizhu",
		SpaceID:        "space-1",
		HostID:         "alice",
		Status:         "playing",
		IsActive:       true,
		PlayersJSON:    `[{"userId":"alice"},{"userId":"bob"},{"userId":"carol"}]`,
		SpectatorsJSON: "[]",
		State: marshalState(map[string]interface{}{
			"players":       []string{"alice", "bob", "carol"},
			"currentPlayer": "bob",
			"board":         make([]interface{}, 9),
		}),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session failed: %v", err)
	}

	w := postJSONAs(r, "/api/v1/minigames/leave", "bob", map[string]interface{}{"sessionId": session.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("leave 应返回 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var got model.MinigameSession
	if err := db.First(&got, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("reload session failed: %v", err)
	}
	state := parseState(got.State)
	if cur, _ := state["currentPlayer"].(string); cur != "alice" {
		t.Fatalf("离开者的回合应交给仍在座的 alice，实际 currentPlayer=%q", cur)
	}
	if players := parsePlayers(got.PlayersJSON); len(players) != 2 {
		t.Fatalf("离开后应剩 2 名玩家，实际 %d", len(players))
	}
	// 注：本用例用 3 人斗地主而非 1v1 棋类——1v1 一方离开已改为「判负结算」（D3，见 forfeit_test.go），
	// 回合交接逻辑只在多人房生效。
}

// TestPersistSessionStateRejectsStaleSnapshot 回归：以 state 原文为乐观锁，
// 基于同一份旧快照的第二次写入必须失败（避免并发动作互相覆盖/落子丢失）。
func TestPersistSessionStateRejectsStaleSnapshot(t *testing.T) {
	h, _, db := newRuleFixesRouter(t)

	now := time.Now().UTC()
	session := model.MinigameSession{
		ID:             "s-lock-1",
		GameType:       "tictactoe",
		SpaceID:        "space-1",
		HostID:         "alice",
		Status:         "playing",
		IsActive:       true,
		PlayersJSON:    `[{"userId":"alice"},{"userId":"bob"}]`,
		SpectatorsJSON: "[]",
		State:          `{}`,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session failed: %v", err)
	}

	// 两个请求读到同一份快照
	var first, second model.MinigameSession
	if err := db.First(&first, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("first read failed: %v", err)
	}
	if err := db.First(&second, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("second read failed: %v", err)
	}

	prevState := first.State
	first.State = `{"move":1}`
	if err := h.persistSessionState(&first, prevState); err != nil {
		t.Fatalf("首次写入应成功，实际错误: %v", err)
	}

	second.State = `{"move":2}`
	if err := h.persistSessionState(&second, prevState); err != errSessionStateConflict {
		t.Fatalf("陈旧快照的第二次写入应返回 errSessionStateConflict，实际: %v", err)
	}

	var reloaded model.MinigameSession
	db.First(&reloaded, "id = ?", session.ID)
	if reloaded.State != `{"move":1}` {
		t.Fatalf("冲突写入不应改动 state，实际 %q", reloaded.State)
	}
}

// TestLeaderboardDeduplicatesUsers 回归：同一用户存在多条历史记录时，
// 榜单只能出现一次（取最好成绩），否则并发提交会在榜上刷出重复用户。
func TestLeaderboardDeduplicatesUsers(t *testing.T) {
	_, r, db := newRuleFixesRouter(t)

	now := time.Now().UTC()
	rows := []model.MinigameLeaderboard{
		{ID: "lb-1", GameType: "2048", UserID: "user-1", Username: "alice", BestScore: 50, CreatedAt: now, UpdatedAt: now},
		{ID: "lb-2", GameType: "2048", UserID: "user-1", Username: "alice", BestScore: 90, CreatedAt: now, UpdatedAt: now},
		{ID: "lb-3", GameType: "2048", UserID: "user-2", Username: "bob", BestScore: 70, CreatedAt: now, UpdatedAt: now},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed leaderboard failed: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/minigames/leaderboard?gameType=2048", nil)
	req.Header.Set("X-Test-User", "alice")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("榜单应返回 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Entries []struct {
				UserID    string `json:"userId"`
				BestScore int64  `json:"bestScore"`
			} `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Data.Entries) != 2 {
		t.Fatalf("去重后应有 2 名用户，实际 %d: %s", len(resp.Data.Entries), w.Body.String())
	}
	best := map[string]int64{}
	for _, e := range resp.Data.Entries {
		best[e.UserID] = e.BestScore
	}
	if best["user-1"] != 90 {
		t.Fatalf("user-1 应保留最好成绩 90，实际 %d", best["user-1"])
	}
	if _, dup := best["user-1"]; !dup {
		t.Fatalf("user-1 缺失: %s", w.Body.String())
	}
}
