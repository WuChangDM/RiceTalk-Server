package minigames

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newScoreRouter 挂排行榜上报接口，并用 X-Test-User 模拟调用者身份。
func newScoreRouter(t *testing.T) (*Handler, *gin.Engine, *gorm.DB) {
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
	g.POST("/leaderboard", auth, h.SubmitScore)
	g.GET("/leaderboard", auth, h.GetLeaderboard)
	return h, r, db
}

// decodeAPI 解析统一响应 {code,message,data}。
func decodeAPI(t *testing.T, w *httptest.ResponseRecorder) (string, map[string]interface{}) {
	t.Helper()
	var resp struct {
		Code string                 `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, w.Body.String())
	}
	return resp.Code, resp.Data
}

// TestSubmitScoreAuthoritativeForBoardGames D1：棋类客户端上报分数一律忽略，
// 排行榜只由服务端结算写入；接口仍返回 200 并回显当前成绩（兼容旧客户端）。
func TestSubmitScoreAuthoritativeForBoardGames(t *testing.T) {
	h, r, db := newScoreRouter(t)

	// 1) 客户端伪造高分：不生效、不落库。
	w := postJSONAs(r, "/api/v1/minigames/leaderboard", "alice",
		map[string]interface{}{"gameType": "tictactoe", "score": 999999})
	if w.Code != http.StatusOK {
		t.Fatalf("权威计分游戏应返回 200（忽略分数），实际 %d: %s", w.Code, w.Body.String())
	}
	code, data := decodeAPI(t, w)
	if code != "OK" {
		t.Fatalf("期望 OK，实际 %s", code)
	}
	if data["authoritative"] != true {
		t.Fatalf("响应应标记 authoritative=true，实际 %v", data["authoritative"])
	}
	if data["updated"] != false {
		t.Fatalf("客户端分数不应产生更新，实际 updated=%v", data["updated"])
	}
	if got, _ := data["bestScore"].(float64); got != 0 {
		t.Fatalf("伪造分数不应落库，bestScore 期望 0，实际 %v", got)
	}
	var count int64
	db.Model(&model.MinigameLeaderboard{}).
		Where("game_type = ? AND user_id = ?", "tictactoe", "alice").Count(&count)
	if count != 0 {
		t.Fatalf("客户端上报不应写入排行榜，实际 %d 行", count)
	}

	// 2) 服务端结算一局后，客户端再上报高分仍不改变成绩。
	session := &model.MinigameSession{ID: "s-d1-1", GameType: "tictactoe", SpaceID: "space-1", HostID: "alice"}
	h.finishGame(session, "alice", false)
	w = postJSONAs(r, "/api/v1/minigames/leaderboard", "alice",
		map[string]interface{}{"gameType": "tictactoe", "score": 888888})
	_, data = decodeAPI(t, w)
	if got, _ := data["bestScore"].(float64); got != 1 {
		t.Fatalf("客户端高分不应覆盖服务端胜场，bestScore 期望 1，实际 %v", got)
	}
}

// TestFinishGameAwardsServerWin D1：服务端结算给胜者累加胜场；平局/无胜者/非权威游戏不加分。
func TestFinishGameAwardsServerWin(t *testing.T) {
	h, _, db := newScoreRouter(t)

	chessSession := &model.MinigameSession{ID: "s-d1-chess", GameType: "chess", SpaceID: "space-1", HostID: "alice"}
	h.finishGame(chessSession, "bob", false)

	var row model.MinigameLeaderboard
	if err := db.Where("game_type = ? AND user_id = ?", "chess", "bob").First(&row).Error; err != nil {
		t.Fatalf("胜者成绩应落库: %v", err)
	}
	if row.BestScore != 1 {
		t.Fatalf("首胜应记 1 场，实际 %d", row.BestScore)
	}

	h.finishGame(chessSession, "bob", false)
	if err := db.Where("game_type = ? AND user_id = ?", "chess", "bob").First(&row).Error; err != nil {
		t.Fatalf("再次查询失败: %v", err)
	}
	if row.BestScore != 2 {
		t.Fatalf("第二胜应累计到 2，实际 %d", row.BestScore)
	}

	// 平局与「无胜者」都不加分。
	h.finishGame(chessSession, "", true)
	if err := db.Where("game_type = ? AND user_id = ?", "chess", "bob").First(&row).Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if row.BestScore != 2 {
		t.Fatalf("平局不应加分，实际 %d", row.BestScore)
	}

	// 无排行榜的联机游戏（狼人杀）不产生胜场记录。
	h.finishGame(&model.MinigameSession{ID: "s-d1-wolf", GameType: "werewolf", SpaceID: "space-1"}, "alice", false)
	var wolfRows int64
	db.Model(&model.MinigameLeaderboard{}).Where("game_type = ?", "werewolf").Count(&wolfRows)
	if wolfRows != 0 {
		t.Fatalf("狼人杀不应产生胜场排行榜，实际 %d 行", wolfRows)
	}
}

// TestSinglePlayerScoreCapRejected D1：单机排行榜超上限直接拒收。
func TestSinglePlayerScoreCapRejected(t *testing.T) {
	_, r, db := newScoreRouter(t)

	overCap := singlePlayerScoreCaps["2048"] + 1
	w := postJSONAs(r, "/api/v1/minigames/leaderboard", "alice",
		map[string]interface{}{"gameType": "2048", "score": overCap})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("超上限应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	code, _ := decodeAPI(t, w)
	if code != "MINIGAME_INVALID_ACTION" {
		t.Fatalf("错误码期望 MINIGAME_INVALID_ACTION，实际 %s", code)
	}
	var count int64
	db.Model(&model.MinigameLeaderboard{}).Where("game_type = ?", "2048").Count(&count)
	if count != 0 {
		t.Fatalf("被拒收的分数不应落库，实际 %d 行", count)
	}

	// 正常分数仍可上报。
	w = postJSONAs(r, "/api/v1/minigames/leaderboard", "alice",
		map[string]interface{}{"gameType": "2048", "score": 100})
	if w.Code != http.StatusOK {
		t.Fatalf("正常分数应 200，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestSinglePlayerScoreAnomalyRejected D1：短时暴涨（>=20 倍且基数 >=1000）被判异常。
func TestSinglePlayerScoreAnomalyRejected(t *testing.T) {
	_, r, _ := newScoreRouter(t)

	if w := postJSONAs(r, "/api/v1/minigames/leaderboard", "alice",
		map[string]interface{}{"gameType": "2048", "score": 5000}); w.Code != http.StatusOK {
		t.Fatalf("首次上报应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	anomalous := int64(5000*scoreAnomalyFactor + 1)
	w := postJSONAs(r, "/api/v1/minigames/leaderboard", "alice",
		map[string]interface{}{"gameType": "2048", "score": anomalous})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("暴涨分数应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	code, _ := decodeAPI(t, w)
	if code != "MINIGAME_INVALID_ACTION" {
		t.Fatalf("错误码期望 MINIGAME_INVALID_ACTION，实际 %s", code)
	}

	// asc 类（最短用时）同样受上限约束，且正常值可通过。
	if w := postJSONAs(r, "/api/v1/minigames/leaderboard", "bob",
		map[string]interface{}{"gameType": "chess-local", "score": 90_000}); w.Code != http.StatusBadRequest {
		t.Fatalf("asc 超上限应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	if w := postJSONAs(r, "/api/v1/minigames/leaderboard", "bob",
		map[string]interface{}{"gameType": "chess-local", "score": 300}); w.Code != http.StatusOK {
		t.Fatalf("asc 正常值应 200，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestBoardGameLeaderboardUsesWinDimension D1：棋类排行榜维度为「胜场数」。
func TestBoardGameLeaderboardUsesWinDimension(t *testing.T) {
	for _, gameType := range []string{"tictactoe", "chess", "xiangqi", "gobang", "ludo"} {
		info, ok := getGameInfo(gameType)
		if !ok {
			t.Fatalf("%s 应已注册", gameType)
		}
		if info.ScoreDimension != authoritativeScoreDimension {
			t.Fatalf("%s 的排行榜维度期望 %q，实际 %q", gameType, authoritativeScoreDimension, info.ScoreDimension)
		}
		if !isAuthoritativeScored(gameType) {
			t.Fatalf("%s 应判定为服务端权威计分", gameType)
		}
	}
	// 非棋类联机游戏保持无排行榜。
	for _, gameType := range []string{"werewolf", "doudizhu", "riichi"} {
		info, _ := getGameInfo(gameType)
		if info.ScoreDimension != "" {
			t.Fatalf("%s 不应有排行榜维度，实际 %q", gameType, info.ScoreDimension)
		}
		if isAuthoritativeScored(gameType) {
			t.Fatalf("%s 不应判定为权威计分", gameType)
		}
	}
}
