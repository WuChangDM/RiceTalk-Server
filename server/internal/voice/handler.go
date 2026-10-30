package voice

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles HTTP requests for voice
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
}

// NewHandler creates a new voice handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{service: NewService(db, cfg, hub), db: db, cfg: cfg, hub: hub}
}

// SetSfxTrigger injects the sfx trigger, forwarding to the service.
func (h *Handler) SetSfxTrigger(t SfxTrigger) { h.service.SetSfxTrigger(t) }

// CleanupParticipantForUser removes a user from a voice room and broadcasts
// the updated participant list. Intended to be called from main.go's
// OnUserFullyOffline hook so that ghost records are reaped when a user's last
// WebSocket disconnects.
func (h *Handler) CleanupParticipantForUser(userID, roomID string) error {
	return h.service.CleanupParticipant(userID, roomID)
}

// CleanupOnStartup removes any voice participant records that are not
// legitimate on server boot (orphans, stale entries, users with offline
// presence). Intended to be called from main.go right after hub.Run() so that
// any clients already connected see the correct participant list immediately.
func (h *Handler) CleanupOnStartup() {
	h.service.CleanupOnStartup()
}

// RegisterRoutes registers voice routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	voice := r.Group("/voice")
	voice.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		voice.GET("/status", h.GetStatus)
		voice.POST("/token", h.GetToken)
		voice.POST("/token/refresh", h.RefreshToken)
		voice.GET("/participants/:id", h.GetParticipants)
		voice.POST("/join", h.JoinVoice)
		voice.POST("/leave", h.LeaveVoice)
		voice.POST("/participants/:id/mute", middleware.RequireAdmin(), h.MuteParticipant)
		voice.POST("/participants/:id/kick", middleware.RequireAdmin(), h.KickParticipant)
		voice.POST("/recordings", middleware.RequireAdmin(), h.StartRecording)
		voice.DELETE("/recordings", middleware.RequireAdmin(), h.StopRecording)
		voice.GET("/recordings", h.GetRecordings)
		voice.GET("/e2ee/key", h.GetE2EEKey)
		voice.POST("/e2ee/key/rotate", middleware.RequireAdmin(), h.RotateE2EEKey)
		// DES-20261006-01 §二：房间口令（零知识密钥协商）。
		// 权限与频道/语音写接口一致（RequireAdmin：OWNER/ADMIN）。
		voice.PUT("/e2ee/passphrase", middleware.RequireAdmin(), h.SetE2EEPassphrase)
	}
}

// broadcastVoiceEvent emits a real-time event to a voice room and, when the
// room is bound to a channel, to the channel subscribers as well. This keeps
// existing channel-subscribed clients working while also supporting independent
// room subscriptions.
func (h *Handler) broadcastVoiceEvent(roomID string, data []byte) {
	if h.hub == nil || roomID == "" {
		return
	}
	h.hub.BroadcastToRoom(roomID, data)
	// If the room is bound to a channel, also broadcast to channel subscribers.
	var room model.VoiceRoom
	if err := h.db.Select("bind_channel_id").First(&room, "id = ?", roomID).Error; err == nil {
		if room.BindChannelID != nil && *room.BindChannelID != "" {
			h.hub.BroadcastToChannel(*room.BindChannelID, data)
		}
	}
}

// voiceChannelAccessRow 是一次 JOIN（channels → 角色频道权限）的结果（R-02）。
type voiceChannelAccessRow struct {
	ID          string `gorm:"column:id"`
	ChannelType string `gorm:"column:type"`
	Visibility  string `gorm:"column:visibility"`
	CanView     *bool  `gorm:"column:can_view"`
}

