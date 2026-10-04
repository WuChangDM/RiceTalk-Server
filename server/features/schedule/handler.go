package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles schedule HTTP requests
type Handler struct {
	db  *gorm.DB
	cfg *config.Config
	hub *realtime.Hub
}

// NewHandler creates a new schedule handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{db: db, cfg: cfg, hub: hub}
}

// RegisterRoutes registers schedule routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	sc := r.Group("/schedule")
	sc.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		sc.GET("/events", h.GetEvents)
		sc.POST("/events", h.CreateEvent)
		sc.PUT("/events/:id", h.UpdateEvent)
		sc.DELETE("/events/:id", h.DeleteEvent)
		// A8-S1/S2: 事件邀请 RSVP
		sc.GET("/events/:id/invites", h.GetInvites)
		sc.PUT("/events/:id/invite", h.RespondInvite)
	}
}

// EventResponse is the frontend-friendly format
type EventResponse struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Date            string `json:"date"`
	Time            string `json:"time"`
	Location        string `json:"location,omitempty"`
	Description     string `json:"description,omitempty"`
	Creator         string `json:"creator"`
	ReminderMinutes int    `json:"reminderMinutes"`
}

func formatEvent(e model.ScheduleEvent, username string) EventResponse {
	dateStr := e.StartTime.Format("2006-01-02")
	timeStr := e.StartTime.Format("15:04")
	if e.AllDay {
		timeStr = "全天"
	}
	return EventResponse{
		ID:              e.ID,
		Title:           e.Title,
		Date:            dateStr,
		Time:            timeStr,
		Location:        e.Location,
		Description:     e.Description,
		Creator:         username,
		ReminderMinutes: e.ReminderMinutes,
	}
}

// GetEvents returns all schedule events
// L25: 支持按日期范围查询 ?startDate=2026-06-01&endDate=2026-06-30
// M24: 非 admin 用户只能看到 scope=all 的日程；admin 可看到所有日程
func (h *Handler) GetEvents(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	var events []model.ScheduleEvent
	query := h.db.Where("space_id = ?", spaceID)
	if startDate := c.Query("startDate"); startDate != "" {
		if endDate := c.Query("endDate"); endDate != "" {
			query = query.Where("event_date BETWEEN ? AND ?", startDate, endDate)
		}
	}
	// M24: scope 权限过滤 - 非 admin 只能看 scope=all
	role := middleware.GetRole(c)
	if role != middleware.RoleOwner && role != middleware.RoleAdmin {
		query = query.Where("scope = ?", "all")
	}
	if err := query.Find(&events).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// Batch query usernames for all events (avoid N+1)
	userIDs := make([]string, 0, len(events))
	for _, e := range events {
		userIDs = append(userIDs, e.CreatedBy)
	}
	var users []model.User
	usernameMap := make(map[string]string)
	if len(userIDs) > 0 {
		if err := h.db.Select("id, display_name, username").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			errors.JSONError(c, errors.ErrInternal)
			return
		}
		for _, u := range users {
			usernameMap[u.ID] = u.DisplayName
			if usernameMap[u.ID] == "" {
				usernameMap[u.ID] = u.Username
			}
		}
	}

	result := make([]EventResponse, 0, len(events))
	for _, e := range events {
		result = append(result, formatEvent(e, usernameMap[e.CreatedBy]))
	}
	errors.Success(c, result)
}

