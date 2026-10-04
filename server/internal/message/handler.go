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
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
}

// NewHandler creates a new message handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub, svc *Service) *Handler {
	if svc == nil {
		svc = NewService(db)
	}
	return &Handler{service: svc, db: db, cfg: cfg, hub: hub}
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
	if around != "" {
		messages, hasMore, err = h.service.GetMessagesAround(channelID, around, limit)
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
		ID        string    `json:"id"`
		Username  string    `json:"username"`
		Content   string    `json:"content"`
		CreatedAt time.Time `json:"createdAt"`
	}

	type messageItem struct {
		model.Message
		Author  authorInfo   `json:"author"`
		ReplyTo *replyToInfo `json:"replyTo,omitempty"`
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

	items := make([]messageItem, len(messages))
	for i, m := range messages {
		items[i].Message = m
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
					ID:        parent.ID,
					Username:  parent.AuthorUsername,
					Content:   content,
					CreatedAt: parent.CreatedAt,
				}
			}
		}
	}

	errors.Success(c, gin.H{
		"items":   items,
		"hasMore": hasMore,
		"cursor":  cursor,
	})
}

// CreateMessage creates a new message in a channel.
// Accepts JSON (legacy: {content, clientMessageId}) or multipart/form-data
// (with optional "attachments" file field and "content"/"clientMessageId" form fields).
func (h *Handler) CreateMessage(c *gin.Context) {
	channelID := c.Param("id")
	userID := middleware.GetUserID(c)
	username := middleware.GetUsername(c)
	role := middleware.GetRole(c)

	var content string
	var clientMessageID string
	var parentID string
	var files []*multipart.FileHeader

	contentType := c.GetHeader("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		content = c.PostForm("content")
		clientMessageID = c.PostForm("clientMessageId")
		parentID = c.PostForm("parentId")
		form, err := c.MultipartForm()
		if err == nil && form != nil {
			files = form.File["attachments"]
		}
	} else {
		var body struct {
			Content         string  `json:"content"`
			ClientMessageID string  `json:"clientMessageId"`
			ParentID        *string `json:"parentId"`
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
	}

	msg, err := h.service.CreateMessage(channelID, userID, username, role, content, clientMessageID, parentID, files)
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

	reaction, err := h.service.AddReaction(messageID, userID, body.Emoji)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast message_reaction_added event
	if h.hub != nil {
		// Look up channel ID for the message
		if msg, err := h.service.GetMessage(messageID); err == nil {
			data, _ := json.Marshal(map[string]interface{}{
				"type": "message_reaction_added",
				"payload": map[string]interface{}{
					"messageId": messageID,
					"channelId": msg.ChannelID,
					"reaction": map[string]interface{}{
						"id":     reaction.ID,
						"userId": reaction.UserID,
						"emoji":  reaction.Emoji,
					},
				},
			})
			h.hub.BroadcastToChannel(msg.ChannelID, data)
		}
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

	if err := h.service.RemoveReaction(messageID, userID, body.Emoji); err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast message_reaction_removed event
	if h.hub != nil {
		if msg, err := h.service.GetMessage(messageID); err == nil {
			data, _ := json.Marshal(map[string]interface{}{
				"type": "message_reaction_removed",
				"payload": map[string]interface{}{
					"messageId": messageID,
					"channelId": msg.ChannelID,
					"reaction": map[string]interface{}{
						"userId": userID,
						"emoji":  body.Emoji,
					},
				},
			})
			h.hub.BroadcastToChannel(msg.ChannelID, data)
		}
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

// MarkChannelRead marks a channel as read
func (h *Handler) MarkChannelRead(c *gin.Context) {
	channelID := c.Param("id")
	userID := middleware.GetUserID(c)

	var body struct {
		LastMessageID string `json:"lastMessageId"`
	}
	// SEC-001: 允许空请求体（使用最新消息 ID），但拒绝非法 JSON
	if err := httpbind.BindJSONAllowEmpty(c, &body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid JSON body: "+err.Error()))
		return
	}

	lastMessageID := body.LastMessageID
	if lastMessageID == "" {
		// Get the latest message ID in the channel
		var latestMsg model.Message
		if err := h.db.Where("channel_id = ?", channelID).Order("created_at DESC").First(&latestMsg).Error; err == nil {
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

	errors.Success(c, gin.H{
		"messages": items,
		"hasMore":  hasMore,
		"total":    len(messages),
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
func (h *Handler) AckMessage(c *gin.Context) {
	messageID := c.Param("id")
	userID := middleware.GetUserID(c)

	// Get the message to find its channel
	msg, err := h.service.GetMessage(messageID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	if err := h.service.RecordAck(messageID, userID, msg.ChannelID); err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	c.Status(http.StatusNoContent)
}
