package schedule

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
)

// A8-S1/S2（DES-20261001-01 §9.2-9.4）：日程事件邀请 RSVP。
//
// 契约要点（客户端已按此落库，不得偏离）：
//   - inviteeIds 为空/缺省 = 普通事件，行为与历史完全一致（旧请求体完全兼容）；
//   - 邀请校验：≤50、去重、必须为空间成员、不含创建者，失败返回 400
//     SCHEDULE_INVITE_INVALID；
//   - GET /events/:id/invites 可见者 = 创建者 + 受邀人（scope=admin_only 事件
//     维持 N4 口径，全局 OWNER/ADMIN 亦可见）；其余一律 404，不泄露存在性；
//   - PUT /events/:id/invite 仅受邀人本人可应答；非受邀人返回 404
//     SCHEDULE_NOT_FOUND（与「事件不存在」同码，不泄露事件存在性）；
//     应答幂等：重复同 status 返回 200（responded_at 会刷新）；
//   - WS schedule_invite_updated → 全部受邀人；schedule_invite_responded → 创建者。

// maxInvitees 单个事件最多邀请人数（A8-S1）。
const maxInvitees = 50

// InviteStatus 邀请状态枚举值。
const (
	InviteStatusPending  = "pending"
	InviteStatusAccepted = "accepted"
	InviteStatusDeclined = "declined"
)

// InviteInfo 是 GET invites / 编辑响应里返回的单条邀请视图。
type InviteInfo struct {
	UserID      string     `json:"userId"`
	DisplayName string     `json:"displayName"`
	Status      string     `json:"status"`
	RespondedAt *time.Time `json:"respondedAt"`
}

// normalizeInvitees 校验并规范化创建请求中的 inviteeIds（A8-S1）。
// 返回去重后的受邀人列表；无邀请（nil/空数组）返回空的空切片，事件按普通事件处理。
// 校验失败返回 SCHEDULE_INVITE_INVALID（400）。
func (h *Handler) normalizeInvitees(spaceID, creatorID string, raw []string) ([]string, error) {
	// 去重（保持首次出现顺序）
	seen := make(map[string]bool, len(raw))
	unique := make([]string, 0, len(raw))
	for _, id := range raw {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		// 无 inviteeIds 或空数组 = 普通事件，零变化（旧请求体完全兼容）
		return []string{}, nil
	}
	if len(unique) > maxInvitees {
		return nil, errors.New(errors.SCHEDULE_INVITE_INVALID, "inviteeIds supports at most 50 invitees")
	}
	for _, id := range unique {
		if id == creatorID {
			return nil, errors.New(errors.SCHEDULE_INVITE_INVALID, "creator must not be included in inviteeIds")
		}
	}
	// 必须全部为该空间成员
	var memberIDs []string
	if err := h.db.Model(&model.Membership{}).
		Where("space_id = ? AND user_id IN ?", spaceID, unique).
		Pluck("user_id", &memberIDs).Error; err != nil {
		return nil, err
	}
	if len(memberIDs) != len(unique) {
		return nil, errors.New(errors.SCHEDULE_INVITE_INVALID, "inviteeIds must all be space members")
	}
	return unique, nil
}

// createEventWithInvites 在事务内创建事件与 pending 邀请行（A8-S1）。
// inviteeIDs 为空时不建任何邀请行（普通事件路径）。
func (h *Handler) createEventWithInvites(event *model.ScheduleEvent, inviteeIDs []string) error {
	return h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(event).Error; err != nil {
			return err
		}
		for _, uid := range inviteeIDs {
			invite := model.ScheduleEventInvite{
				ID:      idgen.GenerateID(idgen.PrefixInvite),
				EventID: event.ID,
				UserID:  uid,
				Status:  InviteStatusPending,
			}
			if err := tx.Create(&invite).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// loadEventInvites 返回事件邀请行（按创建时间稳定排序）。
func (h *Handler) loadEventInvites(eventID string) ([]model.ScheduleEventInvite, error) {
	var invites []model.ScheduleEventInvite
	if err := h.db.Where("event_id = ?", eventID).
		Order("created_at ASC, id ASC").Find(&invites).Error; err != nil {
		return nil, err
	}
	return invites, nil
}

// buildInviteInfos 将邀请行组装为响应视图（displayName = 用户显示名，退回用户名）。
func (h *Handler) buildInviteInfos(invites []model.ScheduleEventInvite) []InviteInfo {
	infos := make([]InviteInfo, 0, len(invites))
	if len(invites) == 0 {
		return infos
	}
	ids := make([]string, 0, len(invites))
	for _, it := range invites {
		ids = append(ids, it.UserID)
	}
	var users []model.User
	nameMap := make(map[string]string)
	if err := h.db.Select("id, display_name, username").Where("id IN ?", ids).Find(&users).Error; err == nil {
		for _, u := range users {
			nameMap[u.ID] = u.DisplayName
			if nameMap[u.ID] == "" {
				nameMap[u.ID] = u.Username
			}
		}
	}
	for _, it := range invites {
		infos = append(infos, InviteInfo{
			UserID:      it.UserID,
			DisplayName: nameMap[it.UserID],
			Status:      it.Status,
			RespondedAt: it.RespondedAt,
		})
	}
	return infos
}

// canViewInvites 判定用户能否查看事件的邀请列表（A8-S1 契约）：
// 创建者 / 受邀人可看；scope=admin_only 事件维持 N4 口径（全局 OWNER/ADMIN 可见）。
func canViewInvites(event model.ScheduleEvent, userID, role string, isInvitee bool) bool {
	if event.CreatedBy == userID {
		return true
	}
	if isInvitee {
		return true
	}
	if event.Scope == "admin_only" && (role == middleware.RoleOwner || role == middleware.RoleAdmin) {
		return true
	}
	return false
}

// GetInvites 返回指定事件的邀请列表（A8-S1）。
// 可见者 = 创建者 + 受邀人（admin_only 事件维持 N4 口径）；其余 404 不泄露存在性。
func (h *Handler) GetInvites(c *gin.Context) {
	id := c.Param("id")

	var event model.ScheduleEvent
	if err := h.db.Where("id = ?", id).First(&event).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SCHEDULE_NOT_FOUND, "event not found"))
		return
	}

	userID := middleware.GetUserID(c)
	var inviteCount int64
	if err := h.db.Model(&model.ScheduleEventInvite{}).
		Where("event_id = ? AND user_id = ?", id, userID).
		Count(&inviteCount).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if !canViewInvites(event, userID, middleware.GetRole(c), inviteCount > 0) {
		errors.JSONError(c, errors.New(errors.SCHEDULE_NOT_FOUND, "event not found"))
		return
	}

	invites, err := h.loadEventInvites(id)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	errors.Success(c, gin.H{"invites": h.buildInviteInfos(invites)})
}