// CreateEvent creates a new schedule event
func (h *Handler) CreateEvent(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	var body struct {
		Title           string    `json:"title"`
		Description     string    `json:"description"`
		Location        string    `json:"location"`
		StartTime       time.Time `json:"startTime"`
		EndTime         time.Time `json:"endTime"`
		AllDay          bool      `json:"allDay"`
		Scope           string    `json:"scope"`           // M24: all / admin_only
		ReminderMinutes int       `json:"reminderMinutes"` // L26: 提前提醒分钟数（0=不提醒）
		InviteeIDs      []string  `json:"inviteeIds"`      // A8-S1: 受邀人（空/缺省=普通事件，行为不变）
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if strings.TrimSpace(body.Title) == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("title is required"))
		return
	}

	// M24: scope 枚举校验，默认 all
	scope := body.Scope
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "admin_only" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid scope, must be one of: all, admin_only"))
		return
	}

	// L26: reminderMinutes 校验（0-1440，最多提前24小时）
	if body.ReminderMinutes < 0 || body.ReminderMinutes > 1440 {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("reminderMinutes must be between 0 and 1440"))
		return
	}

	endTime := body.EndTime
	if endTime.IsZero() {
		endTime = body.StartTime.Add(time.Hour)
	}

	// L25: 从 StartTime 提取日期部分填充 EventDate
	eventDate := time.Date(body.StartTime.Year(), body.StartTime.Month(), body.StartTime.Day(), 0, 0, 0, 0, time.UTC)

	// A8-S1: 校验并规范化受邀人（≤50、去重、空间成员、不含创建者；空=普通事件）
	userID := middleware.GetUserID(c)
	inviteeIDs, err := h.normalizeInvitees(spaceID, userID, body.InviteeIDs)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	event := model.ScheduleEvent{
		ID:              idgen.GenerateID(idgen.PrefixEvent),
		SpaceID:         spaceID,
		Title:           body.Title,
		Description:     body.Description,
		Location:        body.Location,
		EventDate:       eventDate,
		StartTime:       body.StartTime,
		EndTime:         endTime,
		AllDay:          body.AllDay,
		Scope:           scope, // M24
		ReminderMinutes: body.ReminderMinutes, // L26
		InviteeIDs:      inviteeIDs,           // A8-S1: 受邀人快照（始终非 nil，空数组=普通事件）
		CreatedBy:       userID,
	}
	if err := h.createEventWithInvites(&event, inviteeIDs); err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// C37: 广播 schedule_event_created（N4: 按 scope 定向发送，不再全局广播）
	h.broadcastToScopeRecipients(event, "schedule_event_created", map[string]interface{}{
		"id":              event.ID,
		"title":           event.Title,
		"startTime":       event.StartTime,
		"endTime":         event.EndTime,
		"allDay":          event.AllDay,
		"location":        event.Location,
		"createdBy":       event.CreatedBy,
		"reminderMinutes": event.ReminderMinutes, // L26
	})

	// A8-S2: 事件带邀请创建时，向全部受邀人推送 schedule_invite_updated
	if len(inviteeIDs) > 0 {
		if invites, err := h.loadEventInvites(event.ID); err == nil {
			h.broadcastInviteUpdated(event, invites)
		}
	}

	errors.Success(c, event)
}

