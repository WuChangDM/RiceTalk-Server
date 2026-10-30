package message

import (
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/httpbind"
	"ridgericetalk/internal/channel"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles HTTP requests for messages
// MessageBroadcaster 是 message handler 使用的 WebSocket 广播能力。
// 抽成接口的目的：测试可注入 recording fake，从而断言投递给谁
// （A9 二期 R1：线程未读归零只发本人、增量作者跳过）。
type MessageBroadcaster interface {
	BroadcastToChannel(channelID string, data []byte)
	BroadcastToChannelExcept(channelID, excludeUserID string, data []byte)
	BroadcastToUser(userID string, data []byte)
}

type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     MessageBroadcaster
}

// NewHandler creates a new message handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub, svc *Service) *Handler {
	if svc == nil {
		svc = NewService(db)
	}
	// 注意：typed nil（*realtime.Hub）转接口后不再是 nil，必须显式判空后再赋值。
	var broadcaster MessageBroadcaster
	if hub != nil {
		broadcaster = hub
	}
	return &Handler{service: svc, db: db, cfg: cfg, hub: broadcaster}
}

// RegisterRoutes registers message routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	// Channel messages (require channel-level permission)
	chanPerm := middleware.ChannelPermission(h.db)
	r.GET("/channels/:id/messages", middleware.AuthRequired(h.cfg, h.db), chanPerm, h.GetMessages)
	r.POST("/channels/:id/messages", middleware.AuthRequired(h.cfg, h.db), chanPerm, h.CreateMessage)
	// 消息置顶/取消置顶（复用频道级 pinned_message_id 设计）
	r.POST("/channels/:id/messages/:messageId/pin", middleware.AuthRequired(h.cfg, h.db), chanPerm, h.PinMessage)
	r.GET("/channels/:id/unread", middleware.AuthRequired(h.cfg, h.db), chanPerm, h.GetUnreadCount)
	// M16: 新路由 mark-read（设计文档 §8.4A 规范）
	r.POST("/channels/:id/mark-read", middleware.AuthRequired(h.cfg, h.db), chanPerm, h.MarkChannelRead)
	// M16: 旧路由 read 保留用于向后兼容，后续版本将移除
	r.POST("/channels/:id/read", middleware.AuthRequired(h.cfg, h.db), chanPerm, h.MarkChannelRead)

	// Message search (restored for functional test coverage)
	r.GET("/channels/:id/messages/search", middleware.AuthRequired(h.cfg, h.db), chanPerm, h.SearchMessages)
	// Global message search is restricted to channels visible to the caller.
	r.GET("/messages/search", middleware.AuthRequired(h.cfg, h.db), h.SearchAllMessages)

	// Message operations (edit/delete removed per PRD §5.1)
	r.POST("/messages/:id/reactions", middleware.AuthRequired(h.cfg, h.db), h.AddReaction)
	r.DELETE("/messages/:id/reactions", middleware.AuthRequired(h.cfg, h.db), h.RemoveReaction)
	r.GET("/messages/:id/thread", middleware.AuthRequired(h.cfg, h.db), h.GetThread)
	// 注意：本路由的频道权限在 GetThread handler 内部判定（由父消息 JOIN 反查频道）；
	// 不要给它挂 ChannelPermission —— :id 是 messageId，会被当频道 ID 查询导致全 404。
	r.POST("/messages/:id/ack", middleware.AuthRequired(h.cfg, h.db), h.AckMessage)
	r.GET("/channel-files/:id", middleware.AuthRequired(h.cfg, h.db), h.DownloadAttachment)
}

