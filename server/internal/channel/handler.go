package channel

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles HTTP requests for channels
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
}

// NewHandler creates a new channel handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{service: NewService(db), db: db, cfg: cfg, hub: hub}
}

// RegisterRoutes registers channel routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	// Space routes
	r.GET("/space", middleware.AuthRequired(h.cfg, h.db), h.GetSpace)
	r.PATCH("/space", middleware.AuthRequired(h.cfg, h.db), middleware.RequireAdmin(), h.UpdateSpace)
	r.PATCH("/space/members/:uid", middleware.AuthRequired(h.cfg, h.db), middleware.RequireAdmin(), h.UpdateMemberRole)
	r.DELETE("/space/members/:uid", middleware.AuthRequired(h.cfg, h.db), middleware.RequireAdmin(), h.RemoveMember)

	// Channel routes
	r.GET("/channels", middleware.AuthRequired(h.cfg, h.db), h.GetChannels)
	r.GET("/channels/:id", middleware.AuthRequired(h.cfg, h.db), h.GetChannel)
	r.POST("/channels", middleware.AuthRequired(h.cfg, h.db), middleware.RequireAdmin(), h.CreateChannel)
	r.PATCH("/channels/:id", middleware.AuthRequired(h.cfg, h.db), middleware.RequireAdmin(), h.UpdateChannel)
	r.DELETE("/channels/:id", middleware.AuthRequired(h.cfg, h.db), middleware.RequireAdmin(), h.DeleteChannel)
	r.PUT("/channels/:id/pin", middleware.AuthRequired(h.cfg, h.db), middleware.RequireAdmin(), h.PinMessage)
	r.DELETE("/channels/:id/pin", middleware.AuthRequired(h.cfg, h.db), middleware.RequireAdmin(), h.UnpinMessage)

	// T4/S-1 频道级静音：
	//   * GET /channels/mutes — 当前用户生效中的静音清单（静态段优先于 :id，gin ≥1.7 支持）；
	//   * PUT /channels/:id/mute — 本人开/关本人静音，频道可见性走 ChannelPermission
	//     （与 unread/mark-read 同一防线：admin-only 频道 MEMBER 会拿到 403）。
	r.GET("/channels/mutes", middleware.AuthRequired(h.cfg, h.db), h.ListChannelMutes)
	r.PUT("/channels/:id/mute", middleware.AuthRequired(h.cfg, h.db), middleware.ChannelPermission(h.db), h.SetChannelMute)
}

// GetSpace returns the current space info
func (h *Handler) GetSpace(c *gin.Context) {
	space, err := h.service.GetSpace()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, space)
}

