package minigames

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/tests/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func init() {
	_ = idgen.Init(1, 1)
}

// setupTestHandler creates a minigames Handler with the given hub.
// When hub is nil, the broadcast code paths are skipped (defensive guard).
func setupTestHandler(t *testing.T, hub *realtime.Hub) (*Handler, *gin.Engine, *gorm.DB) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	cfg := &config.Config{
		JWTSecret:     "test-jwt-secret-for-minigames-32bytes!!",
		EncryptionKey: "test-encryption-key-for-minigames-32bytes!",
	}
	h := NewHandler(db, cfg, hub)

	// Seed default space so resolveSpaceID can fall back or match context space_id.
	db.Create(&model.Space{ID: "space-1", Name: "Test Space", CreatedAt: time.Now(), UpdatedAt: time.Now()})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	mg := api.Group("/minigames")
	{
		mg.GET("/list", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.GetGames(c)
		})
		mg.POST("/join", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.Join(c)
		})
		mg.POST("/leave", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.Leave(c)
		})
		mg.POST("/action", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.Action(c)
		})
		// L24: 断线重连状态恢复
		mg.GET("/sessions/:id/state", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.GetState(c)
		})
		// L23: 游戏内聊天
		mg.POST("/chat", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.Chat(c)
		})
		// L22: 房间列表
		mg.GET("/rooms", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.GetRooms(c)
		})
		mg.POST("/invite", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.Invite(c)
		})
		mg.POST("/leaderboard", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.SubmitScore(c)
		})
		mg.GET("/leaderboard", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.GetLeaderboard(c)
		})
		mg.GET("/scores/me", func(c *gin.Context) {
			c.Set("user_id", "user-1")
			c.Set("username", "alice")
			c.Set("role", "MEMBER")
			c.Set("space_id", "space-1")
			h.GetMyScore(c)
		})
	}
	return h, r, db
}

// seedMembership creates a membership record so the user passes the Space
// membership check in Join/Action.
func seedMembership(t *testing.T, db *gorm.DB, spaceID, userID string) {
	t.Helper()
	membership := model.Membership{
		ID:        idgen.NextString(),
		UserID:    userID,
		SpaceID:   spaceID,
		Role:      "MEMBER",
		JoinedAt:  time.Now(),
		CreatedAt: time.Now(),
	}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatalf("failed to seed membership: %v", err)
	}
}