// GetMessages retrieves messages for a channel.
// Optional query param `around=<messageId>` returns one page centred on that
// message (inclusive) for search jump-to-message; without it the behaviour is
// unchanged (cursor pagination from the latest message).
func (h *Handler) GetMessages(c *gin.Context) {
	channelID := c.Param("id")
	cursor := c.Query("cursor")
	around := c.Query("around")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	var messages []model.Message
	var hasMore bool
	var err error
	// A9 二期：around 命中线程回复时，服务端改为以父消息为中心取页，并把
	// threadParentId / focusMessageId 回报给客户端（页内不含该回复本身）。
	threadParentID, focusMessageID := "", ""
	if around != "" {
		info, aroundErr := h.service.AroundMessages(channelID, around, limit)
		if aroundErr != nil {
			errors.JSONError(c, aroundErr)
			return
		}
		messages, hasMore = info.Messages, info.HasMore
		threadParentID, focusMessageID = info.ThreadParentID, info.FocusMessageID
	} else {
		messages, hasMore, err = h.service.GetMessages(channelID, cursor, limit)
	}
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Build enriched response with author objects
	type authorInfo struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Avatar      string `json:"avatar"`
	}

	type replyToInfo struct {
		ID          string    `json:"id"`
		Username    string    `json:"username"`
		DisplayName string    `json:"displayName,omitempty"` // C-P1-6：回复条作者名优先用 displayName
		Content     string    `json:"content"`
		CreatedAt   time.Time `json:"createdAt"`
	}

	type messageItem struct {
		model.Message
		Author  authorInfo   `json:"author"`
		ReplyTo *replyToInfo `json:"replyTo,omitempty"`
		// A9 二期：该消息的直接回复数（一次 GROUP BY 批量回填，禁 N+1）。
		ThreadReplyCount int64 `json:"threadReplyCount"`
	}

	// Collect parent message IDs for batch lookup
	parentIDs := make(map[string]struct{})
	for _, m := range messages {
		if m.ParentID != nil && *m.ParentID != "" {
			parentIDs[*m.ParentID] = struct{}{}
		}
	}

	// Batch query parent messages
	parentMap := make(map[string]*model.Message)
	if len(parentIDs) > 0 {
		ids := make([]string, 0, len(parentIDs))
		for id := range parentIDs {
			ids = append(ids, id)
		}
		var parents []model.Message
		if err := h.db.Where("id IN ?", ids).Find(&parents).Error; err == nil {
			for i := range parents {
				parentMap[parents[i].ID] = &parents[i]
			}
		}
	}

	// A9 二期：批量回填 threadReplyCount（本页消息 ID 一次 GROUP BY）。
	replyCounts := map[string]int64{}
	if len(messages) > 0 {
		ids := make([]string, 0, len(messages))
		for _, m := range messages {
			ids = append(ids, m.ID)
		}
		if counts, cerr := h.service.ThreadReplyCounts(ids); cerr == nil {
			replyCounts = counts
		}
	}

	items := make([]messageItem, len(messages))
	for i, m := range messages {
		items[i].Message = m
		items[i].ThreadReplyCount = replyCounts[m.ID]
		items[i].Author = authorInfo{
			ID:          m.UserID,
			Username:    m.AuthorUsername,
			DisplayName: m.AuthorDisplayName,
			Avatar:      m.AuthorAvatarURL,
		}
		if m.ParentID != nil && *m.ParentID != "" {
			if parent, ok := parentMap[*m.ParentID]; ok {
				content := parent.Content
				if len(content) > 100 {
					content = content[:100] + "…"
				}
				items[i].ReplyTo = &replyToInfo{
					ID:          parent.ID,
					Username:    parent.AuthorUsername,
					DisplayName: parent.AuthorDisplayName,
					Content:     content,
					CreatedAt:   parent.CreatedAt,
				}
			}
		}
	}

	resp := gin.H{
		"items":   items,
		"hasMore": hasMore,
		"cursor":  cursor,
	}
	if threadParentID != "" {
		resp["threadParentId"] = threadParentID
	}
	if focusMessageID != "" {
		resp["focusMessageId"] = focusMessageID
	}
	errors.Success(c, resp)
}

