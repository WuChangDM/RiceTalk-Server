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

// GetE2EEKey returns the E2EE key for a room (or generates one if it doesn't exist)
func (h *Handler) GetE2EEKey(c *gin.Context) {
	roomID := c.Query("roomId")
	if roomID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("roomId is required"))
		return
	}
	userID := middleware.GetUserID(c)
	key, err := h.service.GetE2EEKey(roomID, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"key": key})
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
