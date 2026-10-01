package minigames

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"

	"gorm.io/gorm"
)

// seedSession inserts a minigame session row directly (bypassing Join) so tests
// can set up host/player/status combinations the hardcoded test routes can't.
func seedSession(t *testing.T, db *gorm.DB, s *model.MinigameSession) {
	t.Helper()
	if s.ID == "" {
		s.ID = idgen.NextString()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	s.UpdatedAt = time.Now().UTC()
	if err := db.Create(s).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}
}

func playersJSON(userIDs ...string) string {
	players := make([]map[string]interface{}, 0, len(userIDs))
	for i, id := range userIDs {
		players = append(players, map[string]interface{}{
			"userId":   id,
			"username": id,
			"score":    0,
			"seat":     i,
		})
	}
	data, _ := json.Marshal(players)
	return string(data)
}

func doPost(t *testing.T, r http.Handler, path string, body map[string]interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func doGet(t *testing.T, r http.Handler, path string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

// TestGetGamesExposesCategoryAndLeaderboardMeta verifies the single/multi split
// and per-game leaderboard metadata required by 设计 §15.3.3 / §15.7.4.
func TestGetGamesExposesCategoryAndLeaderboardMeta(t *testing.T) {
	_, r, _ := setupTestHandler(t, nil)

	w, resp := doGet(t, r, "/api/v1/minigames/list")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	games, _ := resp["data"].([]interface{})
	if len(games) == 0 {
		t.Fatal("expected a non-empty game list")
	}

	byID := map[string]map[string]interface{}{}
	for _, g := range games {
		m, _ := g.(map[string]interface{})
		if id, _ := m["id"].(string); id != "" {
			byID[id] = m
		}
	}

	single := []string{"2048", "tictactoe-local", "chess-local"}
	for _, id := range single {
		g, ok := byID[id]
		if !ok {
			t.Fatalf("expected single-player game %q in list", id)
		}
		if g["category"] != "single" {
			t.Errorf("%s: expected category single, got %v", id, g["category"])
		}
		if g["localOnly"] != true {
			t.Errorf("%s: expected localOnly true", id)
		}
		if dim, _ := g["scoreDimension"].(string); dim == "" {
			t.Errorf("%s: expected a leaderboard dimension", id)
		}
		if order, _ := g["scoreOrder"].(string); order != "desc" && order != "asc" {
			t.Errorf("%s: expected scoreOrder desc|asc, got %v", id, g["scoreOrder"])
		}
	}

	multi := []string{"tictactoe", "chess", "gobang", "werewolf", "ludo", "doudizhu"}
	for _, id := range multi {
		g, ok := byID[id]
		if !ok {
			t.Fatalf("expected multiplayer game %q in list", id)
		}
		if g["category"] != "multi" {
			t.Errorf("%s: expected category multi, got %v", id, g["category"])
		}
		if dim, _ := g["scoreDimension"].(string); dim != "" {
			t.Errorf("%s: multiplayer games must have no leaderboard, got dimension %q", id, dim)
		}
	}

	// 国际象棋（单机）用时越短越好 → asc；2048 分数越高越好 → desc。
	if order, _ := byID["chess-local"]["scoreOrder"].(string); order != "asc" {
		t.Errorf("chess-local: expected scoreOrder asc, got %v", order)
	}
	if order, _ := byID["2048"]["scoreOrder"].(string); order != "desc" {
		t.Errorf("2048: expected scoreOrder desc, got %v", order)
	}

	// 飞行棋为 2-4 人（设计 §15.7.5.1）
	if min, _ := byID["ludo"]["minPlayers"].(float64); int(min) != 2 {
		t.Errorf("ludo: expected minPlayers 2, got %v", byID["ludo"]["minPlayers"])
	}
	if max, _ := byID["ludo"]["players"].(float64); int(max) != 4 {
		t.Errorf("ludo: expected players 4, got %v", byID["ludo"]["players"])
	}
}

// TestJoinRejectsSinglePlayerGame verifies 单机游戏 cannot create a room —
// previously this produced a permanently unstartable 1/1 room.
func TestJoinRejectsSinglePlayerGame(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	for _, gameType := range []string{"2048", "tictactoe-local", "chess-local"} {
		w, _ := doPost(t, r, "/api/v1/minigames/join", map[string]interface{}{"gameType": gameType})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400 (single-player games need no room), got %d, body: %s", gameType, w.Code, w.Body.String())
		}
	}

	// 多人游戏仍可建房
	w, _ := doPost(t, r, "/api/v1/minigames/join", map[string]interface{}{"gameType": "tictactoe"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 creating a multiplayer room, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestLeaderboardAscendingKeepsMinimum verifies "最短用时" semantics: for asc games
// the stored best is the minimum and the leaderboard is ordered ascending.
func TestLeaderboardAscendingKeepsMinimum(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	// 先提交 45 秒，再提交 30 秒 → 更优（更短），应更新为 30。
	if w, _ := doPost(t, r, "/api/v1/minigames/leaderboard", map[string]interface{}{"gameType": "chess-local", "score": 45}); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	w, resp := doPost(t, r, "/api/v1/minigames/leaderboard", map[string]interface{}{"gameType": "chess-local", "score": 30})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	data, _ := resp["data"].(map[string]interface{})
	if best, _ := data["bestScore"].(float64); int(best) != 30 {
		t.Fatalf("expected bestScore 30 (shorter time wins), got %v", data["bestScore"])
	}

	// 再提交 60 秒 → 更差，best 保持 30。
	w, resp = doPost(t, r, "/api/v1/minigames/leaderboard", map[string]interface{}{"gameType": "chess-local", "score": 60})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	data, _ = resp["data"].(map[string]interface{})
	if best, _ := data["bestScore"].(float64); int(best) != 30 {
		t.Fatalf("expected bestScore to remain 30, got %v", data["bestScore"])
	}

	// 排行榜响应带维度与方向，并按升序排列。
	w, resp = doGet(t, r, "/api/v1/minigames/leaderboard?gameType=chess-local")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ld, _ := resp["data"].(map[string]interface{})
	if ld["order"] != "asc" {
		t.Errorf("expected order asc, got %v", ld["order"])
	}
	if dim, _ := ld["dimension"].(string); dim == "" {
		t.Error("expected a leaderboard dimension in the response")
	}
	entries, _ := ld["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	// desc 方向仍取最大值（2048）。
	doPost(t, r, "/api/v1/minigames/leaderboard", map[string]interface{}{"gameType": "2048", "score": 128})
	_, resp = doPost(t, r, "/api/v1/minigames/leaderboard", map[string]interface{}{"gameType": "2048", "score": 64})
	data, _ = resp["data"].(map[string]interface{})
	if best, _ := data["bestScore"].(float64); int(best) != 128 {
		t.Fatalf("expected 2048 bestScore to remain 128, got %v", data["bestScore"])
	}
}

// TestLeaderboardRejectsMultiplayerGame verifies 多人联机游戏无排行榜（设计 §15.7.4）。
func TestLeaderboardRejectsMultiplayerGame(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	for _, gameType := range []string{"tictactoe", "gobang", "doudizhu", "werewolf"} {
		w, _ := doPost(t, r, "/api/v1/minigames/leaderboard", map[string]interface{}{"gameType": gameType, "score": 10})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400 (multiplayer games have no leaderboard), got %d", gameType, w.Code)
		}
	}
}

// TestGameRestartResetsState verifies the 设计 §15.3.4「再来一局」path:
// host can restart a playing/ended game, state is re-initialized.
func TestGameRestartResetsState(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	seedSession(t, db, &model.MinigameSession{
		GameType:       "tictactoe",
		SpaceID:        "space-1",
		HostID:         "user-1",
		Status:         "ended",
		State:          `{"board":["X","O","X","O","X","O","X","O","X"],"players":["user-1","user-2"],"currentPlayer":"user-1","winner":"user-1","actions":[]}`,
		PlayersJSON:    playersJSON("user-1", "user-2"),
		SpectatorsJSON: "[]",
		IsActive:       true,
	})

	var session model.MinigameSession
	if err := db.Where("game_type = ?", "tictactoe").First(&session).Error; err != nil {
		t.Fatalf("session not found: %v", err)
	}

	w, _ := doPost(t, r, "/api/v1/minigames/action", map[string]interface{}{
		"sessionId": session.ID,
		"action":    "game_restart",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for host game_restart, got %d, body: %s", w.Code, w.Body.String())
	}

	if err := db.Where("id = ?", session.ID).First(&session).Error; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if session.Status != "playing" {
		t.Errorf("expected status playing after restart, got %q", session.Status)
	}
	state := parseState(session.State)
	board, _ := state["board"].([]interface{})
	if len(board) != 9 {
		t.Fatalf("expected a fresh 9-cell board, got %v", state["board"])
	}
	for i, cell := range board {
		if cell != nil {
			t.Errorf("expected empty cell at %d after restart, got %v", i, cell)
		}
	}
	if actions, _ := state["actions"].([]interface{}); len(actions) != 1 {
		// 重置后的动作日志被清空，仅保留本次 game_restart 自身（与 game_start 行为一致）。
		t.Errorf("expected only the game_restart entry in the action log, got %d entries", len(actions))
	}
}

// TestGameRestartRejectedForNonHost verifies only the host may restart.
func TestGameRestartRejectedForNonHost(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	seedSession(t, db, &model.MinigameSession{
		GameType:       "tictactoe",
		SpaceID:        "space-1",
		HostID:         "user-2", // 房主不是 user-1
		Status:         "ended",
		State:          `{"board":["X","O","X","O","X","O","X","O","X"],"players":["user-1","user-2"],"currentPlayer":"user-1","winner":"user-1","actions":[]}`,
		PlayersJSON:    playersJSON("user-1", "user-2"),
		SpectatorsJSON: "[]",
		IsActive:       true,
	})

	var session model.MinigameSession
	if err := db.Where("game_type = ?", "tictactoe").First(&session).Error; err != nil {
		t.Fatalf("session not found: %v", err)
	}

	w, _ := doPost(t, r, "/api/v1/minigames/action", map[string]interface{}{
		"sessionId": session.ID,
		"action":    "game_restart",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-host restart, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestLudoStartsWithTwoPlayers verifies the 2-4 player contract: ludo may start
// at its lower bound (2) even though its upper bound is 4.
func TestLudoStartsWithTwoPlayers(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	seedSession(t, db, &model.MinigameSession{
		GameType:       "ludo",
		SpaceID:        "space-1",
		HostID:         "user-1",
		Status:         "waiting",
		State:          "{}",
		PlayersJSON:    playersJSON("user-1", "user-2"),
		SpectatorsJSON: "[]",
		IsActive:       true,
	})

	var session model.MinigameSession
	if err := db.Where("game_type = ?", "ludo").First(&session).Error; err != nil {
		t.Fatalf("session not found: %v", err)
	}

	w, _ := doPost(t, r, "/api/v1/minigames/action", map[string]interface{}{
		"sessionId": session.ID,
		"action":    "game_start",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 starting ludo with 2 players, got %d, body: %s", w.Code, w.Body.String())
	}
	if err := db.Where("id = ?", session.ID).First(&session).Error; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if session.Status != "playing" {
		t.Errorf("expected status playing, got %q", session.Status)
	}
}

// TestBroadcastStateOnlyForPublicInfoGames pins the rule behind carrying the
// authoritative state in action broadcasts: public-info games (no
// SpectatorStateProvider) may broadcast state, hidden-info games must not.
func TestBroadcastStateOnlyForPublicInfoGames(t *testing.T) {
	h := NewHandler(nil, nil, nil)

	for _, gameType := range []string{"tictactoe", "gobang", "chess"} {
		engine, ok := h.engines.Get(gameType)
		if !ok {
			t.Fatalf("no engine registered for %s", gameType)
		}
		if _, masked := engine.(SpectatorStateProvider); masked {
			t.Errorf("%s: public-info game must not implement SpectatorStateProvider", gameType)
		}
	}
	for _, gameType := range []string{"doudizhu", "werewolf"} {
		engine, ok := h.engines.Get(gameType)
		if !ok {
			t.Fatalf("no engine registered for %s", gameType)
		}
		if _, masked := engine.(SpectatorStateProvider); !masked {
			t.Errorf("%s: hidden-info game must implement SpectatorStateProvider", gameType)
		}
	}
}

// TestGetRoomsExcludesEndedRooms verifies finished games no longer pollute
// the active room list (previously they stayed listed forever).
func TestGetRoomsExcludesEndedRooms(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	seedSession(t, db, &model.MinigameSession{
		GameType: "tictactoe", SpaceID: "space-1", HostID: "user-1",
		Status: "waiting", State: "{}", PlayersJSON: playersJSON("user-1"), SpectatorsJSON: "[]", IsActive: true,
	})
	seedSession(t, db, &model.MinigameSession{
		GameType: "tictactoe", SpaceID: "space-1", HostID: "user-1",
		Status: "ended", State: "{}", PlayersJSON: playersJSON("user-1"), SpectatorsJSON: "[]", IsActive: true,
	})

	w, resp := doGet(t, r, "/api/v1/minigames/rooms")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	data, _ := resp["data"].(map[string]interface{})
	games, _ := data["games"].([]interface{})
	if len(games) != 1 {
		t.Fatalf("expected only the 1 non-ended room, got %d", len(games))
	}
	room, _ := games[0].(map[string]interface{})
	if room["status"] != "waiting" {
		t.Errorf("expected the listed room to be waiting, got %v", room["status"])
	}
}