// resolveVoiceRoomChannelID 解析语音房间对应的频道 ID（R-02）。
// 返回 ("", nil) 表示该房间未绑定频道（独立房间）：没有频道可见性可判，
// 由 service 层既有的「空间成员」校验兜底。
func (h *Handler) resolveVoiceRoomChannelID(roomID string) (string, *errors.AppError) {
	var room model.VoiceRoom
	if err := h.db.Select("id", "bind_channel_id").First(&room, "id = ?", roomID).Error; err == nil {
		if room.BindChannelID != nil && *room.BindChannelID != "" {
			return *room.BindChannelID, nil
		}
		return "", nil
	} else if err != gorm.ErrRecordNotFound {
		return "", errors.ErrInternal
	}

	// 房间记录尚未创建：roomID 可能就是频道 ID（findOrCreateVoiceRoom 会自动 provision），
	// 或已有独立房间绑定到该频道。两种情况都按该频道做可见性校验。
	var bound model.VoiceRoom
	if err := h.db.Select("id").First(&bound, "bind_channel_id = ?", roomID).Error; err == nil {
		return roomID, nil
	} else if err != gorm.ErrRecordNotFound {
		return "", errors.ErrInternal
	}
	var channel model.Channel
	if err := h.db.Select("id").First(&channel, "id = ?", roomID).Error; err == nil {
		return channel.ID, nil
	} else if err != gorm.ErrRecordNotFound {
		return "", errors.ErrInternal
	}
	return "", errors.New(errors.VOICE_ROOM_NOT_FOUND, "voice room not found")
}

// loadVoiceChannelAccess 用一次 JOIN 取频道可见性 + 当前角色的 can_view（R-02）。
func (h *Handler) loadVoiceChannelAccess(c *gin.Context, channelID string) (*voiceChannelAccessRow, *errors.AppError) {
	var row voiceChannelAccessRow
	if err := h.db.Table("channels AS c").
		Select("c.id AS id, COALESCE(c.type, '') AS type, COALESCE(c.visibility, '') AS visibility, crp.can_view AS can_view").
		Joins("LEFT JOIN channel_role_permissions AS crp ON crp.channel_id = c.id AND crp.role = ?", middleware.GetRole(c)).
		Where("c.id = ? AND c.deleted_at IS NULL", channelID).
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
		}
		return nil, errors.ErrInternal
	}
	return &row, nil
}

// authorizeVoiceRoomAccess 在**签发 LiveKit token / 加入房间之前**做频道可见性校验（R-02）。
// 事件注入与媒体面越权的共同入口：token 一旦签发即不可回收，因此校验必须前置到 service 调用之前。
// 复用 middleware.CheckChannelAccess 纯内核：DM → 404 CHANNEL_NOT_FOUND（先于 OWNER）、
// public 放行、admin-only 非 ADMIN → 403、role-specific 无行或 can_view=false → 403、
// 未知 visibility → 403；频道不存在/已软删 → 404 CHANNEL_NOT_FOUND。
func (h *Handler) authorizeVoiceRoomAccess(c *gin.Context, roomID string) bool {
	channelID, appErr := h.resolveVoiceRoomChannelID(roomID)
	if appErr != nil {
		errors.JSONError(c, appErr)
		return false
	}
	if channelID == "" {
		return true
	}
	row, appErr := h.loadVoiceChannelAccess(c, channelID)
	if appErr != nil {
		errors.JSONError(c, appErr)
		return false
	}
	if appErr := middleware.CheckChannelAccess(c, &model.Channel{
		ID:         row.ID,
		Type:       row.ChannelType,
		Visibility: row.Visibility,
	}, row.CanView); appErr != nil {
		errors.JSONError(c, appErr)
		return false
	}
	return true
}

// GetE2EEKey returns the E2EE material for a room (or generates one if it doesn't exist).
// DES-20261006-01 §二：按房间模式分派响应（data 包装保持一期结构）：
//   - 口令房：{"mode":"passphrase","magic":"<blob>"} —— 不返回 key；
//   - 一期模式：{"mode":"server","key":"<hex>"} —— 行为与一期一致（新增 mode 字段）。
func (h *Handler) GetE2EEKey(c *gin.Context) {
	roomID := c.Query("roomId")
	if roomID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("roomId is required"))
		return
	}
	// R-04：频道可见性闸门只拦「会实际下发密钥本体」的 server 模式。
	//   - 模式判定只读房间元数据（E2EEKeyMode），不读库取 key、不生成 key；
	//   - 403 分支因此不会产生 e2ee_keys 行，也不写任何日志明文；
	//   - 二期口令/客户端自持密钥模式（passphrase）不依赖服务端下发密钥，
	//     保持既有判定顺序与可用性，不因频道可见性把口令房成员锁死。
	// 判定顺序与 R-02 一致：DM → 404 CHANNEL_NOT_FOUND（先于 OWNER 放行）、
	// 无权限 → 403 CHANNEL_ACCESS_DENIED、房间/频道不存在或已软删 → 404。
	keyMode, modeErr := h.service.E2EEKeyMode(roomID)
	if modeErr != nil {
		errors.JSONError(c, modeErr)
		return
	}
	if keyMode == E2EEModeServer && !h.authorizeVoiceRoomAccess(c, roomID) {
		return
	}

	userID := middleware.GetUserID(c)
	info, err := h.service.GetE2EEKey(roomID, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if info.Mode == E2EEModePassphrase {
		errors.Success(c, gin.H{"mode": info.Mode, "magic": info.Magic})
		return
	}
	errors.Success(c, gin.H{"mode": info.Mode, "key": info.Key})
}

