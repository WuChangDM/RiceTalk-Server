package minigames

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"ridgericetalk/features/minigames/gobang"
	"ridgericetalk/features/minigames/xiangqi"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 本文件覆盖 1.9.4 task-122 的 handler 层契约：
//   - 认输 / 求和接受 → 对局结束并**恰好写一条** minigame_history；
//   - 悔棋接受 → 回退盘面但**不写**历史；
//   - 已结束房间重复认输 → 幂等 200，不重复结算；
//   - 并发控制动作 → 乐观锁只让一个动作落库，历史不重复。

// newControlActionRouter 构造带 /action 的测试路由（X-Test-User 指定调用者）。
func newControlActionRouter(t *testing.T) (*Handler, *gin.Engine, *gorm.DB) {
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
	return h, r, db
}

// seedControlSpaceMembers 写入 space-1 的成员关系（Action 会校验 space membership）。
func seedControlSpaceMembers(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	for _, u := range []string{"alice", "bob", "carol"} {
		m := model.Membership{
			ID:       "membership-" + u,
			UserID:   u,
			SpaceID:  "space-1",
			Role:     "MEMBER",
			JoinedAt: now,
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("创建成员关系 %s 失败: %v", u, err)
		}
	}
}

// seedControlSession 直接落一局进行中的棋类对局（alice 先手、bob 后手、carol 观战）。
func seedControlSession(t *testing.T, db *gorm.DB, id, gameType string) {
	t.Helper()
	players := []map[string]interface{}{
		{"userId": "alice", "username": "Alice"},
		{"userId": "bob", "username": "Bob"},
	}
	var (
		state map[string]interface{}
		err   error
	)
	switch gameType {
	case "xiangqi":
		state, err = xiangqi.New().Init(players)
	case "gobang":
		state, err = gobang.New().Init(players)
	default:
		t.Fatalf("不支持的游戏类型: %s", gameType)
	}
	if err != nil {
		t.Fatalf("初始化 %s 对局失败: %v", gameType, err)
	}

	now := time.Now().UTC()
	session := model.MinigameSession{
		ID:             id,
		GameType:       gameType,
		SpaceID:        "space-1",
		HostID:         "alice",
		Status:         "playing",
		IsActive:       true,
		PlayersJSON:    `[{"userId":"alice","username":"Alice"},{"userId":"bob","username":"Bob"}]`,
		SpectatorsJSON: `[{"userId":"carol","username":"Carol"}]`,
		State:          marshalState(state),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("创建对局失败: %v", err)
	}
}

// postActionAs 以指定玩家身份提交动作，要求 HTTP 200 并返回 data 部分。
func postActionAs(t *testing.T, r *gin.Engine, sessionID, user, action string, payload interface{}) map[string]interface{} {
	t.Helper()
	body := map[string]interface{}{"sessionId": sessionID, "action": action}
	if payload != nil {
		body["payload"] = payload
	}
	w := postJSONAs(r, "/api/v1/minigames/action", user, body)
	if w.Code != http.StatusOK {
		t.Fatalf("%s 执行 %s 期望 200，实际 %d，body=%s", user, action, w.Code, w.Body.String())
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	return resp.Data
}

// historyCount 返回某局的结算历史条数。
func historyCount(t *testing.T, db *gorm.DB, sessionID string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.MinigameHistory{}).Where("session_id = ?", sessionID).Count(&n).Error; err != nil {
		t.Fatalf("统计历史失败: %v", err)
	}
	return n
}

// loadSession 读回对局行。
func loadSession(t *testing.T, db *gorm.DB, id string) model.MinigameSession {
	t.Helper()
	var s model.MinigameSession
	if err := db.First(&s, "id = ?", id).Error; err != nil {
		t.Fatalf("读取对局失败: %v", err)
	}
	return s
}

// stateOf 把对局 state 文本解析为 map。
func stateOf(t *testing.T, s model.MinigameSession) map[string]interface{} {
	t.Helper()
	var state map[string]interface{}
	if err := json.Unmarshal([]byte(s.State), &state); err != nil {
		t.Fatalf("解析 state 失败: %v", err)
	}
	return state
}