// CreateMessage creates a new message in a channel.
// Accepts JSON (legacy: {content, clientMessageId}) or multipart/form-data
// (with optional "attachments" file field and "content"/"clientMessageId" form fields).
// 消息转发（一期）可选字段：forwardFrom（转发来源作者显示名快照，≤64 字符）与
// attachmentIds（JSON 数组 / multipart 逗号分隔，复制既有附件引用，物理文件零拷贝）。
func (h *Handler) CreateMessage(c *gin.Context) {
	channelID := c.Param("id")
	userID := middleware.GetUserID(c)
	username := middleware.GetUsername(c)
	role := middleware.GetRole(c)

	var content string
	var clientMessageID string
	var parentID string
	var forwardFrom string
	var attachmentIDs []string
	var files []*multipart.FileHeader

	contentType := c.GetHeader("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		content = c.PostForm("content")
		clientMessageID = c.PostForm("clientMessageId")
		parentID = c.PostForm("parentId")
		// 消息转发（一期）：multipart 分支同样接受 forwardFrom 与逗号分隔的
		// attachmentIds（复制既有附件引用，不要求携带新文件）。
		forwardFrom = c.PostForm("forwardFrom")
		if raw := strings.TrimSpace(c.PostForm("attachmentIds")); raw != "" {
			for _, id := range strings.Split(raw, ",") {
				if id = strings.TrimSpace(id); id != "" {
					attachmentIDs = append(attachmentIDs, id)
				}
			}
		}
		form, err := c.MultipartForm()
		if err == nil && form != nil {
			files = form.File["attachments"]
		}
	} else {
		var body struct {
			Content         string   `json:"content"`
			ClientMessageID string   `json:"clientMessageId"`
			ParentID        *string  `json:"parentId"`
			ForwardFrom     string   `json:"forwardFrom"`   // 转发来源作者显示名快照（可空，≤64 字符）
			AttachmentIDs   []string `json:"attachmentIds"` // 转发复制的源附件 ID 列表（可空）
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			errors.JSONError(c, errors.ErrBadRequest)
			return
		}
		content = body.Content
		clientMessageID = body.ClientMessageID
		if body.ParentID != nil {
			parentID = *body.ParentID
		}
		forwardFrom = body.ForwardFrom
		attachmentIDs = body.AttachmentIDs
	}

	msg, err := h.service.CreateMessage(channelID, userID, username, role, content, clientMessageID, parentID, forwardFrom, files, attachmentIDs)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast to channel subscribers via WebSocket
	if h.hub != nil {
		payload := map[string]interface{}{
			"id":              msg.ID,
			"channelId":       msg.ChannelID,
			"userId":          msg.UserID,
			"username":        msg.AuthorUsername,
			"displayName":     msg.AuthorDisplayName,
			"avatarUrl":       msg.AuthorAvatarURL,
			"role":            msg.AuthorRole,
			"content":         msg.Content,
			"type":            msg.Type,
			"parentId":        msg.ParentID,
			"forwardFrom":     msg.ForwardFrom, // 转发来源显示名快照（空串 = 非转发）
			"clientMessageId": msg.ClientMessageID,
			"attachments":     msg.Attachments,
			"createdAt":       msg.CreatedAt,
		}
		data, err := json.Marshal(map[string]interface{}{
			"type":    "message",
			"payload": payload,
		})
		if err != nil {
			// Marshal failure is non-critical; log and continue.
			// The message is already persisted.
		} else {
			h.hub.BroadcastToChannel(channelID, data)
		}
	}

	// A9 二期 R1：线程回复 -> 向频道订阅者增量推送 thread_unread_changed（delta=1），
	// 作者本人跳过不发（服务端不投递，而非让客户端忽略）。
	if h.hub != nil && msg.ParentID != nil && *msg.ParentID != "" {
		if data, marshalErr := json.Marshal(map[string]interface{}{
			"type":    "thread_unread_changed",
			"payload": threadUnreadDeltaPayload(msg.ChannelID, *msg.ParentID, msg.ID, msg.UserID),
		}); marshalErr == nil {
			h.hub.BroadcastToChannelExcept(msg.ChannelID, msg.UserID, data)
		}
	}

	// @提及：定向推送 mention_notification 给被 @ 的用户，并累加其未读提及数。
	// 对齐 Discord/Slack：提及绑定用户 ID（<@user_xxx>），改名后历史提及仍有效。
	if len(msg.MentionUserIDs) > 0 {
		h.sendMentionNotifications(msg)
	}

	errors.Success(c, msg)
}

// sendMentionNotifications pushes a mention_notification to every mentioned user
// and bumps their UserChannelRead.MentionCount (the unread mention counter).
func (h *Handler) sendMentionNotifications(msg *model.Message) {
	if h.hub == nil || len(msg.MentionUserIDs) == 0 {
		return
	}
	// Snapshot a short content preview for the notification toast.
	preview := msg.Content
	if len(preview) > 80 {
		preview = preview[:80] + "…"
	}
	fromDisplay := msg.AuthorDisplayName
	if fromDisplay == "" {
		fromDisplay = msg.AuthorUsername
	}
	for userID := range msg.MentionUserIDs {
		if userID == msg.UserID {
			continue // don't notify yourself
		}
		// Bump unread mention count (incremental upsert).
		if err := h.db.Model(&model.UserChannelRead{}).
			Where("user_id = ? AND channel_id = ?", userID, msg.ChannelID).
			Update("mention_count", gorm.Expr("mention_count + 1")).Error; err == nil {
			// no-op on missing row (user hasn't read this channel yet); fine
		}
		// Push real-time notification.
		payload := map[string]interface{}{
			"fromUserId":   msg.UserID,
			"fromUsername": fromDisplay,
			"channelId":    msg.ChannelID,
			"messageId":    msg.ID,
			"context":      preview,
			"userId":       userID,
		}
		data, err := json.Marshal(map[string]interface{}{
			"type":    "mention_notification",
			"payload": payload,
		})
		if err != nil {
			continue
		}
		h.hub.BroadcastToUser(userID, data)
	}
}

