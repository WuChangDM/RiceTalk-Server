package minigames

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

const defaultLeaderboardLimit = 20
const maxLeaderboardLimit = 100

// leaderboardOrder resolves a game's ranking direction (设计 §15.7.4)。
// "desc"（越大越好，如最高分/最高连胜）或 "asc"（越小越好，如最短用时）。
// 未注册的游戏按 desc 处理，保持历史兼容。
func leaderboardOrder(gameType string) string {
	if info, ok := getGameInfo(gameType); ok && info.ScoreOrder == "asc" {
		return "asc"
	}
	return "desc"
}

// SubmitScore handles POST /minigames/leaderboard.
// Only scores > 0 are stored; each user keeps their best score per game.
// "Best" depends on the game's ScoreOrder: max for desc, min for asc (§15.7.4).
func (h *Handler) SubmitScore(c *gin.Context) {
	var body struct {
		GameType string `json:"gameType" binding:"required"`
		Score    int64  `json:"score"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if body.Score <= 0 {
		errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "score must be greater than 0"))
		return
	}

	// D1（task-65）：多人棋类为服务端权威计分（胜场数），客户端上传的 score 一律忽略。
	// 这里仍返回 200 + 当前成绩，保留请求体字段以兼容旧客户端；实际加分由
	// finishGame -> addAuthoritativeWin 在服务端结算时完成。
	if isAuthoritativeScored(body.GameType) {
		bestScore := int64(0)
		var authoritativeRow model.MinigameLeaderboard
		err := h.db.Where("game_type = ? AND user_id = ?", body.GameType, middleware.GetUserID(c)).
			First(&authoritativeRow).Error
		if err == nil {
			bestScore = authoritativeRow.BestScore
		} else if err != gorm.ErrRecordNotFound {
			// 规则 1：真库故障不能伪装成「当前 0 分」。
			errors.JSONError(c, errors.ErrInternal)
			return
		}
		errors.Success(c, gin.H{
			"bestScore":     bestScore,
			"updated":       false,
			"authoritative": true,
			"dimension":     authoritativeScoreDimension,
		})
		return
	}

	// 已知但不提供排行榜的游戏（狼人杀/斗地主/日麻）拒绝提交（设计 §15.7.4）。
	if info, ok := getGameInfo(body.GameType); ok && info.ScoreDimension == "" {
		errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "this game has no leaderboard"))
		return
	}

	order := leaderboardOrder(body.GameType)
	userID := middleware.GetUserID(c)

	// 先读当前最好成绩：只用于单机分数异常检测（D1 阈值），
	// 写决策一律走 upsertBestScore 的原子条件更新，避免「先查后写」竞态。
	currentBest, err := h.currentBestScore(body.GameType, userID)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// D1：单机排行榜限分数上限 + 异常暴涨检测（阈值见 score.go 的 singlePlayerScoreCaps）。
	if info, ok := getGameInfo(body.GameType); ok && info.LocalOnly {
		if verr := validateSinglePlayerScore(info, body.Score, currentBest); verr != nil {
			errors.JSONError(c, verr)
			return
		}
	}

	updated, bestScore, err := h.upsertBestScore(body.GameType, userID, body.Score, order)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	errors.Success(c, gin.H{
		"bestScore": bestScore,
		"updated":   updated,
	})
}

// currentBestScore 读该用户在某游戏的当前最好成绩（无记录返回 0）。
func (h *Handler) currentBestScore(gameType, userID string) (int64, error) {
	var row model.MinigameLeaderboard
	err := h.db.Where("game_type = ? AND user_id = ?", gameType, userID).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.BestScore, nil
}

// upsertBestScore 维护「每人每游戏一条最好成绩」，返回 (是否更新, 更新后的最好成绩, 错误)。
//
// 1.9.5 task-138（规则 3）：原实现是「First 查行 → 内存比较 → Save/Create」，并发提交
// 时两个请求都读到旧值、后写覆盖前写 → 丢最好成绩；错误也被上抛成 500 但读改写本身
// 不安全。现改为两条原子路径：
//   - 条件更新：`UPDATE ... SET best_score = ? WHERE ... AND best_score < ?`（asc 为 `>`），
//     只有「确实更好」才命中，天然并发安全且不会把更好的成绩改差；
//   - 无行时插入初始行；并发下插入可能撞 PG 唯一约束 → 回退一次条件更新。
func (h *Handler) upsertBestScore(gameType, userID string, score int64, order string) (bool, int64, error) {
	better := "best_score < ?"
	if order == "asc" {
		better = "best_score > ?"
	}

	lock := leaderboardKeyLock(gameType, userID)
	lock.Lock()
	defer lock.Unlock()

	res := h.db.Model(&model.MinigameLeaderboard{}).
		Where("game_type = ? AND user_id = ? AND "+better, gameType, userID, score).
		Update("best_score", score)
	if res.Error != nil {
		return false, 0, res.Error
	}
	if res.RowsAffected > 0 {
		return true, score, nil
	}

	// 条件更新 0 行有两种可能：①还没有该用户的行；②已有更好的成绩。
	// 先区分，避免「已有更好成绩」时误插一行重复记录。
	exists, err := h.leaderboardRowExists(gameType, userID)
	if err != nil {
		return false, 0, err
	}
	if exists {
		best, err := h.currentBestScore(gameType, userID)
		if err != nil {
			return false, 0, err
		}
		return false, best, nil
	}

	// 无既有行 → 插入初始行；并发下（跨进程）插入可能撞 PG 唯一约束 → 回退一次条件更新。
	createErr := h.db.Create(&model.MinigameLeaderboard{
		ID:        idgen.NextString(),
		GameType:  gameType,
		UserID:    userID,
		BestScore: score,
	}).Error
	if createErr == nil {
		return true, score, nil
	}
	retry := h.db.Model(&model.MinigameLeaderboard{}).
		Where("game_type = ? AND user_id = ? AND "+better, gameType, userID, score).
		Update("best_score", score)
	if retry.Error != nil {
		return false, 0, retry.Error
	}
	updated := retry.RowsAffected > 0
	best, err := h.currentBestScore(gameType, userID)
	if err != nil {
		return false, 0, err
	}
	return updated, best, nil
}

// leaderboardRowExists 判断该用户在某游戏是否已有排行榜行（仅取主键，避免整行扫描）。
func (h *Handler) leaderboardRowExists(gameType, userID string) (bool, error) {
	var ids []string
	err := h.db.Model(&model.MinigameLeaderboard{}).
		Where("game_type = ? AND user_id = ?", gameType, userID).
		Limit(1).
		Pluck("id", &ids).Error
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

// GetLeaderboard handles GET /minigames/leaderboard.
// Returns only users with best_score > 0, ordered by the game's ranking direction.
func (h *Handler) GetLeaderboard(c *gin.Context) {
	gameType := c.Query("gameType")
	if gameType == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("gameType is required"))
		return
	}

	limitStr := c.DefaultQuery("limit", strconv.Itoa(defaultLeaderboardLimit))
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = defaultLeaderboardLimit
	}
	if limit > maxLeaderboardLimit {
		limit = maxLeaderboardLimit
	}

	order := leaderboardOrder(gameType)
	orderClause := "best_score DESC"
	if order == "asc" {
		orderClause = "best_score ASC"
	}

	var records []model.MinigameLeaderboard
	// 多取若干行再按 userId 去重：历史上并发提交可能为同一用户写出多条
	// （表上无 (game_type,user_id) 唯一约束），直接 Limit(limit) 会让榜单出现重复用户。
	if err := h.db.Where("game_type = ? AND best_score > 0", gameType).
		Order(orderClause).
		Limit(limit * 4).
		Find(&records).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	// 结果已按名次方向排序，同一用户保留首次出现（即最好成绩）的那一行。
	deduped := make([]model.MinigameLeaderboard, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, r := range records {
		if _, ok := seen[r.UserID]; ok {
			continue
		}
		seen[r.UserID] = struct{}{}
		deduped = append(deduped, r)
		if len(deduped) >= limit {
			break
		}
	}
	records = deduped

	// username 已退役：排行榜显示名读时按 UserID 解析当前 displayName（覆盖遗留快照）。
	nameMap := make(map[string]string, len(records))
	var userIDs []string
	for _, r := range records {
		userIDs = append(userIDs, r.UserID)
	}
	if len(userIDs) > 0 {
		var users []model.User
		// 规则 1：显示名解析失败不再静默降级成空名字，直接 500，避免榜单出现无主成绩。
		if err := h.db.Select("id, display_name, username").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			errors.JSONError(c, errors.ErrInternal)
			return
		}
		for _, u := range users {
			nameMap[u.ID] = u.DisplayName
			if nameMap[u.ID] == "" {
				nameMap[u.ID] = u.Username
			}
		}
	}

	result := make([]map[string]interface{}, 0, len(records))
	for i, r := range records {
		name := nameMap[r.UserID]
		if name == "" {
			name = r.Username
		}
		result = append(result, map[string]interface{}{
			"rank":      i + 1,
			"userId":    r.UserID,
			"username":  name,
			"bestScore": r.BestScore,
			"updatedAt": r.UpdatedAt,
		})
	}

	dimension := ""
	if info, ok := getGameInfo(gameType); ok {
		dimension = info.ScoreDimension
	}
	errors.Success(c, gin.H{
		"gameType":  gameType,
		"dimension": dimension,
		"order":     order,
		"entries":   result,
	})
}

// GetMyScore handles GET /minigames/scores/me.
func (h *Handler) GetMyScore(c *gin.Context) {
	gameType := c.Query("gameType")
	if gameType == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("gameType is required"))
		return
	}

	userID := middleware.GetUserID(c)
	best, err := h.currentBestScore(gameType, userID)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if best == 0 {
		errors.Success(c, gin.H{"bestScore": 0})
		return
	}

	var record model.MinigameLeaderboard
	if err := h.db.Where("game_type = ? AND user_id = ?", gameType, userID).First(&record).Error; err != nil {
		// 规则 1：真库故障不能伪装成「没有成绩」。
		if err == gorm.ErrRecordNotFound {
			errors.Success(c, gin.H{"bestScore": 0})
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	errors.Success(c, gin.H{
		"bestScore": record.BestScore,
		"updatedAt": record.UpdatedAt,
	})
}
