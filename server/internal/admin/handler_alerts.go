// Package admin — handler_alerts.go
//
// DES-20261001-01 §12.3「A11 管理后台监控告警」REST 面：
//   - GET  /api/admin/alerts?includeResolved=&limit=  告警列表（默认活跃 +
//     最近 50 条已恢复；includeResolved=false 只返回活跃）；
//   - POST /api/admin/alerts/:id/mute {hours}         静默同 type 告警推送，
//     写 admin_audit（LogAudit 模式）。
package admin

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

// 告警列表默认/上限参数（DES §12.3：默认活跃 + 最近 50 条 resolved）。
const (
	alertsDefaultResolvedLimit = 50
	alertsMaxResolvedLimit     = 200
	// mute 默认 24h（DES/前端「静默 24h」按钮），上限 720h（30 天）防误填。
	alertsMuteDefaultHours = 24
	alertsMuteMaxHours     = 720
)

// GetAlerts handles GET /api/admin/alerts.
//
// Query:
//   - includeResolved: "false" 只返回活跃告警；其余（缺省）返回活跃 + resolved 历史
//   - limit: resolved 历史条数上限（默认 50，封顶 200）
//
// Response data: {"active": [...], "resolved": [...], "activeCount": N}
// active 按 last_seen_at DESC（最刺眼的在前），resolved 按 last_seen_at DESC。
func (h *Handler) GetAlerts(c *gin.Context) {
	includeResolved := c.Query("includeResolved") != "false"

	limit := alertsDefaultResolvedLimit
	if v := c.Query("limit"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 {
			errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "limit 必须为正整数"))
			return
		}
		if parsed > alertsMaxResolvedLimit {
			parsed = alertsMaxResolvedLimit
		}
		limit = parsed
	}

	var active []model.AdminAlert
	if err := h.db.Where("resolved_at IS NULL").Order("last_seen_at DESC").Find(&active).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	resolved := []model.AdminAlert{}
	if includeResolved {
		if err := h.db.Where("resolved_at IS NOT NULL").
			Order("last_seen_at DESC").Limit(limit).Find(&resolved).Error; err != nil {
			errors.JSONError(c, errors.ErrInternal)
			return
		}
	}

	errors.Success(c, gin.H{
		"active":      active,
		"resolved":    resolved,
		"activeCount": len(active),
	})
}

// MuteAlert handles POST /api/admin/alerts/:id/mute.
//
// Body: {"hours": N}（缺省 24；1..720 之外拒绝）。静默按 type 生效（见
// AlertTypeMuted）：期间评估器照常写表，但同 type 不再向 admin WS 推送。
// 写操作记 admin 审计（与 update_config 等敏感操作同一通道）。
func (h *Handler) MuteAlert(c *gin.Context) {
	alertID := c.Param("id")

	var body struct {
		Hours *int `json:"hours"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "请求体必须是 {hours: N}"))
		return
	}
	hours := alertsMuteDefaultHours
	if body.Hours != nil {
		hours = *body.Hours
	}
	if hours < 1 || hours > alertsMuteMaxHours {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "hours 取值范围 1..720"))
		return
	}

	var row model.AdminAlert
	if err := h.db.First(&row, "id = ?", alertID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.SYSTEM_NOT_FOUND, "告警不存在"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	mutedUntil := time.Now().Add(time.Duration(hours) * time.Hour)
	row.MutedUntil = &mutedUntil
	if err := h.db.Save(&row).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	h.service.LogAudit(
		middleware.GetUserID(c), "alert_mute", alertID,
		"type="+row.Type+" hours="+strconv.Itoa(hours),
		c.ClientIP(), c.Request.UserAgent(), true,
	)

	errors.Success(c, gin.H{
		"alert":  row,
		"action": "muted",
	})
}