// TestControlResignWritesHistoryOnce 认输：结束 + 恰好一条历史；重复认输幂等不重复结算。
func TestControlResignWritesHistoryOnce(t *testing.T) {
	_, r, db := newControlActionRouter(t)
	seedControlSpaceMembers(t, db)
	seedControlSession(t, db, "s-resign-1", "xiangqi")

	postActionAs(t, r, "s-resign-1", "alice", "resign", nil)

	var hist []model.MinigameHistory
	if err := db.Where("session_id = ?", "s-resign-1").Find(&hist).Error; err != nil {
		t.Fatalf("查询历史失败: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("认输后应恰好写 1 条历史，实际 %d 条", len(hist))
	}
	if hist[0].WinnerID != "bob" || hist[0].IsDraw {
		t.Fatalf("alice 认输后历史应为 bob 获胜且非和棋，实际 winner=%q isDraw=%v", hist[0].WinnerID, hist[0].IsDraw)
	}
	if s := loadSession(t, db, "s-resign-1"); s.Status != "ended" {
		t.Fatalf("认输后对局状态应为 ended，实际 %q", s.Status)
	}

	// 重复认输：幂等 200，不重复结算、不重复写历史。
	data := postActionAs(t, r, "s-resign-1", "alice", "resign", nil)
	if got, _ := data["idempotent"].(bool); !got {
		t.Fatalf("重复认输应返回 idempotent=true，实际 data=%v", data)
	}
	if n := historyCount(t, db, "s-resign-1"); n != 1 {
		t.Fatalf("重复认输不应重复写历史，期望 1 条，实际 %d 条", n)
	}
}

// TestControlDrawAcceptWritesDrawHistory 求和接受：结束 + 一条和棋历史。
func TestControlDrawAcceptWritesDrawHistory(t *testing.T) {
	_, r, db := newControlActionRouter(t)
	seedControlSpaceMembers(t, db)
	seedControlSession(t, db, "s-draw-1", "gobang")

	postActionAs(t, r, "s-draw-1", "alice", "draw_offer", nil)
	postActionAs(t, r, "s-draw-1", "bob", "draw_accept", nil)

	var hist []model.MinigameHistory
	if err := db.Where("session_id = ?", "s-draw-1").Find(&hist).Error; err != nil {
		t.Fatalf("查询历史失败: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("和棋后应恰好写 1 条历史，实际 %d 条", len(hist))
	}
	if !hist[0].IsDraw {
		t.Fatalf("和棋历史 isDraw 应为 true，实际 false（winner=%q）", hist[0].WinnerID)
	}
	if s := loadSession(t, db, "s-draw-1"); s.Status != "ended" {
		t.Fatalf("和棋后对局状态应为 ended，实际 %q", s.Status)
	}
}

// TestControlUndoDoesNotWriteHistory 悔棋：盘面回退但绝不写结算历史。
func TestControlUndoDoesNotWriteHistory(t *testing.T) {
	_, r, db := newControlActionRouter(t)
	seedControlSpaceMembers(t, db)
	seedControlSession(t, db, "s-undo-1", "gobang")

	postActionAs(t, r, "s-undo-1", "alice", "move_made", map[string]int{"r": 7, "c": 7})
	// alice 是上一手落子方 → 由 alice 请求撤回自己那一手、bob 确认。
	postActionAs(t, r, "s-undo-1", "alice", "undo_request", nil)
	postActionAs(t, r, "s-undo-1", "bob", "undo_accept", nil)

	if n := historyCount(t, db, "s-undo-1"); n != 0 {
		t.Fatalf("悔棋不应写结算历史，实际 %d 条", n)
	}
	s := loadSession(t, db, "s-undo-1")
	if s.Status != "playing" {
		t.Fatalf("悔棋后对局应继续（playing），实际 %q", s.Status)
	}
	state := stateOf(t, s)
	if got, _ := state["currentPlayer"].(string); got != "alice" {
		t.Fatalf("悔棋后应轮到发起方 alice 重走，实际 %q", got)
	}
	if got, _ := state["undoCount"].(float64); int(got) != 1 {
		t.Fatalf("悔棋后计数应为 1，实际 %v", state["undoCount"])
	}
	rows, _ := state["board"].([]interface{})
	row, _ := rows[7].([]interface{})
	if row[7] != nil {
		t.Fatalf("悔棋后 (7,7) 的落子应被清空，实际 %v", row[7])
	}
}

// TestControlSpectatorCannotAct 观战者不能认输，也不写历史。
func TestControlSpectatorCannotAct(t *testing.T) {
	_, r, db := newControlActionRouter(t)
	seedControlSpaceMembers(t, db)
	seedControlSession(t, db, "s-spectator-1", "xiangqi")

	w := postJSONAs(r, "/api/v1/minigames/action", "carol", map[string]interface{}{
		"sessionId": "s-spectator-1",
		"action":    "resign",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("观战者认输应返回 400，实际 %d，body=%s", w.Code, w.Body.String())
	}
	if n := historyCount(t, db, "s-spectator-1"); n != 0 {
		t.Fatalf("观战者认输不应写历史，实际 %d 条", n)
	}
	if s := loadSession(t, db, "s-spectator-1"); s.Status != "playing" {
		t.Fatalf("观战者认输不应结束对局，实际状态 %q", s.Status)
	}
}

// TestControlConcurrentResignKeepsSingleHistory 并发认输：
// 乐观锁保证只有一个动作落库，历史不重复、状态不丢。
func TestControlConcurrentResignKeepsSingleHistory(t *testing.T) {
	_, r, db := newControlActionRouter(t)
	seedControlSpaceMembers(t, db)
	seedControlSession(t, db, "s-race-1", "xiangqi")

	const n = 6
	codes := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		user := "alice"
		if i%2 == 1 {
			user = "bob"
		}
		go func(idx int, who string) {
			defer wg.Done()
			w := postJSONAs(r, "/api/v1/minigames/action", who, map[string]interface{}{
				"sessionId": "s-race-1",
				"action":    "resign",
			})
			codes[idx] = w.Code
		}(i, user)
	}
	wg.Wait()

	okCount := 0
	for _, code := range codes {
		if code == http.StatusOK {
			okCount++
		}
	}
	if okCount == 0 {
		t.Fatalf("并发认输应至少有一个成功，实际状态码 %v", codes)
	}
	if got := historyCount(t, db, "s-race-1"); got != 1 {
		t.Fatalf("并发认输应恰好写 1 条历史，实际 %d 条（状态码 %v）", got, codes)
	}
	s := loadSession(t, db, "s-race-1")
	if s.Status != "ended" {
		t.Fatalf("并发认输后对局应 ended，实际 %q", s.Status)
	}
	state := stateOf(t, s)
	if w, _ := state["winner"].(string); w != "alice" && w != "bob" {
		t.Fatalf("并发认输后 winner 应为一个玩家，实际 %q", w)
	}
}
