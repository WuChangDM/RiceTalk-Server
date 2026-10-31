package minigames

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ridgericetalk/features/minigames/riichi"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// M14（task-82）：日麻 GetState 玩家视角必须剥离 snapshot（含牌山 seed 与全部动作），
// 否则玩家可离线用 wallFor(seed, handIndex) 复算四家手牌与后续摸牌顺序。
//
// 这些用例刻意在 handler 层跑（真 SQLite + 真引擎 + 真路由），断言的是**下发给客户端的
// JSON 字符串**，而不是结构体字段 —— 只有字符串断言才能证明字段真的没被序列化出去。

func newTask82Router(t *testing.T) (*Handler, *gin.Engine, *gorm.DB) {
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
	g.POST("/action", auth, h.Action)
	g.GET("/sessions/:id/state", auth, h.GetState)
	return h, r, db
}

// seedRiichiSession 用真引擎初始化一局日麻并落库（4 名玩家 alice/bob/carol/dave）。
func seedRiichiSession(t *testing.T, db *gorm.DB, sessionID string) {
	t.Helper()
	ids := []string{"alice", "bob", "carol", "dave"}
	players := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		players = append(players, map[string]interface{}{"userId": id, "displayName": strings.ToUpper(id[:1]) + id[1:]})
		if err := db.Create(&model.Membership{ID: "m-" + id, UserID: id, SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
			t.Fatalf("seed membership %s: %v", id, err)
		}
	}
	playersJSON, _ := json.Marshal(players)

	state, err := riichi.New().Init(players)
	if err != nil {
		t.Fatalf("riichi engine init failed: %v", err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal riichi state failed: %v", err)
	}

	session := model.MinigameSession{
		ID:             sessionID,
		GameType:       "riichi",
		SpaceID:        "space-1",
		HostID:         "alice",
		State:          string(stateJSON),
		Status:         "playing",
		PlayersJSON:    string(playersJSON),
		SpectatorsJSON: `[{"userId":"charlie"}]`,
		IsActive:       true,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := db.Create(&model.Membership{ID: "m-charlie", UserID: "charlie", SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("seed spectator membership: %v", err)
	}
}

// getStateRaw 调 GetState 并返回 data.state 原始 JSON 字符串。
func getStateRaw(t *testing.T, r *gin.Engine, sessionID, user string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/minigames/sessions/"+sessionID+"/state", nil)
	req.Header.Set("X-Test-User", user)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetState(%s) status=%d body=%s", user, w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			State string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, w.Body.String())
	}
	return resp.Data.State
}

// TestRiichiPlayerStateStripsSnapshot（M14 核心回归）
// 断言玩家视角 JSON 不含 snapshot/seed/wall/actions 字样，且本座位视图完整。
func TestRiichiPlayerStateStripsSnapshot(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedRiichiSession(t, db, "s-m14-alice")

	raw := getStateRaw(t, r, "s-m14-alice", "alice")

	for _, banned := range []string{"snapshot", "seed", "wall", "actions"} {
		if strings.Contains(raw, `"`+banned+`"`) {
			t.Fatalf("玩家视角 state 不应出现 %q，实际=%s", banned, truncateForLog(raw))
		}
	}
	for _, required := range []string{`"views"`, `"closed_tiles"`, `"legal_actions"`, `"seat":0`, `"players"`, `"match"`} {
		if !strings.Contains(raw, required) {
			t.Fatalf("玩家视角 state 缺少 %s，实际=%s", required, truncateForLog(raw))
		}
	}

	var state map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("unmarshal player state failed: %v", err)
	}
	views, ok := state["views"].([]interface{})
	if !ok || len(views) != 4 {
		t.Fatalf("views 应保留 4 个座位（客户端按 views[seat] 索引），实际 len=%d", len(views))
	}
	own, _ := views[0].(map[string]interface{})
	if tiles, ok := own["closed_tiles"].([]interface{}); !ok || len(tiles) == 0 {
		t.Fatalf("自己座位应保留 closed_tiles（13 张起手），实际=%v", own["closed_tiles"])
	}
	if _, exists := own["legal_actions"]; !exists {
		t.Fatalf("自己座位应保留 legal_actions")
	}
	for i := 1; i < len(views); i++ {
		other, _ := views[i].(map[string]interface{})
		for _, banned := range []string{"closed_tiles", "drawn", "legal_actions"} {
			if _, exists := other[banned]; exists {
				t.Fatalf("别家座位 views[%d] 不应含 %s（否则可看到对手手牌）", i, banned)
			}
		}
	}
}

// TestRiichiPlayerStateCanContinuePlaying（M14 回归：剥离后仍可正常出牌）
func TestRiichiPlayerStateCanContinuePlaying(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedRiichiSession(t, db, "s-m14-play")

	raw := getStateRaw(t, r, "s-m14-play", "alice")
	var state map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("unmarshal player state failed: %v", err)
	}
	views := state["views"].([]interface{})
	own := views[0].(map[string]interface{})
	legal, _ := own["legal_actions"].([]interface{})
	if len(legal) == 0 {
		t.Fatalf("自己座位应至少有 1 个合法动作，实际=%v", own)
	}

	var discard map[string]interface{}
	for _, item := range legal {
		act, _ := item.(map[string]interface{})
		if kind, _ := act["kind"].(string); kind == "discard" {
			discard = act
			break
		}
	}
	if discard == nil {
		t.Fatalf("起手应存在 discard 合法动作，实际=%v", legal)
	}
	tiles, _ := discard["tiles"].([]interface{})
	if len(tiles) == 0 {
		t.Fatalf("discard 动作应带 tiles，实际=%v", discard)
	}
	payload, _ := json.Marshal(map[string]interface{}{"tile": tiles[0]})
	body, _ := json.Marshal(map[string]interface{}{
		"sessionId": "s-m14-play",
		"action":    "discard",
		"payload":   json.RawMessage(payload),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/minigames/action", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "alice")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("剥离 snapshot 后出牌应成功，实际 status=%d body=%s", w.Code, truncateForLog(w.Body.String()))
	}

	// 出牌后行动权应离开 alice，证明对局在无 snapshot 的情况下仍能继续。
	//
	// 注意：Init 的牌山 seed 取 time.Now()，随机手牌可能让别家在 alice 出牌后进入
	// 鸣牌窗口（chi/pon/ron），此时 currentPlayer 是那个申告座位而非固定 bob，
	// 因此只断言「不再是 alice 且非空」，不锁具体座位。
	after := getStateRaw(t, r, "s-m14-play", "alice")
	var afterState map[string]interface{}
	if err := json.Unmarshal([]byte(after), &afterState); err != nil {
		t.Fatalf("unmarshal after state failed: %v", err)
	}
	if cur, _ := afterState["currentPlayer"].(string); cur == "" || cur == "alice" {
		t.Fatalf("alice 出牌后 currentPlayer 应离开 alice，实际=%q", cur)
	}
}

// TestRiichiSpectatorStateUnchanged（旁观口径：仍为公开视角，无 snapshot/hands）
func TestRiichiSpectatorStateUnchanged(t *testing.T) {
	_, r, db := newTask82Router(t)
	seedRiichiSession(t, db, "s-m14-spec")

	raw := getStateRaw(t, r, "s-m14-spec", "charlie")
	if strings.Contains(raw, `"snapshot"`) {
		t.Fatalf("旁观视角本就不含 snapshot（SpectatorState 既有行为），实际=%s", truncateForLog(raw))
	}
	if !strings.Contains(raw, `"spectator"`) {
		t.Fatalf("旁观视角应含 spectator 公开视图，实际=%s", truncateForLog(raw))
	}
	if strings.Contains(raw, `"closed_tiles"`) {
		t.Fatalf("旁观视角不应含任何手牌 closed_tiles，实际=%s", truncateForLog(raw))
	}
}

// TestNonRiichiPlayerStateUnchanged（非日麻游戏零变化）
func TestNonRiichiPlayerStateUnchanged(t *testing.T) {
	_, r, db := newTask82Router(t)
	players := []map[string]interface{}{
		{"userId": "alice", "displayName": "Alice"},
		{"userId": "bob", "displayName": "Bob"},
	}
	playersJSON, _ := json.Marshal(players)
	stateJSON := `{"game":"tictactoe","status":"playing","players":["alice","bob"],"currentPlayer":"alice","winner":null,"board":[null,null,null,null,null,null,null,null,null]}`
	for _, id := range []string{"alice", "bob"} {
		if err := db.Create(&model.Membership{ID: "m-" + id, UserID: id, SpaceID: "space-1", Role: "MEMBER"}).Error; err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}
	if err := db.Create(&model.MinigameSession{
		ID: "s-m14-ttt", GameType: "tictactoe", SpaceID: "space-1", HostID: "alice",
		State: stateJSON, Status: "playing", PlayersJSON: string(playersJSON), IsActive: true,
	}).Error; err != nil {
		t.Fatalf("seed tictactoe session: %v", err)
	}

	raw := getStateRaw(t, r, "s-m14-ttt", "alice")
	if !strings.Contains(raw, `"board"`) || !strings.Contains(raw, `"tictactoe"`) {
		t.Fatalf("非日麻游戏 state 应原样下发（无 PlayerStateProvider），实际=%s", truncateForLog(raw))
	}
	if strings.Contains(raw, `"snapshot"`) {
		t.Fatalf("非日麻游戏不应凭空出现 snapshot，实际=%s", truncateForLog(raw))
	}
}

func truncateForLog(s string) string {
	const max = 400
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