// UpdateEvent updates a schedule event
// N5: 请求体字段全部使用指针，仅更新显式传入的字段；未传字段保持原值
// （「显式传空串」与「未传」可区分，如 location:"" 表示清空地点）
func (h *Handler) UpdateEvent(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Title           *string    `json:"title"`
		Description     *string    `json:"description"`
		Location        *string    `json:"location"`
		StartTime       *time.Time `json:"startTime"`
		EndTime         *time.Time `json:"endTime"`
		AllDay          *bool      `json:"allDay"`
		Scope           *string    `json:"scope"`           // M24: all / admin_only
		ReminderMinutes *int       `json:"reminderMinutes"` // L26: 提前提醒分钟数（指针类型，区分未传入和0）
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// N5: title 显式传入时不允许为空（与 CreateEvent 校验口径一致）
	if body.Title != nil && strings.TrimSpace(*body.Title) == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("title cannot be empty"))
		return
	}

	// M24: scope 枚举校验（如果传入）
	if body.Scope != nil && *body.Scope != "all" && *body.Scope != "admin_only" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid scope, must be one of: all, admin_only"))
		return
	}

	// L26: reminderMinutes 校验（如果传入）
	if body.ReminderMinutes != nil {
		if *body.ReminderMinutes < 0 || *body.ReminderMinutes > 1440 {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("reminderMinutes must be between 0 and 1440"))
			return
		}
	}

	// N5: startTime 显式传入时不允许为零值（避免把 EventDate/StartTime 清成零值）
	if body.StartTime != nil && body.StartTime.IsZero() {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("startTime is invalid"))
		return
	}

	var event model.ScheduleEvent
	if err := h.db.Where("id = ?", id).First(&event).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SCHEDULE_NOT_FOUND, "event not found"))
		return
	}

	userID := middleware.GetUserID(c)
	if event.CreatedBy != userID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.New(errors.SCHEDULE_PERMISSION_DENIED, "no permission to operate this event"))
		return
	}

	updates := map[string]interface{}{}
	if body.Title != nil {
		updates["title"] = *body.Title
	}
	if body.Description != nil {
		updates["description"] = *body.Description
	}
	if body.Location != nil {
		updates["location"] = *body.Location
	}
	if body.StartTime != nil {
		updates["start_time"] = *body.StartTime
		// L25: 如果更新了 StartTime，同步更新 EventDate
		updates["event_date"] = time.Date(body.StartTime.Year(), body.StartTime.Month(), body.StartTime.Day(), 0, 0, 0, 0, time.UTC)
	}
	if body.EndTime != nil {
		updates["end_time"] = *body.EndTime
	}
	if body.AllDay != nil {
		updates["all_day"] = *body.AllDay
	}
	// M24: 如果更新了 scope
	if body.Scope != nil {
		updates["scope"] = *body.Scope
	}
	// L26: 如果更新了 reminderMinutes，重置 reminder_sent
	if body.ReminderMinutes != nil {
		updates["reminder_minutes"] = *body.ReminderMinutes
		updates["reminder_sent"] = false
	}
	if len(updates) == 0 {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("no updatable fields provided"))
		return
	}
	if err := h.db.Model(&model.ScheduleEvent{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// N5: 重读更新后的事件，广播最终完整值（部分更新时客户端也能拿到全量最新数据）
	// N4: 按 scope 定向发送（若本次把 scope 改为 admin_only，则按新口径只通知管理员）
	var invites []model.ScheduleEventInvite
	if existing, err := h.loadEventInvites(id); err == nil {
		invites = existing
	}
	if err := h.db.Where("id = ?", id).First(&event).Error; err == nil {
		h.broadcastToScopeRecipients(event, "schedule_event_updated", map[string]interface{}{
			"id":              event.ID,
			"title":           event.Title,
			"startTime":       event.StartTime,
			"endTime":         event.EndTime,
			"allDay":          event.AllDay,
			"location":        event.Location,
			"createdBy":       event.CreatedBy,
			"reminderMinutes": event.ReminderMinutes,
		})
		// A8-S2: 事件带邀请被编辑（标题/时间等影响受邀人视图），向全部受邀人推送
		h.broadcastInviteUpdated(event, invites)
	}

	resp := gin.H{"message": "updated"}
	// A8-S1: 响应体带 invites（若该事件有；无邀请事件保持原响应形状不变）
	if len(invites) > 0 {
		resp["invites"] = h.buildInviteInfos(invites)
	}
	errors.Success(c, resp)
}

// DeleteEvent deletes a schedule event
func (h *Handler) DeleteEvent(c *gin.Context) {
	id := c.Param("id")

	var event model.ScheduleEvent
	if err := h.db.Where("id = ?", id).First(&event).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SCHEDULE_NOT_FOUND, "event not found"))
		return
	}

	userID := middleware.GetUserID(c)
	if event.CreatedBy != userID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.New(errors.SCHEDULE_PERMISSION_DENIED, "no permission to operate this event"))
		return
	}

	if err := h.db.Delete(&model.ScheduleEvent{}, "id = ?", id).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// A8-S1: 级联清理邀请行（迁移 000039 不加外键，由业务层保证删除）
	if err := h.db.Where("event_id = ?", id).Delete(&model.ScheduleEventInvite{}).Error; err != nil {
		fmt.Printf("failed to delete schedule invites: event_id=%s error=%v\n", id, err)
	}

	// C37: 广播 schedule_event_deleted（N4: 按 scope 定向发送，不再全局广播）
	h.broadcastToScopeRecipients(event, "schedule_event_deleted", map[string]interface{}{
		"id":        id,
		"createdBy": event.CreatedBy,
	})

	errors.Success(c, gin.H{"message": "deleted"})
}

// ===== L26: 日程提醒功能 =====

// reminderGraceWindow 提醒补发宽限窗（N4 附加项）：
// 提醒时间已过但未超过该窗口时才补发；超过窗口的（如服务器长时间停机后重启）
// 属于陈旧提醒，不再补发，仅静默标记 reminder_sent 防止重复扫描。
const reminderGraceWindow = 15 * time.Minute

