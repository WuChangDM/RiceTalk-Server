package dm

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/message"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/user"
	"ridgericetalk/middleware"
)

// Handler handles DM channel HTTP requests.
type Handler struct {
	service    *Service
	msgService *message.Service
	userSvc    *user.Service
	db         *gorm.DB
	cfg        *config.Config
	hub        *realtime.Hub
}

// NewHandler creates a new DM handler.
func NewHandler(db *gorm.DB, cfg *config.Config, msgService *message.Service, hub *realtime.Hub) *Handler {
	return &Handler{
		service:    NewService(db),
		msgService: msgService,
		userSvc:    user.NewService(db),
		db:         db,
		cfg:        cfg,
		hub:        hub,
	}
}

// RegisterRoutes registers DM routes.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	dm := r.Group("/dm/channels")
	dm.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		dm.GET("", h.ListDMChannels)
		dm.POST("", h.CreateDMChannel)
		dm.GET("/:id/messages", h.GetMessages)
		dm.POST("/:id/messages", h.CreateMessage)
	}
}

// ListDMChannels returns the current user's DM channels.
func (h *Handler) ListDMChannels(c *gin.Context) {
	userID := middleware.GetUserID(c)
	channels, err := h.service.ListDMChannels(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, channels)
}

// CreateDMChannel creates or returns an existing DM channel with the recipient.
func (h *Handler) CreateDMChannel(c *gin.Context) {
	userID := middleware.GetUserID(c)
	var body struct {
		RecipientID string `json:"recipientId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.RecipientID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("recipientId required"))
		return
	}

	ch, err := h.service.GetOrCreateDMChannel(userID, body.RecipientID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, ch)
}

// GetMessages returns messages for a DM channel.
func (h *Handler) GetMessages(c *gin.Context) {
	userID := middleware.GetUserID(c)
	channelID := c.Param("id")

	ok, err := h.service.IsDMChannelParticipant(channelID, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if !ok {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	limitStr := c.DefaultQuery("limit", "50")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	cursor := c.Query("cursor")

	messages, hasMore, err := h.msgService.GetMessages(channelID, cursor, limit)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	c.JSON(http.StatusOK, errors.Response{
		Code: "OK",
		Data: gin.H{
			"items":   messages,
			"hasMore": hasMore,
		},
	})
}

// CreateMessage creates a text message in a DM channel.
func (h *Handler) CreateMessage(c *gin.Context) {
	userID := middleware.GetUserID(c)
	channelID := c.Param("id")

	ok, err := h.service.IsDMChannelParticipant(channelID, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if !ok {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Content) == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("content required"))
		return
	}

	u, err := h.userSvc.GetUser(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// username 已退役：私信作者快照存显示名（AuthorUsername 兜底字段，读时按 UserID 解析 displayName）
	authorName := u.DisplayName
	if authorName == "" {
		authorName = u.Username
	}
	msg, err := h.msgService.CreateMessageText(channelID, userID, authorName, u.Role, body.Content, "")
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
			"clientMessageId": msg.ClientMessageID,
			"attachments":     msg.Attachments,
			"createdAt":       msg.CreatedAt,
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type":    "message",
			"payload": payload,
		})
		h.hub.BroadcastToChannel(channelID, data)
	}
	errors.Success(c, msg)
}