// DownloadAttachment serves a message attachment (or its thumbnail).
func (h *Handler) DownloadAttachment(c *gin.Context) {
	attachmentID := c.Param("id")
	userID := middleware.GetUserID(c)
	thumb := c.Query("thumb") == "1"

	// R-03：先做「附件 → 消息 → 频道」权限校验（?thumb=1 与正式下载同一受控路径），
	// 未通过则绝不进入 service 读磁盘文件。
	if !h.authorizeAttachmentAccess(c, attachmentID) {
		return
	}

	physicalPath, filename, contentType, err := h.service.DownloadAttachment(attachmentID, userID, thumb)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", filename))
	// 明确声明支持 Range 请求，使浏览器/视频播放器可以分段加载大视频/图片
	c.Header("Accept-Ranges", "bytes")
	c.File(physicalPath)
}

// PinMessage pins or unpins a message in a channel and broadcasts a message_pin event.
// 请求体可选 {"pinned": bool}，缺省为 true（置顶）。
func (h *Handler) PinMessage(c *gin.Context) {
	channelID := c.Param("id")
	messageID := c.Param("messageId")
	userID := middleware.GetUserID(c)

	var body struct {
		Pinned *bool `json:"pinned"`
	}
	// SEC-001: 允许空请求体（缺省置顶 true），但拒绝非法 JSON
	if err := httpbind.BindJSONAllowEmpty(c, &body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid JSON body: "+err.Error()))
		return
	}
	pinned := true
	if body.Pinned != nil {
		pinned = *body.Pinned
	}

	if err := h.service.PinMessage(channelID, messageID, userID, pinned); err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast message_pin event to channel subscribers via WebSocket
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "message_pin",
			"payload": map[string]interface{}{
				"channelId": channelID,
				"messageId": messageID,
				"pinned":    pinned,
				"pinnedBy":  userID,
			},
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
		h.hub.BroadcastToChannel(channelID, data)
	}

	errors.Success(c, gin.H{"message": "ok", "pinned": pinned})
}

// messageChannelAccessRow 是一次 JOIN（messages → channels → 角色权限）的结果。
// R-01（表情回应）复用该结构，禁止「先查消息再查频道」的两次拼接（防跨频道拼接/TOCTOU）。
type messageChannelAccessRow struct {
	ID          string `gorm:"column:id"`
	ChannelID   string `gorm:"column:channel_id"`
	ChannelType string `gorm:"column:channel_type"`
	Visibility  string `gorm:"column:visibility"`
	CanView     *bool  `gorm:"column:can_view"`
}

// authorizeByMessageID 由消息 ID 经一次 JOIN 解析其所属频道，并复用
// middleware.CheckChannelAccess 做频道级权限判定（禁止在此复制权限逻辑）。
// 成功返回频道 ID（供广播复用，省掉二次查询）；失败时已写入错误响应，调用方直接 return。
// 语义：消息不存在/已软删 → 404 MESSAGE_NOT_FOUND；消息所属频道已删除 → 404 CHANNEL_NOT_FOUND；
// 无频道权限 → 403 CHANNEL_ACCESS_DENIED（响应体与 chanPerm 中间件一致）。
func (h *Handler) authorizeByMessageID(c *gin.Context, messageID string) (string, bool) {
	var row messageChannelAccessRow
	if err := h.db.Table("messages AS m").
		Select("m.id AS id, COALESCE(c.id, '') AS channel_id, COALESCE(c.type, '') AS channel_type, COALESCE(c.visibility, '') AS visibility, crp.can_view AS can_view").
		Joins("LEFT JOIN channels AS c ON c.id = m.channel_id AND c.deleted_at IS NULL").
		Joins("LEFT JOIN channel_role_permissions AS crp ON crp.channel_id = c.id AND crp.role = ?", middleware.GetRole(c)).
		Where("m.id = ? AND m.deleted_at IS NULL", messageID).
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.MESSAGE_NOT_FOUND, "message not found"))
		} else {
			errors.JSONError(c, errors.ErrInternal)
		}
		return "", false
	}
	if row.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found"))
		return "", false
	}
	if appErr := middleware.CheckChannelAccess(c, &model.Channel{
		ID:         row.ChannelID,
		Type:       row.ChannelType,
		Visibility: row.Visibility,
	}, row.CanView); appErr != nil {
		errors.JSONError(c, appErr)
		return "", false
	}
	return row.ChannelID, true
}