// UpdateSpace updates the space info
func (h *Handler) UpdateSpace(c *gin.Context) {
	var body struct {
		Name          string `json:"name"`
		Icon          string `json:"icon"`
		AllowRegister *bool  `json:"allowRegister"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	updates := map[string]interface{}{}
	if body.Name != "" {
		updates["name"] = body.Name
	}
	if body.Icon != "" {
		updates["icon"] = body.Icon
	}
	if body.AllowRegister != nil {
		updates["allow_register"] = *body.AllowRegister
	}

	if err := h.service.UpdateSpace(updates); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "space updated"})
}

// GetMembers returns all space members
func (h *Handler) GetMembers(c *gin.Context) {
	// This is handled by user handler, but we need it here for the route
	errors.Success(c, gin.H{"message": "use /api/v1/users for members"})
}

// UpdateMemberRole updates a member's role
func (h *Handler) UpdateMemberRole(c *gin.Context) {
	uid := c.Param("uid")
	var body struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if err := h.service.UpdateMemberRole(uid, body.Role); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "role updated"})
}

// RemoveMember removes a member from the space
func (h *Handler) RemoveMember(c *gin.Context) {
	uid := c.Param("uid")
	currentUserID := middleware.GetUserID(c)
	if uid == currentUserID {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "cannot remove yourself"))
		return
	}

	if err := h.service.RemoveMember(uid); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "member removed"})
}

// GetChannels returns all channels visible to the current user
func (h *Handler) GetChannels(c *gin.Context) {
	userID := middleware.GetUserID(c)
	userRole := middleware.GetRole(c)
	channels, err := h.service.GetChannels(userID, userRole)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, channels)
}

// GetChannel returns a single channel by ID.
func (h *Handler) GetChannel(c *gin.Context) {
	id := c.Param("id")
	channel, err := h.service.GetChannel(id)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, channel)
}

// CreateChannel creates a new channel
func (h *Handler) CreateChannel(c *gin.Context) {
	var body struct {
		Name         string `json:"name"`
		Type         string `json:"type"`
		IsPrivate    bool   `json:"isPrivate"`
		AudioQuality string `json:"audioQuality"`
		Visibility   string `json:"visibility"`
		SortGroup    string `json:"sortGroup"` // A1-S1：可选分组名，空串=未分组
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if body.Type == "" {
		body.Type = "text"
	}

	// FEATURE is treated as text (text channel with featured content)
	if body.Type == "FEATURE" || body.Type == "TEXT" {
		body.Type = "text"
	}
	if body.Type == "VOICE" {
		body.Type = "voice"
	}
	ch, err := h.service.CreateChannel(body.Name, body.Type, body.IsPrivate, body.AudioQuality, body.Visibility, body.SortGroup)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, ch)
}

// UpdateChannel updates a channel
func (h *Handler) UpdateChannel(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Name         string  `json:"name"`
		IsPrivate    *bool   `json:"isPrivate"`
		Visibility   string  `json:"visibility"`
		Position     *int    `json:"position"`
		AudioQuality string  `json:"audioQuality"`
		SortGroup    *string `json:"sortGroup"` // A1-S1：指针语义——未传不改；传空串=移出分组
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	updates := map[string]interface{}{}
	if body.Name != "" {
		updates["name"] = body.Name
	}
	if body.Visibility != "" {
		switch body.Visibility {
		case "public", "admin-only", "role-specific":
			updates["visibility"] = body.Visibility
		default:
			errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "invalid visibility"))
			return
		}
	} else if body.IsPrivate != nil {
		// Legacy isPrivate flag: map to new visibility values
		if *body.IsPrivate {
			updates["visibility"] = "admin-only"
		} else {
			updates["visibility"] = "public"
		}
	}
	if body.Position != nil {
		updates["position"] = *body.Position
	}
	// A1-S1：分组名指针语义——未传（nil）不修改；传空串 = 移出分组（清空 sort_group）。
	// 校验（trim + rune 数上限）与 service.CreateChannel 共用 NormalizeSortGroup。
	if body.SortGroup != nil {
		normalized, err := NormalizeSortGroup(*body.SortGroup)
		if err != nil {
			errors.JSONError(c, err)
			return
		}
		updates["sort_group"] = normalized
	}
	// P0-3: 音质此前根本没被 body 接收，导致选择器静默无效。
	// 取值域与 service.CreateChannel 一致（fluent/standard/high/ultra）。
	if body.AudioQuality != "" {
		switch body.AudioQuality {
		case "fluent", "standard", "high", "ultra":
			updates["voice_quality"] = body.AudioQuality
		default:
			errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "invalid audioQuality, must be one of: fluent, standard, high, ultra"))
			return
		}
	}

	if err := h.service.UpdateChannel(id, updates); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "channel updated"})
}

// DeleteChannel deletes a channel
func (h *Handler) DeleteChannel(c *gin.Context) {
	id := c.Param("id")
	if err := h.service.DeleteChannel(id); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "channel deleted"})
}

// PinMessage pins a message to the channel
func (h *Handler) PinMessage(c *gin.Context) {
	channelID := c.Param("id")
	var body struct {
		MessageID string `json:"messageId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.MessageID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("messageId required"))
		return
	}

	// Get previous pinned message ID for the broadcast payload
	var ch model.Channel
	previousPinnedID := ""
	if err := h.db.Select("pinned_message_id").First(&ch, "id = ?", channelID).Error; err == nil {
		if ch.PinnedMessageID != nil {
			previousPinnedID = *ch.PinnedMessageID
		}
	}

	if err := h.service.PinMessage(channelID, body.MessageID); err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast channel_pin_updated event
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "channel_pin_updated",
			"payload": map[string]interface{}{
				"channelId":               channelID,
				"pinnedMessageId":         body.MessageID,
				"previousPinnedMessageId": previousPinnedID,
			},
		})
		h.hub.BroadcastToChannel(channelID, data)
	}

	errors.Success(c, gin.H{"message": "message pinned"})
}

// UnpinMessage removes the pinned message from the channel
func (h *Handler) UnpinMessage(c *gin.Context) {
	channelID := c.Param("id")
	if err := h.service.UnpinMessage(channelID); err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast channel_pin_updated event (pinnedMessageId is null)
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "channel_pin_updated",
			"payload": map[string]interface{}{
				"channelId":               channelID,
				"pinnedMessageId":         nil,
				"previousPinnedMessageId": nil,
			},
		})
		h.hub.BroadcastToChannel(channelID, data)
	}

	errors.Success(c, gin.H{"message": "unpinned"})
}

// SetChannelMute 开/关当前用户对某个频道的静音（T4/S-1）。
// body: { muted: bool, durationMinutes?: int }——durationMinutes > 0 表示限时静音，缺省永久。
func (h *Handler) SetChannelMute(c *gin.Context) {
	channelID := c.Param("id")
	userID := middleware.GetUserID(c)

	var body struct {
		Muted           bool `json:"muted"`
		DurationMinutes int  `json:"durationMinutes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if body.DurationMinutes < 0 {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("durationMinutes must be >= 0"))
		return
	}

	if err := h.service.SetChannelMute(userID, channelID, body.Muted, body.DurationMinutes); err != nil {
		errors.JSONError(c, err)
		return
	}
	message := "channel unmuted"
	if body.Muted {
		message = "channel muted"
	}
	errors.Success(c, gin.H{"message": message})
}

// ListChannelMutes 返回当前用户生效中的频道静音清单（供客户端启动时批量拉取）。
func (h *Handler) ListChannelMutes(c *gin.Context) {
	userID := middleware.GetUserID(c)
	mutes, err := h.service.ListChannelMutes(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, mutes)
}
