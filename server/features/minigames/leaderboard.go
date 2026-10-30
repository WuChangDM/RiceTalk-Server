package minigames

import (
	"strconv"

	"github.com/gin-gonic/gin"

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
		if err := h.db.Where("game_type = ? AND user_id = ?", body.GameType, middleware.GetUserID(c)).
			First(&authoritativeRow).Error; err == nil {
			bestScore = authoritativeRow.BestScore
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

	// best returns whether candidate is an improvement over current.
	best := func(current, candidate int64) bool {
		if order == "asc" {
			return candidate < current
		}
		return candidate > current
	}

	// 先读已有记录：既用于单机分数校验的基线，也用于后续 upsert 决策。
	var existing model.MinigameLeaderboard
	err := h.db.Where("game_type = ? AND user_id = ?", body.GameType, userID).First(&existing).Error
	hasExisting := err == nil
	currentBest := int64(0)
	if hasExisting {
		currentBest = existing.BestScore
	}

	// D1：单机排行榜限分数上限 + 异常暴涨检测（阈值见 score.go 的 singlePlayerScoreCaps）。
	if info, ok := getGameInfo(body.GameType); ok && info.LocalOnly {
		if verr := validateSinglePlayerScore(info, body.Score, currentBest); verr != nil {
			errors.JSONError(c, verr)
			return
		}
	}

	record := existing
	updated := false
	if hasExisting {
		if best(existing.BestScore, body.Score) {
			record.BestScore = body.Score
			if saveErr := h.db.Save(&record).Error; saveErr != nil {
				errors.JSONError(c, errors.ErrInternal)
				return
			}
			updated = true
		}
	} else {
		record = model.MinigameLeaderboard{
			ID:        idgen.NextString(),
			GameType:  body.GameType,
			UserID:    userID,
			Username:  "",
			BestScore: body.Score,
		}
		if createErr := h.db.Create(&record).Error; createErr != nil {
			errors.JSONError(c, errors.ErrInternal)
			return
		}
		updated = true
	}

	errors.Success(c, gin.H{
		"bestScore": record.BestScore,
		"updated":   updated,
	})
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
		if err := h.db.Select("id, display_name, username").Where("id IN ?", userIDs).Find(&users).Error; err == nil {
			for _, u := range users {
				nameMap[u.ID] = u.DisplayName
				if nameMap[u.ID] == "" {
					nameMap[u.ID] = u.Username
				}
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
	var record model.MinigameLeaderboard
	if err := h.db.Where("game_type = ? AND user_id = ?", gameType, userID).First(&record).Error; err != nil {
		errors.Success(c, gin.H{"bestScore": 0})
		return
	}

	errors.Success(c, gin.H{
		"bestScore": record.BestScore,
		"updatedAt": record.UpdatedAt,
	})
}