// reminderRecipients 计算对指定日程事件有可见权的收件人列表（N4 修复）。
// 口径与 GetEvents 的 scope 过滤保持一致：
//   - scope=all（空间事件）→ 该空间的全部成员（排除已停用用户）
//   - scope=admin_only（受限事件）→ 全局 OWNER/ADMIN 用户（排除已停用用户）
//
// A8-S2 扩展：在 scope 口径基础上并集「受邀人中 status != declined 的用户」
// （同样排除停用账号）。admin_only 事件的受邀人本就在 N4 口径下可见才被邀请；
// 已 declined 的受邀人不再接收提醒，但 scope 口径原有的收件人不受影响。
func (h *Handler) reminderRecipients(event model.ScheduleEvent) ([]string, error) {
	ids := []string{}
	seen := make(map[string]bool)
	add := func(list []string) {
		for _, id := range list {
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if event.Scope == "admin_only" {
		var scopeIDs []string
		if err := h.db.Model(&model.User{}).
			Where("role IN ? AND is_active = ? AND deleted_at IS NULL",
				[]string{middleware.RoleOwner, middleware.RoleAdmin}, true).
			Pluck("id", &scopeIDs).Error; err != nil {
			return nil, err
		}
		add(scopeIDs)
	} else {
		// scope=all：空间全部成员（join users 排除已停用账号）
		var memberIDs []string
		if err := h.db.Model(&model.Membership{}).
			Joins("JOIN users ON users.id = memberships.user_id AND users.is_active = ? AND users.deleted_at IS NULL", true).
			Where("memberships.space_id = ?", event.SpaceID).
			Pluck("memberships.user_id", &memberIDs).Error; err != nil {
			return nil, err
		}
		add(memberIDs)
	}

	// A8-S2: 并集受邀人（status != declined，排除已停用账号）
	var inviteeIDs []string
	if err := h.db.Model(&model.ScheduleEventInvite{}).
		Joins("JOIN users ON users.id = schedule_event_invites.user_id AND users.is_active = ? AND users.deleted_at IS NULL", true).
		Where("schedule_event_invites.event_id = ? AND schedule_event_invites.status <> ?", event.ID, InviteStatusDeclined).
		Pluck("schedule_event_invites.user_id", &inviteeIDs).Error; err != nil {
		return nil, err
	}
	add(inviteeIDs)
	return ids, nil
}

// broadcastToScopeRecipients 将日程事件消息定向发送给按事件 scope 有可见权的用户（N4 修复）。
// 取代原先的 h.hub.Broadcast 全局广播，避免 admin_only 事件的标题/地点泄露给全员。
func (h *Handler) broadcastToScopeRecipients(event model.ScheduleEvent, msgType string, payload map[string]interface{}) {
	if h.hub == nil {
		return
	}
	recipients, err := h.reminderRecipients(event)
	if err != nil {
		fmt.Printf("failed to resolve schedule recipients: event_id=%s type=%s error=%v\n", event.ID, msgType, err)
		return
	}
	data, _ := json.Marshal(map[string]interface{}{"type": msgType, "payload": payload})
	for _, uid := range recipients {
		h.hub.BroadcastToUser(uid, data)
	}
}

// shouldSendReminder 判断当前时刻是否应补发提醒（L26 + N4 附加宽限窗）：
// 已过提醒时间且落在宽限窗内才发送，超窗的陈旧提醒不补发。
func shouldSendReminder(now, reminderTime time.Time) bool {
	if !now.After(reminderTime) {
		return false
	}
	return now.Sub(reminderTime) <= reminderGraceWindow
}

// StartReminderLoop starts a background goroutine to scan and send reminders (L26)
// 应在 main.go 启动时调用，传入可取消的 context
func (h *Handler) StartReminderLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.scanAndSendReminders()
		}
	}
}

// scanAndSendReminders scans for upcoming events that need reminders (L26)
// 查询未来24小时内需要提醒且未发送的日程，到达提醒时间则向可见用户定向发送提醒事件；
// 超过宽限窗的陈旧提醒不补发，仅标记 reminder_sent（N4 附加项）
func (h *Handler) scanAndSendReminders() {
	now := time.Now().UTC()
	// 查询未来24小时内需要提醒且未发送的日程
	var events []model.ScheduleEvent
	if err := h.db.Where(
		"reminder_minutes > 0 AND reminder_sent = ? AND start_time > ? AND start_time <= ?",
		false, now, now.Add(24*time.Hour),
	).Find(&events).Error; err != nil {
		return
	}

	for _, event := range events {
		reminderTime := event.StartTime.Add(-time.Duration(event.ReminderMinutes) * time.Minute)
		if now.After(reminderTime) {
			// N4 附加项：超过宽限窗的陈旧提醒（如长时间停机后重启）不补发，
			// 仅标记 reminder_sent 防止后续扫描重复处理
			if !shouldSendReminder(now, reminderTime) {
				if err := h.db.Model(&model.ScheduleEvent{}).Where("id = ?", event.ID).Update("reminder_sent", true).Error; err != nil {
					fmt.Printf("failed to mark stale reminder sent: event_id=%s error=%v\n", event.ID, err)
				}
				continue
			}
			// 发送提醒（N4: 定向发送给有可见权的用户）
			h.sendReminder(event)
			// 标记已发送（防重复）
			if err := h.db.Model(&model.ScheduleEvent{}).Where("id = ?", event.ID).Update("reminder_sent", true).Error; err != nil {
				fmt.Printf("failed to mark reminder sent: event_id=%s error=%v\n", event.ID, err)
			}
		}
	}
}

// sendReminder sends a schedule_reminder event to users allowed to see the event (L26 + N4)
func (h *Handler) sendReminder(event model.ScheduleEvent) {
	h.broadcastToScopeRecipients(event, "schedule_reminder", map[string]interface{}{
		"id":              event.ID,
		"title":           event.Title,
		"startTime":       event.StartTime,
		"location":        event.Location,
		"reminderMinutes": event.ReminderMinutes,
	})
}