// attachmentChannelAccessRow 是一次 JOIN（message_attachments → messages → channels → 角色权限）的结果。
type attachmentChannelAccessRow struct {
	ID          string `gorm:"column:id"`
	MessageID   string `gorm:"column:message_id"`
	ChannelID   string `gorm:"column:channel_id"`
	ChannelType string `gorm:"column:channel_type"`
	Visibility  string `gorm:"column:visibility"`
	CanView     *bool  `gorm:"column:can_view"`
}

// authorizeAttachmentAccess 由附件 ID 经一次 JOIN 解析「附件 → 消息 → 频道」归属，
// 复用 middleware.CheckChannelAccess 做频道级判定（R-03）。?thumb=1 与正式下载走同一入口。
// 语义：附件不存在 → 404 FILE_NOT_FOUND；附件所属消息/频道已删除 → 404 MESSAGE_NOT_FOUND；
// 无频道权限 → 403 CHANNEL_ACCESS_DENIED。
// 历史数据（message_id 为空）无法解析归属频道：按 fail-closed 拒绝（service 层的 Space
// 成员校验保留为二次防线，但不足以证明频道级可见性）。
func (h *Handler) authorizeAttachmentAccess(c *gin.Context, attachmentID string) bool {
	var row attachmentChannelAccessRow
	if err := h.db.Table("message_attachments AS a").
		Select("a.id AS id, COALESCE(a.message_id, '') AS message_id, COALESCE(c.id, '') AS channel_id, COALESCE(c.type, '') AS channel_type, COALESCE(c.visibility, '') AS visibility, crp.can_view AS can_view").
		Joins("LEFT JOIN messages AS m ON m.id = a.message_id AND m.deleted_at IS NULL").
		Joins("LEFT JOIN channels AS c ON c.id = m.channel_id AND c.deleted_at IS NULL").
		Joins("LEFT JOIN channel_role_permissions AS crp ON crp.channel_id = c.id AND crp.role = ?", middleware.GetRole(c)).
		Where("a.id = ?", attachmentID).
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.FILE_NOT_FOUND, "attachment not found"))
		} else {
			errors.JSONError(c, errors.ErrInternal)
		}
		return false
	}
	if row.MessageID == "" {
		// 历史附件没有 message_id，无法解析其所属频道/空间，无法证明访问权 → 拒绝。
		errors.JSONError(c, errors.New(errors.CHANNEL_ACCESS_DENIED, "attachment has no message ownership"))
		return false
	}
	if row.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.MESSAGE_NOT_FOUND, "message not found"))
		return false
	}
	if appErr := middleware.CheckChannelAccess(c, &model.Channel{
		ID:         row.ChannelID,
		Type:       row.ChannelType,
		Visibility: row.Visibility,
	}, row.CanView); appErr != nil {
		errors.JSONError(c, appErr)
		return false
	}
	return true
}

// AddReaction adds a reaction to a message
func (h *Handler) AddReaction(c *gin.Context) {
	messageID := c.Param("id")
	userID := middleware.GetUserID(c)

	var body struct {
		Emoji string `json:"emoji"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// R-01：必须先做频道权限校验（一次 JOIN），未通过则既不落库也不广播。
	channelID, ok := h.authorizeByMessageID(c, messageID)
	if !ok {
		return
	}

	reaction, err := h.service.AddReaction(messageID, userID, body.Emoji)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast message_reaction_added event（只在校验通过后广播）
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "message_reaction_added",
			"payload": map[string]interface{}{
				"messageId": messageID,
				"channelId": channelID,
				"reaction": map[string]interface{}{
					"id":     reaction.ID,
					"userId": reaction.UserID,
					"emoji":  reaction.Emoji,
				},
			},
		})
		h.hub.BroadcastToChannel(channelID, data)
	}

	errors.Success(c, reaction)
}

// RemoveReaction removes a reaction from a message
func (h *Handler) RemoveReaction(c *gin.Context) {
	messageID := c.Param("id")
	userID := middleware.GetUserID(c)

	var body struct {
		Emoji string `json:"emoji"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// R-01：先校验频道权限，未通过则不删除、不广播（防跨频道删反应/事件注入）。
	channelID, ok := h.authorizeByMessageID(c, messageID)
	if !ok {
		return
	}

	if err := h.service.RemoveReaction(messageID, userID, body.Emoji); err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast message_reaction_removed event（只在校验通过后广播）
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "message_reaction_removed",
			"payload": map[string]interface{}{
				"messageId": messageID,
				"channelId": channelID,
				"reaction": map[string]interface{}{
					"userId": userID,
					"emoji":  body.Emoji,
				},
			},
		})
		h.hub.BroadcastToChannel(channelID, data)
	}

	errors.Success(c, gin.H{"message": "reaction removed"})
}

