package minigames

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// 1.9.5 task-138（规则 3）：排行榜写成绩改为原子条件更新。
//
// 本文件覆盖三类风险：
//  1. 并发提交时不再丢最好成绩（原实现是 First→内存比较→Save 的读改写）；
//  2. 权威计分（棋类胜场 +1）并发结算不丢更新；
//  3. 真库故障必须 fail-fast 返回 500，不能伪装成「0 分 / 未上榜」。

// newRaceRouter 挂排行榜相关接口，并固定调用者为 alice。
//
// 单连接（MaxOpenConns(1)）是为了让 SQLite 下的并发用例稳定复现「读改写丢更新」：
// 若实现退化为先查后写，串行化也挡不住两次读之间被插队。
func newRaceRouter(t *testing.T) (*Handler, *gin.Engine, *gorm.DB) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取 sql.DB 失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	h := NewHandler(db, &config.Config{
		JWTSecret:     "test-jwt-secret-for-minigames-32bytes!!",
		EncryptionKey: "test-encryption-key-for-minigames-32bytes!",
	}, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1/minigames")
	auth := func(c *gin.Context) {
		c.Set("user_id", "alice")
		c.Set("username", "alice")
		c.Set("role", "MEMBER")
		c.Set("space_id", "space-1")
		c.Next()
	}
	g.POST("/leaderboard", auth, h.SubmitScore)
	g.GET("/leaderboard", auth, h.GetLeaderboard)
	g.GET("/scores/me", auth, h.GetMyScore)
	return h, r, db
}

func leaderboardRows(t *testing.T, db *gorm.DB, gameType, userID string) []model.MinigameLeaderboard {
	t.Helper()
	var rows []model.MinigameLeaderboard
	if err := db.Where("game_type = ? AND user_id = ?", gameType, userID).Find(&rows).Error; err != nil {
		t.Fatalf("查排行榜行失败: %v", err)
	}
	return rows
}

// runConcurrently 起 n 个 goroutine 并发执行 fn，任一失败都汇总后让用例失败。
func runConcurrently(t *testing.T, n int, fn func(i int) error) {
	t.Helper()
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := fn(i); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("并发执行失败: %v", err)
	}
}

// TestUpsertBestScoreConcurrentKeepsMax 并发提交 10 个分数，只保留最高分且仅一行。
func TestUpsertBestScoreConcurrentKeepsMax(t *testing.T) {
	h, _, db := newRaceRouter(t)

	runConcurrently(t, 10, func(i int) error {
		_, _, err := h.upsertBestScore("2048", "alice", int64((i+1)*100), "desc")
		return err
	})
	if t.Failed() {
		return
	}

	rows := leaderboardRows(t, db, "2048", "alice")
	if len(rows) != 1 {
		t.Fatalf("同一用户同一游戏应只有 1 行，实际 %d 行", len(rows))
	}
	if rows[0].BestScore != 1000 {
		t.Fatalf("并发提交后应保留最高分 1000，实际 %d", rows[0].BestScore)
	}
}

// TestUpsertBestScoreConcurrentKeepsMin 升序榜（用时类）并发提交只保留最小值。
func TestUpsertBestScoreConcurrentKeepsMin(t *testing.T) {
	h, _, db := newRaceRouter(t)

	runConcurrently(t, 10, func(i int) error {
		_, _, err := h.upsertBestScore("2048", "alice", int64((i+1)*100), "asc")
		return err
	})
	if t.Failed() {
		return
	}

	rows := leaderboardRows(t, db, "2048", "alice")
	if len(rows) != 1 {
		t.Fatalf("同一用户同一游戏应只有 1 行，实际 %d 行", len(rows))
	}
	if rows[0].BestScore != 100 {
		t.Fatalf("升序榜并发提交后应保留最小分 100，实际 %d", rows[0].BestScore)
	}
}

// TestAddAuthoritativeWinConcurrentNoLostUpdate 棋类 10 次并发结算，胜场必须累加到 10。
func TestAddAuthoritativeWinConcurrentNoLostUpdate(t *testing.T) {
	h, _, db := newRaceRouter(t)

	runConcurrently(t, 10, func(int) error {
		return h.addAuthoritativeWin("xiangqi", "alice")
	})
	if t.Failed() {
		return
	}

	rows := leaderboardRows(t, db, "xiangqi", "alice")
	if len(rows) != 1 {
		t.Fatalf("权威计分应只有 1 行，实际 %d 行", len(rows))
	}
	if rows[0].BestScore != 10 {
		t.Fatalf("10 次并发胜利后胜场应为 10，实际 %d（存在丢更新）", rows[0].BestScore)
	}
}

// TestSubmitScoreFailsFastWhenLeaderboardTableMissing 规则 1：写成绩时真库故障必须 500。
func TestSubmitScoreFailsFastWhenLeaderboardTableMissing(t *testing.T) {
	_, r, db := newRaceRouter(t)
	if err := db.Migrator().DropTable(&model.MinigameLeaderboard{}); err != nil {
		t.Fatalf("删除排行榜表失败: %v", err)
	}

	w := postJSONAs(r, "/api/v1/minigames/leaderboard", "alice",
		map[string]interface{}{"gameType": "2048", "score": 100})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("排行榜表缺失时应返回 500，实际 %d，body=%s", w.Code, w.Body.String())
	}
	if code, _ := decodeAPI(t, w); code != "SYSTEM_INTERNAL_ERROR" {
		t.Fatalf("错误码应为 SYSTEM_INTERNAL_ERROR，实际 %q", code)
	}
}

// TestAuthoritativeSubmitScoreFailsFastWhenTableMissing 权威计分分支同样不能伪装成 0 分。
func TestAuthoritativeSubmitScoreFailsFastWhenTableMissing(t *testing.T) {
	_, r, db := newRaceRouter(t)
	if err := db.Migrator().DropTable(&model.MinigameLeaderboard{}); err != nil {
		t.Fatalf("删除排行榜表失败: %v", err)
	}

	w := postJSONAs(r, "/api/v1/minigames/leaderboard", "alice",
		map[string]interface{}{"gameType": "xiangqi", "score": 1})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("权威计分分支在表缺失时应返回 500，实际 %d，body=%s", w.Code, w.Body.String())
	}
}

// TestGetMyScoreFailsFastWhenTableMissing 规则 1：查自己成绩遇库故障不能回 0 分。
func TestGetMyScoreFailsFastWhenTableMissing(t *testing.T) {
	_, r, db := newRaceRouter(t)
	if err := db.Migrator().DropTable(&model.MinigameLeaderboard{}); err != nil {
		t.Fatalf("删除排行榜表失败: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/minigames/scores/me?gameType=2048", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("查自己成绩遇库故障应返回 500，实际 %d，body=%s", w.Code, w.Body.String())
	}
}

// TestGetLeaderboardFailsFastWhenUserTableMissing 规则 1：显示名解析失败不能静默降级成空名字。
func TestGetLeaderboardFailsFastWhenUserTableMissing(t *testing.T) {
	_, r, db := newRaceRouter(t)
	if err := db.Create(&model.MinigameLeaderboard{
		ID:        "lb-1",
		GameType:  "2048",
		UserID:    "alice",
		Username:  "",
		BestScore: 512,
	}).Error; err != nil {
		t.Fatalf("写入排行榜行失败: %v", err)
	}
	if err := db.Migrator().DropTable(&model.User{}); err != nil {
		t.Fatalf("删除用户表失败: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/minigames/leaderboard?gameType=2048", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("显示名解析失败应返回 500，实际 %d，body=%s", w.Code, w.Body.String())
	}
}