// SetE2EEPassphrase sets or clears the room passphrase magic blob.
// DES-20261006-01 §二：body {"roomId": "...", "magic": "<blob>"|null}。
// magic 非空 = 启用口令模式（≤4KB、合法 base64）；null/空 = 清除，退回一期模式。
// 权限：路由层 RequireAdmin()（OWNER/ADMIN，与频道设置写接口一致）。
func (h *Handler) SetE2EEPassphrase(c *gin.Context) {
	var body struct {
		RoomID string  `json:"roomId"`
		Magic  *string `json:"magic"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if body.RoomID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("roomId is required"))
		return
	}
	if err := h.service.SetE2EEPassphrase(body.RoomID, body.Magic); err != nil {
		errors.JSONError(c, err)
		return
	}
	enabled := body.Magic != nil && *body.Magic != ""
	errors.Success(c, gin.H{
		"message":           map[bool]string{true: "房间口令已更新", false: "房间口令已清除"}[enabled],
		"passphraseEnabled": enabled,
	})
}

// RotateE2EEKey generates a new E2EE key for a room, invalidating the old one
func (h *Handler) RotateE2EEKey(c *gin.Context) {
	var body struct {
		RoomID string `json:"roomId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if err := h.service.RotateE2EEKey(body.RoomID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "E2EE key rotated"})
}

// GetStatus returns voice service status
func (h *Handler) GetStatus(c *gin.Context) {
	errors.Success(c, h.service.GetStatus())
}

// GetToken generates a LiveKit token for a room
func (h *Handler) GetToken(c *gin.Context) {
	var body struct {
		RoomID   string `json:"roomId"`
		Identity string `json:"identity,omitempty"` // 可选虚身份（{uid}:share），屏幕共享原生模块用
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// R-02：签发 token 之前先做频道可见性校验（token 一旦返回即不可回收）。
	if !h.authorizeVoiceRoomAccess(c, body.RoomID) {
		return
	}

	userID := middleware.GetUserID(c)
	username := middleware.GetUsername(c)
	role := middleware.GetRole(c)

	token, err := h.service.GenerateTokenWithIdentity(userID, username, role, body.RoomID, body.Identity)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"token":      token,
		"room":       "rrt-room-" + body.RoomID,
		"livekitUrl": h.cfg.LiveKitURLForClient(),
		"expiresIn":  480,
	})
}

// GetParticipants returns participants in a voice room
func (h *Handler) GetParticipants(c *gin.Context) {
	roomID := c.Param("id")
	// R-04：参与者名单同样受频道可见性保护（私有房的成员名单/是否有人在房属于敏感信息）。
	if !h.authorizeVoiceRoomAccess(c, roomID) {
		return
	}
	participants, err := h.service.GetParticipants(roomID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, participants)
}

