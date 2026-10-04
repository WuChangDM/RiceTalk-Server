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

	// 已知但不提供排行榜的游戏（多人联机游戏）拒绝提交（设计 §15.7.4）。
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

	record := model.MinigameLeaderboard{
		ID:        idgen.NextString(),
		GameType:  body.GameType,
		UserID:    userID,
		Username:  "",
		BestScore: body.Score,
	}

	// Upsert while keeping the best score for the game's ranking direction.
	var existing model.MinigameLeaderboard
	err := h.db.Where("game_type = ? AND user_id = ?", body.GameType, userID).First(&existing).Error
	updated := false
	if err == nil {
		if best(existing.BestScore, body.Score) {
			existing.BestScore = body.Score
			if saveErr := h.db.Save(&existing).Error; saveErr != nil {
				errors.JSONError(c, errors.ErrInternal)
				return
			}
			updated = true
		}
		record = existing
	} else {
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
	if err := h.db.Where("game_type = ? AND best_score > 0", gameType).
		Order(orderClause).
		Limit(limit).
		Find(&records).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

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
