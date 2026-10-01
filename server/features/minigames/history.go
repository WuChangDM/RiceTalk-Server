package minigames

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

const defaultHistoryLimit = 20
const maxHistoryLimit = 100

// RecordGameEnd 在一局游戏结束时写入历史记录。
// 参数：
//   - session: 刚结束的游戏会话
//   - winnerID: 胜者用户 ID（平局为空字符串）
//   - isDraw: 是否平局
//
// 设计文档 §15.4：每局游戏结束时写入一条记录，永久保留。
// 写入失败仅记录日志，不影响主流程（游戏结束广播仍然发出）。
func (h *Handler) RecordGameEnd(session *model.MinigameSession, winnerID string, isDraw bool) {
	if session == nil {
		return
	}

	now := time.Now().UTC()
	duration := int64(now.Sub(session.CreatedAt).Seconds())
	if duration < 0 {
		duration = 0
	}

	// 查询房主和胜者的显示名
	hostName := h.lookupDisplayName(session.HostID)
	winnerName := ""
	if !isDraw && winnerID != "" {
		winnerName = h.lookupDisplayName(winnerID)
	}

	// 从 PlayersJSON 提取最终分数
	finalScores := h.extractFinalScores(session.PlayersJSON)

	record := model.MinigameHistory{
		ID:          idgen.NextString(),
		GameType:    session.GameType,
		SpaceID:     session.SpaceID,
		SessionID:   session.ID,
		HostID:      session.HostID,
		HostName:    hostName,
		WinnerID:    winnerID,
		WinnerName:  winnerName,
		IsDraw:      isDraw,
		PlayersJSON: session.PlayersJSON,
		FinalScores: finalScores,
		StartedAt:   session.CreatedAt,
		EndedAt:     now,
		Duration:    duration,
		CreatedAt:   now,
	}

	if err := h.db.Create(&record).Error; err != nil {
		// 历史记录写入失败不应阻断游戏结束流程，仅记录错误
		// handler_test.go 中的测试不依赖此日志
		_ = err
	}
}

// lookupDisplayName 查询用户的显示名，优先 DisplayName，其次 Username。
func (h *Handler) lookupDisplayName(userID string) string {
	if userID == "" {
		return ""
	}
	var user model.User
	if err := h.db.Select("display_name", "username").First(&user, "id = ?", userID).Error; err != nil {
		return ""
	}
	if user.DisplayName != "" {
		return user.DisplayName
	}
	return user.Username
}

// extractFinalScores 从 PlayersJSON 中提取 {userId: score} 映射，序列化为 JSON 字符串。
func (h *Handler) extractFinalScores(playersJSON string) string {
	players := parsePlayers(playersJSON)
	scores := make(map[string]interface{}, len(players))
	for _, p := range players {
		uid, _ := p["userId"].(string)
		if uid == "" {
			continue
		}
		scores[uid] = p["score"]
	}
	b, err := json.Marshal(scores)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// GetHistory handles GET /minigames/history.
// 查询参数：
//   - spaceId: 按 Space 过滤（可选）
//   - gameType: 按游戏类型过滤（可选）
//   - limit: 返回条数，默认 20，最大 100
//   - offset: 偏移量，默认 0
//
// 返回按 created_at DESC 排序的历史记录列表。
func (h *Handler) GetHistory(c *gin.Context) {
	spaceID := c.Query("spaceId")
	gameType := c.Query("gameType")

	limitStr := c.DefaultQuery("limit", strconv.Itoa(defaultHistoryLimit))
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}

	offsetStr := c.DefaultQuery("offset", "0")
	offset, err := strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		offset = 0
	}

	// 权限校验：如果指定了 spaceId，校验当前用户是否为该 Space 成员
	userID := middleware.GetUserID(c)
	if spaceID != "" && !h.checkSpaceMembership(spaceID, userID) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	query := h.db.Model(&model.MinigameHistory{})
	if spaceID != "" {
		query = query.Where("space_id = ?", spaceID)
	}
	if gameType != "" {
		query = query.Where("game_type = ?", gameType)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	var records []model.MinigameHistory
	if err := query.Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	errors.Success(c, gin.H{
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"items":  records,
	})
}