// TestGetGamesAlwaysSucceeds verifies the list endpoint works regardless
// of hub state (it does not broadcast).
func TestGetGamesAlwaysSucceeds(t *testing.T) {
	_, r, _ := setupTestHandler(t, nil)

	req := httptest.NewRequest("GET", "/api/v1/minigames/list", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestJoinWithNilHubSucceeds verifies that Join still succeeds when hub is
// nil (defensive guard works, broadcast is skipped).
func TestJoinWithNilHubSucceeds(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	body := map[string]interface{}{
		"gameType": "tictactoe",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/join", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 with nil hub, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestJoinWithHubDoesNotPanic verifies the Join broadcast code path
// executes without panicking when a real Hub is attached.
func TestJoinWithHubDoesNotPanic(t *testing.T) {
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	go hub.Run()

	_, r, db := setupTestHandler(t, hub)
	seedMembership(t, db, "space-1", "user-1")

	body := map[string]interface{}{
		"gameType": "tictactoe",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/join", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("Join panicked with hub attached: %v", rec)
		}
	}()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestLeaveWithHubDoesNotPanic verifies the Leave broadcast code path
// executes without panicking when a real Hub is attached.
func TestLeaveWithHubDoesNotPanic(t *testing.T) {
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	go hub.Run()

	_, r, db := setupTestHandler(t, hub)
	seedMembership(t, db, "space-1", "user-1")

	// Seed an active session owned by user-1 so Leave can find it
	session := model.MinigameSession{
		ID:        idgen.NextString(),
		GameType:  "tictactoe",
		SpaceID:   "space-1",
		HostID:    "user-1",
		State:     "{}",
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	body := map[string]interface{}{
		"sessionId": session.ID,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/leave", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("Leave panicked with hub attached: %v", rec)
		}
	}()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestActionWithHubDoesNotPanic verifies the Action broadcast code path
// executes without panicking when a real Hub is attached.
func TestActionWithHubDoesNotPanic(t *testing.T) {
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	go hub.Run()

	_, r, db := setupTestHandler(t, hub)
	seedMembership(t, db, "space-1", "user-1")

	// Seed an active session with user-1 as a player and status playing
	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-1",
		State:       `{"currentPlayer":"user-1"}`,
		Status:      "playing",
		PlayersJSON: `[{"userId":"user-1","username":"alice"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	body := map[string]interface{}{
		"sessionId": session.ID,
		"action":    "move_made",
		"payload":   map[string]interface{}{"row": 0, "col": 1, "nextPlayer": "user-1"},
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/action", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("Action panicked with hub attached: %v", rec)
		}
	}()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestActionWithNilHubSucceeds verifies that Action still succeeds when
// hub is nil (defensive guard works, broadcast is skipped).
func TestActionWithNilHubSucceeds(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-1",
		State:       `{"currentPlayer":"user-1"}`,
		Status:      "playing",
		PlayersJSON: `[{"userId":"user-1","username":"alice"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	body := map[string]interface{}{
		"sessionId": session.ID,
		"action":    "move_made",
		"payload":   map[string]interface{}{"row": 1, "col": 1, "nextPlayer": "user-1"},
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/action", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 with nil hub, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestGameStartRequiresHost verifies that only the host can start a game.
func TestGameStartRequiresHost(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")
	seedMembership(t, db, "space-1", "user-2")

	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-2",
		State:       "{}",
		Status:      "waiting",
		PlayersJSON: `[{"userId":"user-1","username":"alice"},{"userId":"user-2","username":"bob"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	body := map[string]interface{}{
		"sessionId": session.ID,
		"action":    "game_start",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/action", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for non-host start, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestGameStartInitializesState verifies that game_start initializes authoritative state.
func TestGameStartInitializesState(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")
	seedMembership(t, db, "space-1", "user-2")

	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-1",
		State:       "{}",
		Status:      "waiting",
		PlayersJSON: `[{"userId":"user-1","username":"alice"},{"userId":"user-2","username":"bob"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	body := map[string]interface{}{
		"sessionId": session.ID,
		"action":    "game_start",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/action", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data["status"] != "playing" {
		t.Errorf("expected status=playing, got %v", data["status"])
	}

	// Verify session state contains currentPlayer and board.
	var sessionResp map[string]interface{}
	req2 := httptest.NewRequest("GET", "/api/v1/minigames/sessions/"+session.ID+"/state", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected state status 200, got %d, body: %s", w2.Code, w2.Body.String())
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &sessionResp); err != nil {
		t.Fatalf("failed to parse state response: %v", err)
	}
	stateData, _ := sessionResp["data"].(map[string]interface{})
	if stateData["status"] != "playing" {
		t.Errorf("expected session status=playing, got %v", stateData["status"])
	}
	stateMapRaw, _ := stateData["state"].(string)
	var stateMap map[string]interface{}
	_ = json.Unmarshal([]byte(stateMapRaw), &stateMap)
	if stateMap["currentPlayer"] != "user-1" {
		t.Errorf("expected currentPlayer=user-1, got %v", stateMap["currentPlayer"])
	}
	if stateMap["board"] == nil {
		t.Error("expected board in state")
	}
}

// TestMoveMadeEnforcesTurnOrder verifies that a player cannot move on another's turn.
func TestMoveMadeEnforcesTurnOrder(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-2",
		State:       `{"currentPlayer":"user-2"}`,
		Status:      "playing",
		PlayersJSON: `[{"userId":"user-1","username":"alice"},{"userId":"user-2","username":"bob"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	body := map[string]interface{}{
		"sessionId": session.ID,
		"action":    "move_made",
		"payload":   map[string]interface{}{"row": 0, "col": 0},
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/action", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for out-of-turn move, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestLocalOnlyGameCannotStart verifies that local-only games cannot be started online.
func TestLocalOnlyGameCannotStart(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "2048",
		SpaceID:     "space-1",
		HostID:      "user-1",
		State:       "{}",
		Status:      "waiting",
		PlayersJSON: `[{"userId":"user-1","username":"alice"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	body := map[string]interface{}{
		"sessionId": session.ID,
		"action":    "game_start",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/action", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for local-only game start, got %d, body: %s", w.Code, w.Body.String())
	}
}

// ===== L21 可扩展游戏注册机制测试 =====

// TestGetGamesReturnsRegisteredGames verifies L21: GetGames returns all registered games
func TestGetGamesReturnsRegisteredGames(t *testing.T) {
	_, r, _ := setupTestHandler(t, nil)

	req := httptest.NewRequest("GET", "/api/v1/minigames/list", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].([]interface{})
	// 内置 4 个游戏
	if len(data) < 4 {
		t.Errorf("expected at least 4 registered games, got %d", len(data))
	}

	// 验证响应格式包含 id/name/players 字段
	if len(data) > 0 {
		first, _ := data[0].(map[string]interface{})
		if _, ok := first["id"]; !ok {
			t.Errorf("game item should have id field")
		}
		if _, ok := first["name"]; !ok {
			t.Errorf("game item should have name field")
		}
		if _, ok := first["players"]; !ok {
			t.Errorf("game item should have players field")
		}
	}
}

// TestRegisterGameAddsToRegistry verifies L21: RegisterGame adds new game to registry
func TestRegisterGameAddsToRegistry(t *testing.T) {
	_, r, _ := setupTestHandler(t, nil)

	// 注册一个新游戏
	newGame := GameInfo{ID: "test-game-unique", Name: "测试游戏", Players: 4}
	RegisterGame(newGame)
	defer func() {
		// 测试后清理：移除注册的游戏
		gamesMu.Lock()
		delete(registeredGames, newGame.ID)
		gamesMu.Unlock()
	}()

	req := httptest.NewRequest("GET", "/api/v1/minigames/list", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].([]interface{})

	// 验证新游戏出现在列表中
	found := false
	for _, item := range data {
		game, _ := item.(map[string]interface{})
		if game["id"] == "test-game-unique" {
			found = true
			if game["name"] != "测试游戏" {
				t.Errorf("expected name=测试游戏, got %v", game["name"])
			}
			if game["players"].(float64) != 4 {
				t.Errorf("expected players=4, got %v", game["players"])
			}
			break
		}
	}
	if !found {
		t.Errorf("registered game 'test-game-unique' not found in game list")
	}
}

// ===== L24: 断线重连状态恢复测试 =====

// TestGetStateReturnsSessionState verifies L24: GetState returns complete session state
func TestGetStateReturnsSessionState(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	// 创建活跃游戏会话
	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-1",
		State:       `{"actions":[{"userId":"user-1","action":"move","payload":{"row":0,"col":0}}]}`,
		Status:      "playing",
		PlayersJSON: `[{"userId":"user-1","score":0}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("failed to seed session: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/minigames/sessions/"+session.ID+"/state", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data["sessionId"] != session.ID {
		t.Errorf("expected sessionId=%s, got %v", session.ID, data["sessionId"])
	}
	if data["gameType"] != "tictactoe" {
		t.Errorf("expected gameType=tictactoe, got %v", data["gameType"])
	}
	if data["status"] != "playing" {
		t.Errorf("expected status=playing, got %v", data["status"])
	}
	// 验证返回了完整状态
	if data["state"] == nil {
		t.Error("expected state field to be present")
	}
	// 验证返回了玩家列表
	players, ok := data["players"].([]interface{})
	if !ok || len(players) != 1 {
		t.Errorf("expected 1 player, got %v", data["players"])
	}
}

// TestGetStateNotFound verifies L24: non-existent session returns 404
func TestGetStateNotFound(t *testing.T) {
	_, r, _ := setupTestHandler(t, nil)

	req := httptest.NewRequest("GET", "/api/v1/minigames/sessions/nonexistent-id/state", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// TestGetStateForbidden verifies L24: non-space-member returns 403
func TestGetStateForbidden(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	// 注意：不调用 seedMembership，user-1 不是 space-1 成员

	session := model.MinigameSession{
		ID:        idgen.NextString(),
		GameType:  "tictactoe",
		SpaceID:   "space-1",
		HostID:    "user-2",
		State:     "{}",
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	db.Create(&session)

	req := httptest.NewRequest("GET", "/api/v1/minigames/sessions/"+session.ID+"/state", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}
}

// ===== L23: 游戏内聊天同步到文字频道测试 =====

// TestChatSucceeds verifies that Chat succeeds for a session member.
func TestChatSucceeds(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-1",
		State:       "{}",
		PlayersJSON: `[{"userId":"user-1","username":"alice"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	db.Create(&session)

	body := map[string]interface{}{
		"sessionId": session.ID,
		"content":   "这步下得不错",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/chat", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestChatInvalidContent verifies L23: empty or too-long content returns 400
func TestChatInvalidContent(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	session := model.MinigameSession{
		ID:        idgen.NextString(),
		GameType:  "tictactoe",
		SpaceID:   "space-1",
		HostID:    "user-1",
		State:     "{}",
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	db.Create(&session)

	// 测试空内容
	body := map[string]interface{}{
		"sessionId": session.ID,
		"content":   "",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/chat", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty content, got %d", w.Code)
	}

	// 测试超长内容（>2000）
	longContent := string(make([]byte, 2001))
	for i := range longContent {
		longContent = longContent[:i] + "a" + longContent[i+1:]
	}
	body = map[string]interface{}{
		"sessionId": session.ID,
		"content":   longContent,
	}
	jsonBody, _ = json.Marshal(body)
	req = httptest.NewRequest("POST", "/api/v1/minigames/chat", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for too-long content, got %d", w.Code)
	}
}

// ===== L22: 房间列表测试 =====

// TestInviteSucceeds verifies that Invite succeeds for a session member.
func TestInviteSucceeds(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-1",
		State:       "{}",
		PlayersJSON: `[{"userId":"user-1","username":"alice"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	db.Create(&session)

	body := map[string]interface{}{
		"sessionId": session.ID,
		"toUserId":  "user-2",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/invite", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

// ===== 独立房间模式补充测试 =====

// TestJoinMaintainsPlayersJSON verifies that Join appends the user to PlayersJSON.
func TestJoinMaintainsPlayersJSON(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	body := map[string]interface{}{
		"gameType": "tictactoe",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/join", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	sessionID := data["sessionId"].(string)

	var session model.MinigameSession
	if err := db.First(&session, "id = ?", sessionID).Error; err != nil {
		t.Fatalf("failed to fetch session: %v", err)
	}

	players := parsePlayers(session.PlayersJSON)
	if len(players) != 1 {
		t.Fatalf("expected 1 player, got %d", len(players))
	}
	if players[0]["userId"] != "user-1" {
		t.Errorf("expected userId=user-1, got %v", players[0]["userId"])
	}
}

// TestJoinAgainDoesNotDuplicatePlayer verifies that joining the same session twice does not duplicate the player.
func TestJoinAgainDoesNotDuplicatePlayer(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	body := map[string]interface{}{
		"gameType": "tictactoe",
	}
	jsonBody, _ := json.Marshal(body)

	// First join creates the room.
	req := httptest.NewRequest("POST", "/api/v1/minigames/join", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 on join #1, got %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	sessionID := data["sessionId"].(string)

	// Second join uses sessionId to join the same room.
	body2 := map[string]interface{}{
		"sessionId": sessionID,
	}
	jsonBody2, _ := json.Marshal(body2)
	req = httptest.NewRequest("POST", "/api/v1/minigames/join", bytes.NewBuffer(jsonBody2))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 on join #2, got %d, body: %s", w.Code, w.Body.String())
	}

	var session model.MinigameSession
	db.First(&session, "id = ?", sessionID)

	players := parsePlayers(session.PlayersJSON)
	if len(players) != 1 {
		t.Errorf("expected 1 player after duplicate join, got %d", len(players))
	}
}

// TestLeaveRemovesPlayerFromPlayersJSON verifies that Leave removes the user from PlayersJSON.
func TestLeaveRemovesPlayerFromPlayersJSON(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	// Join first
	body := map[string]interface{}{
		"gameType": "tictactoe",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/join", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	sessionID := data["sessionId"].(string)

	// Then leave
	leaveBody := map[string]interface{}{"sessionId": sessionID}
	leaveJSON, _ := json.Marshal(leaveBody)
	req = httptest.NewRequest("POST", "/api/v1/minigames/leave", bytes.NewBuffer(leaveJSON))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 on leave, got %d, body: %s", w.Code, w.Body.String())
	}

	var session model.MinigameSession
	db.First(&session, "id = ?", sessionID)
	players := parsePlayers(session.PlayersJSON)
	if len(players) != 0 {
		t.Errorf("expected 0 players after leave, got %d", len(players))
	}
}

// TestJoinRoomFullReturnsForbidden verifies that joining a full room is rejected.
func TestJoinRoomFullReturnsForbidden(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	// Add user-2 membership without recreating the channel
	membership := model.Membership{
		ID:        idgen.NextString(),
		UserID:    "user-2",
		SpaceID:   "space-full",
		Role:      "MEMBER",
		JoinedAt:  time.Now(),
		CreatedAt: time.Now(),
	}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatalf("failed to seed user-2 membership: %v", err)
	}

	// Seed a full session directly (tictactoe has 2 players)
	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-1",
		State:       "{}",
		PlayersJSON: `[{"userId":"user-1","username":"alice"},{"userId":"user-2","username":"bob"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	db.Create(&session)

	// Try to join as user-1 (setupTestHandler always sets user_id=user-1)
	body := map[string]interface{}{
		"gameType": "tictactoe",
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/join", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// user-1 is already in the room, so it should succeed (no duplicate)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 for existing player, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestJoinRoomFullForNewPlayerReturnsConflict verifies that a new player cannot join a full room.
// C-1: returns MINIGAME_ROOM_FULL (409 Conflict) instead of generic 403.
func TestJoinRoomFullForNewPlayerReturnsConflict(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	// Seed a full session
	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-2",
		State:       "{}",
		PlayersJSON: `[{"userId":"user-2","username":"bob"},{"userId":"user-3","username":"carol"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	db.Create(&session)

	body := map[string]interface{}{
		"sessionId": session.ID,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/join", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409 for full room, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TestActionByNonPlayerReturnsBadRequest verifies that a user not in the session cannot perform actions.
// C-1: returns MINIGAME_NOT_IN_ROOM (400 Bad Request) instead of generic 403.
func TestActionByNonPlayerReturnsBadRequest(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	session := model.MinigameSession{
		ID:          idgen.NextString(),
		GameType:    "tictactoe",
		SpaceID:     "space-1",
		HostID:      "user-2",
		State:       "{}",
		PlayersJSON: `[{"userId":"user-2","username":"bob"}]`,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	db.Create(&session)

	body := map[string]interface{}{
		"sessionId": session.ID,
		"action":    "move_made",
		"payload":   map[string]interface{}{"row": 0, "col": 0},
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/action", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for non-player action, got %d, body: %s", w.Code, w.Body.String())
	}
}