// GetUnreadCount returns unread message info for a channel
func (h *Handler) GetUnreadCount(c *gin.Context) {
	channelID := c.Param("id")
	userID := middleware.GetUserID(c)

	info, err := h.service.GetUnreadCount(userID, channelID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, info)
}

// unreadChangedPayload assembles the payload of the unread_count_changed event.
// 抽成纯函数以便测试锁定广播契约：firstUnreadMessageId 供客户端更新「首条未读」
// 定位锚点（mark-read 后已读清零，该值为空串表示锚点失效）。
func unreadChangedPayload(channelID, userID string, unreadCount, mentionCount int64, firstUnreadMessageID string) map[string]interface{} {
	return map[string]interface{}{
		"channelId":            channelID,
		"userId":               userID,
		"unreadCount":          unreadCount,
		"mentionCount":         mentionCount,
		"firstUnreadMessageId": firstUnreadMessageID,
	}
}

// threadUnreadDeltaPayload 增量形态（新的线程回复）：thread_unread_changed，
// 表示该线程未读 +1。发送对象 = 频道订阅者中除作者以外的所有连接。
func threadUnreadDeltaPayload(channelID, parentID, messageID, authorID string) map[string]interface{} {
	return map[string]interface{}{
		"channelId": channelID,
		"parentId":  parentID,
		"messageId": messageID,
		"authorId":  authorID,
		"delta":     1,
	}
}

// threadUnreadResetPayload 归零形态（对某线程 mark-read 后）：thread_unread_changed，
// 表示该线程未读清零。只发给操作者本人。
func threadUnreadResetPayload(channelID, parentID, lastReadMessageID string) map[string]interface{} {
	return map[string]interface{}{
		"channelId":         channelID,
		"parentId":          parentID,
		"unreadCount":       0,
		"lastReadMessageId": lastReadMessageID,
	}
}

// MarkChannelRead marks a channel as read
func (h *Handler) MarkChannelRead(c *gin.Context) {
	channelID := c.Param("id")
	userID := middleware.GetUserID(c)

	var body struct {
		LastMessageID string `json:"lastMessageId"`
		// A9 二期：可选。非空表示「标记该线程已读」，此时只写线程已读位，
		// 不影响频道已读水位，也不广播 unread_count_changed。
		ThreadParentID string `json:"threadParentId"`
	}
	// SEC-001: 允许空请求体（使用最新消息 ID），但拒绝非法 JSON
	if err := httpbind.BindJSONAllowEmpty(c, &body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid JSON body: "+err.Error()))
		return
	}

	// A9 二期（DES 7.2 / R2）：线程已读分支。校验失败统一 404 MESSAGE_NOT_FOUND。
	if body.ThreadParentID != "" {
		resolved, err := h.service.MarkThreadRead(userID, channelID, body.ThreadParentID, body.LastMessageID)
		if err != nil {
			errors.JSONError(c, err)
			return
		}
		// 水位未推进（重复上报同一回复，或更早的 lastMessageId）：不广播归零事件，
		// 否则会把「其实仍有未读」的客户端角标清错。HTTP status 与 code 均不变。
		if !resolved.Advanced {
			errors.Success(c, gin.H{"message": "thread already read"})
			return
		}
		// 归零形态只回给本人（不是 unread_count_changed，也不发给频道）。
		if h.hub != nil {
			if data, marshalErr := json.Marshal(map[string]interface{}{
				"type":    "thread_unread_changed",
				"payload": threadUnreadResetPayload(channelID, body.ThreadParentID, resolved.LastReadMessageID),
			}); marshalErr == nil {
				h.hub.BroadcastToUser(userID, data)
			}
		}
		errors.Success(c, gin.H{"message": "thread marked as read"})
		return
	}

	lastMessageID := body.LastMessageID
	if lastMessageID == "" {
		// 缺省标记到最新主消息：线程回复不再推进频道水位（A9 二期）。
		var latestMsg model.Message
		if err := h.db.Where("channel_id = ? AND parent_id IS NULL", channelID).Order("created_at DESC").First(&latestMsg).Error; err == nil {
			lastMessageID = latestMsg.ID
		}
	}

	if err := h.service.MarkChannelRead(userID, channelID, lastMessageID); err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast unread_count_changed event to the user's connections.
	// mark-read 后未读清零，firstUnreadMessageId 传空串（无未读），客户端据此清除定位锚点。
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type":    "unread_count_changed",
			"payload": unreadChangedPayload(channelID, userID, 0, 0, ""),
		})
		h.hub.BroadcastToUser(userID, data)
	}

	errors.Success(c, gin.H{"message": "marked as read"})
}