// RespondInvite 受邀人应答邀请（A8-S2）。
// 仅受邀人本人可调用（非受邀人 404，与「事件不存在」同码，不泄露存在性）；
// status 仅接受 accepted/declined（其余 400 SCHEDULE_INVITE_INVALID）；
// 幂等：重复同 status 返回 200；每次应答都刷新 responded_at。
func (h *Handler) RespondInvite(c *gin.Context) {
	id := c.Param("id")

	var body struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if body.Status != InviteStatusAccepted && body.Status != InviteStatusDeclined {
		errors.JSONError(c, errors.New(errors.SCHEDULE_INVITE_INVALID, "status must be one of: accepted, declined"))
		return
	}

	var event model.ScheduleEvent
	if err := h.db.Where("id = ?", id).First(&event).Error; err != nil {
		errors.JSONError(c, errors.New(errors.SCHEDULE_NOT_FOUND, "event not found"))
		return
	}

	userID := middleware.GetUserID(c)
	var invite model.ScheduleEventInvite
	if err := h.db.Where("event_id = ? AND user_id = ?", id, userID).
		First(&invite).Error; err != nil {
		// 非受邀人（或事件无该邀请）：404，不泄露事件存在性
		errors.JSONError(c, errors.New(errors.SCHEDULE_NOT_FOUND, "event not found"))
		return
	}

	now := time.Now().UTC()
	if err := h.db.Model(&model.ScheduleEventInvite{}).Where("id = ?", invite.ID).
		Updates(map[string]interface{}{
			"status":       body.Status,
			"responded_at": now,
		}).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	invite.Status = body.Status
	invite.RespondedAt = &now

	// A8-S2: 应答后通知创建者
	h.notifyInviteResponded(event, userID, body.Status)

	errors.Success(c, gin.H{
		"eventId":     event.ID,
		"userId":      invite.UserID,
		"status":      invite.Status,
		"respondedAt": invite.RespondedAt,
	})
}

// inviteUpdatedRecipients 计算 schedule_invite_updated 的收件人 = 全部受邀人
// （含已 declined 的受邀人，便于其看到事件被创建/编辑的最新状态）。
func inviteUpdatedRecipients(invites []model.ScheduleEventInvite) []string {
	ids := make([]string, 0, len(invites))
	seen := make(map[string]bool, len(invites))
	for _, it := range invites {
		if it.UserID == "" || seen[it.UserID] {
			continue
		}
		seen[it.UserID] = true
		ids = append(ids, it.UserID)
	}
	return ids
}

// buildInviteUpdatedMessage 组装 schedule_invite_updated WS 消息（A8-S2）。
func buildInviteUpdatedMessage(event model.ScheduleEvent, invites []model.ScheduleEventInvite) []byte {
	inv := make([]map[string]string, 0, len(invites))
	for _, it := range invites {
		inv = append(inv, map[string]string{"userId": it.UserID, "status": it.Status})
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type": "schedule_invite_updated",
		"payload": map[string]interface{}{
			"eventId":  event.ID,
			"title":    event.Title,
			"startsAt": event.StartTime,
			"invites":  inv,
		},
	})
	return data
}

// broadcastInviteUpdated 在事件带邀请被创建/编辑时向全部受邀人推送（A8-S2）。
func (h *Handler) broadcastInviteUpdated(event model.ScheduleEvent, invites []model.ScheduleEventInvite) {
	if h.hub == nil || len(invites) == 0 {
		return
	}
	h.hub.BroadcastToUsers(inviteUpdatedRecipients(invites), buildInviteUpdatedMessage(event, invites))
}

// buildInviteRespondedMessage 组装 schedule_invite_responded WS 消息（A8-S2）。
func buildInviteRespondedMessage(eventID, userID, status string) []byte {
	data, _ := json.Marshal(map[string]interface{}{
		"type": "schedule_invite_responded",
		"payload": map[string]interface{}{
			"eventId": eventID,
			"userId":  userID,
			"status":  status,
		},
	})
	return data
}

// inviteRespondedRecipients 计算 schedule_invite_responded 的收件人 = 事件创建者。
func inviteRespondedRecipients(event model.ScheduleEvent) []string {
	return []string{event.CreatedBy}
}

// notifyInviteResponded 在受邀人应答后向事件创建者推送（A8-S2）。
func (h *Handler) notifyInviteResponded(event model.ScheduleEvent, userID, status string) {
	if h.hub == nil {
		return
	}
	h.hub.BroadcastToUsers(inviteRespondedRecipients(event), buildInviteRespondedMessage(event.ID, userID, status))
}