// JoinVoice records a user joining a voice room.
// service.JoinVoice internally broadcasts voice_participants_updated with the
// fresh full list, so the handler does not emit a separate joined event.
func (h *Handler) JoinVoice(c *gin.Context) {
	var body struct {
		RoomID string `json:"roomId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// R-02：加入房间（媒体面）之前同样做频道可见性校验。
	if !h.authorizeVoiceRoomAccess(c, body.RoomID) {
		return
	}

	userID := middleware.GetUserID(c)
	if err := h.service.JoinVoice(userID, body.RoomID); err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{"message": "joined voice room"})
}

// LeaveVoice records a user leaving a voice room.
// service.LeaveVoice internally broadcasts voice_participants_updated.
func (h *Handler) LeaveVoice(c *gin.Context) {
	var body struct {
		RoomID string `json:"roomId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	if err := h.service.LeaveVoice(userID, body.RoomID); err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{"message": "left voice room"})
}

// RefreshToken refreshes the LiveKit token for the current room
func (h *Handler) RefreshToken(c *gin.Context) {
	var body struct {
		RoomID   string `json:"roomId"`
		Identity string `json:"identity,omitempty"` // 可选虚身份（{uid}:share），屏幕共享原生模块用
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// R-02：签发 token 之前先做频道可见性校验（token 一旦返回即不可回收）。
	if !h.authorizeVoiceRoomAccess(c, body.RoomID) {
		return
	}

	userID := middleware.GetUserID(c)
	username := middleware.GetUsername(c)
	role := middleware.GetRole(c)

	token, err := h.service.GenerateTokenWithIdentity(userID, username, role, body.RoomID, body.Identity)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"token":      token,
		"room":       "rrt-room-" + body.RoomID,
		"livekitUrl": h.cfg.LiveKitURLForClient(),
		"expiresIn":  480,
	})
}

// MuteParticipant allows admin to force mute/unmute a user
func (h *Handler) MuteParticipant(c *gin.Context) {
	userID := c.Param("id")
	var body struct {
		RoomID string `json:"roomId"`
		Muted  bool   `json:"muted"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if err := h.service.MuteParticipant(userID, body.RoomID, body.Muted); err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast mute state change
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "voice_state_change",
			"payload": map[string]interface{}{
				"room_id":    body.RoomID,
				"channel_id": body.RoomID, // backward compatibility
				"user_id":    userID,
				"event":      map[bool]string{true: "muted", false: "unmuted"}[body.Muted],
			},
		})
		h.broadcastVoiceEvent(body.RoomID, data)
	}

	errors.Success(c, gin.H{"user_id": userID, "muted": body.Muted})
}

// KickParticipant allows admin to remove a user from a voice room
func (h *Handler) KickParticipant(c *gin.Context) {
	userID := c.Param("id")
	var body struct {
		RoomID string `json:"roomId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if err := h.service.KickParticipant(userID, body.RoomID); err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast kick event
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "voice_state_change",
			"payload": map[string]interface{}{
				"room_id":    body.RoomID,
				"channel_id": body.RoomID, // backward compatibility
				"user_id":    userID,
				"event":      "kicked",
			},
		})
		h.broadcastVoiceEvent(body.RoomID, data)
	}

	c.Status(http.StatusNoContent)
}

// StartRecording starts voice room recording
func (h *Handler) StartRecording(c *gin.Context) {
	var body struct {
		RoomID string `json:"roomId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	recording, err := h.service.StartRecording(userID, body.RoomID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast recording started
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "recording_started",
			"payload": map[string]interface{}{
				"room_id":      body.RoomID,
				"channel_id":   body.RoomID, // backward compatibility
				"recording_id": recording.ID,
				"started_by":   userID,
			},
		})
		h.broadcastVoiceEvent(body.RoomID, data)
	}

	errors.Success(c, gin.H{
		"recording_id":          recording.ID,
		"started_at":            recording.StartedAt,
		"participants_notified": true,
	})
}

// StopRecording stops voice room recording
func (h *Handler) StopRecording(c *gin.Context) {
	var body struct {
		RoomID string `json:"roomId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	recording, err := h.service.StopRecording(body.RoomID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Broadcast recording stopped
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "recording_stopped",
			"payload": map[string]interface{}{
				"room_id":      body.RoomID,
				"channel_id":   body.RoomID, // backward compatibility
				"recording_id": recording.ID,
			},
		})
		h.broadcastVoiceEvent(body.RoomID, data)
	}

	errors.Success(c, gin.H{
		"recording_id":     recording.ID,
		"file_url":         recording.FileURL,
		"duration_seconds": 0,
		"file_size_mb":     0,
	})
}

// GetRecordings returns recording history for a room
func (h *Handler) GetRecordings(c *gin.Context) {
	roomID := c.Query("roomId")
	recordings, err := h.service.GetRecordings(roomID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"recordings": recordings})
}