// GetThread retrieves thread replies for a parent message
func (h *Handler) GetThread(c *gin.Context) {
	parentID := c.Param("id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	// SEC(A9-S1)：线程读取必须先定位父消息的所属频道，并复用 ChannelPermission 的
	// 同口径做权限判定。修复前该路由只挂 AuthRequired，任意登录用户凭 parent 消息 ID
	// 即可跨频道/跨空间读取线程回复。
	// 防跨频道拼接：频道 ID 的唯一来源是父消息自身——用一条 JOIN 同时取出 messages 与
	// channels（含 role-specific 的 can_view），不读取任何客户端传入的频道标识；因此
	// 「父消息属于频道 A、请求声称/携带频道 B」无法改变判定结果，且全流程只查一次库。
	type threadAccessRow struct {
		ID          string `gorm:"column:id"`
		ChannelID   string `gorm:"column:channel_id"`
		ChannelType string `gorm:"column:channel_type"`
		Visibility  string `gorm:"column:visibility"`
		CanView     *bool  `gorm:"column:can_view"`
	}
	var accessRow threadAccessRow
	if err := h.db.Table("messages AS m").
		Select("m.id AS id, COALESCE(c.id, '') AS channel_id, COALESCE(c.type, '') AS channel_type, COALESCE(c.visibility, '') AS visibility, crp.can_view AS can_view").
		Joins("LEFT JOIN channels AS c ON c.id = m.channel_id AND c.deleted_at IS NULL").
		Joins("LEFT JOIN channel_role_permissions AS crp ON crp.channel_id = c.id AND crp.role = ?", middleware.GetRole(c)).
		Where("m.id = ? AND m.deleted_at IS NULL", parentID).
		Take(&accessRow).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.MESSAGE_NOT_FOUND, "message not found"))
		} else {
			errors.JSONError(c, errors.ErrInternal)
		}
		return
	}
	if accessRow.ChannelID == "" {
		// 父消息存在但所属频道已删除/不存在：与 chanPerm 的「频道不存在」语义一致。
		errors.JSONError(c, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found"))
		return
	}
	if appErr := middleware.CheckChannelAccess(c, &model.Channel{
		ID:         accessRow.ChannelID,
		Type:       accessRow.ChannelType,
		Visibility: accessRow.Visibility,
	}, accessRow.CanView); appErr != nil {
		errors.JSONError(c, appErr)
		return
	}

	messages, hasMore, err := h.service.GetThread(parentID, limit)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	type authorInfo struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Avatar      string `json:"avatar"`
	}
	type messageItem struct {
		model.Message
		Author authorInfo `json:"author"`
	}
	items := make([]messageItem, len(messages))
	for i, m := range messages {
		items[i].Message = m
		items[i].Author = authorInfo{
			ID:          m.UserID,
			Username:    m.AuthorUsername,
			DisplayName: m.AuthorDisplayName,
			Avatar:      m.AuthorAvatarURL,
		}
	}

	// A9 二期 §7.7：GetThread 回填 threadReplyCount（客户端抽屉标题「N 条回复」）。
	var replyTotal int64
	total := int64(len(messages))
	if counts, cerr := h.service.ThreadReplyCounts([]string{parentID}); cerr == nil {
		replyTotal = counts[parentID]
		// total 语义 = 该线程回复总数（真实 COUNT，不受 limit 截断），与
		// threadReplyCount 同源同值；避免客户端把「本页条数」误当总数。
		total = replyTotal
	}
	errors.Success(c, gin.H{
		"messages":         items,
		"hasMore":          hasMore,
		"total":            total,
		"threadReplyCount": replyTotal,
	})
}

// SearchMessages searches messages within a channel.
func (h *Handler) SearchMessages(c *gin.Context) {
	channelID := c.Param("id")
	query := strings.TrimSpace(c.Query("q"))
	if utf8.RuneCountInString(query) < 2 {
		errors.JSONError(c, errors.New(errors.SEARCH_QUERY_TOO_SHORT, "search query must contain at least 2 characters"))
		return
	}
	cursor := c.Query("cursor")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 100 {
		limit = 50
	}

	messages, hasMore, nextCursor, err := h.service.SearchMessages(channelID, query, cursor, limit)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	type authorInfo struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Avatar      string `json:"avatar"`
	}
	type messageItem struct {
		model.Message
		Author authorInfo `json:"author"`
	}
	items := make([]messageItem, len(messages))
	for i, m := range messages {
		items[i].Message = m
		items[i].Author = authorInfo{
			ID:          m.UserID,
			Username:    m.AuthorUsername,
			DisplayName: m.AuthorDisplayName,
			Avatar:      m.AuthorAvatarURL,
		}
	}

	errors.Success(c, gin.H{
		"items":      items,
		"hasMore":    hasMore,
		"cursor":     nextCursor,
		"totalCount": len(items),
	})
}

// SearchAllMessages searches only ordinary text channels visible to the user.
// The visible channel set is resolved before querying messages so an omitted
// channel_id cannot turn this endpoint into a cross-space data oracle.
func (h *Handler) SearchAllMessages(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if utf8.RuneCountInString(query) < 2 {
		errors.JSONError(c, errors.New(errors.SEARCH_QUERY_TOO_SHORT, "search query must contain at least 2 characters"))
		return
	}

	limit := 25
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("limit must be between 1 and 100"))
			return
		}
		limit = parsed
	}

	var before, after *time.Time
	for name, target := range map[string]**time.Time{"before": &before, "after": &after} {
		raw := c.Query(name)
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails(name+" must be RFC3339"))
			return
		}
		*target = &parsed
	}

	visibleChannels, err := channel.NewService(h.db).GetChannels(
		middleware.GetUserID(c), middleware.GetRole(c),
	)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	visibleIDs := make([]string, 0, len(visibleChannels))
	for _, visible := range visibleChannels {
		if strings.EqualFold(visible.Type, "text") {
			visibleIDs = append(visibleIDs, visible.ID)
		}
	}

	requestedChannel := c.Query("channel_id")
	if requestedChannel != "" {
		allowed := false
		for _, visibleID := range visibleIDs {
			if visibleID == requestedChannel {
				allowed = true
				break
			}
		}
		if !allowed {
			errors.JSONError(c, errors.New(errors.CHANNEL_ACCESS_DENIED, "channel access denied"))
			return
		}
		visibleIDs = []string{requestedChannel}
	}

	results, total, err := h.service.SearchAllMessages(GlobalSearchOptions{
		Query:      query,
		ChannelIDs: visibleIDs,
		AuthorID:   c.Query("from"),
		Before:     before,
		After:      after,
		Limit:      limit,
	})
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"results": results, "total": total})
}

// AckMessage acknowledges a message as received
//
// M-1（安全批次 task-23）：ACK 是消息级写操作，此前只挂 AuthRequired，任意登录用户可对
// 任意 messageID（含 admin-only / role-specific 私有频道的消息）落 ack。现改为入口先做
// 频道可见性校验：复用 authorizeByMessageID（一次 JOIN messages→channels→channel_role_permissions
// + middleware.CheckChannelAccess，与 reactions / 附件下载同一套判定），未通过时直接返回，
// 不落库。语义与其它消息级接口保持一致：
//   - 消息不存在/已软删 → 404 MESSAGE_NOT_FOUND
//   - 无频道可见性 → 403 CHANNEL_ACCESS_DENIED
//   - DM 非参与者 → 404 CHANNEL_NOT_FOUND（先于 OWNER，由 CheckChannelAccess 决定）
//
// 成功响应形状不变（204 No Content）。
func (h *Handler) AckMessage(c *gin.Context) {
	messageID := c.Param("id")
	userID := middleware.GetUserID(c)

	// 校验失败时 authorizeByMessageID 已写出错误响应（404/403），此处直接返回即可。
	channelID, ok := h.authorizeByMessageID(c, messageID)
	if !ok {
		return
	}

	if err := h.service.RecordAck(messageID, userID, channelID); err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	c.Status(http.StatusNoContent)
}
