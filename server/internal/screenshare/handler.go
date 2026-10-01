package screenshare

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

// Handler handles screenshare HTTP requests
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
}

// NewHandler creates a new screenshare handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	svc := NewService(db, cfg)
	svc.SetHub(hub)
	return &Handler{service: svc, db: db, cfg: cfg, hub: hub}
}

// RegisterRoutes registers screenshare routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	ss := r.Group("/screenshare")
	ss.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		ss.POST("/start", h.Start)
		ss.POST("/stop", h.Stop)
		ss.POST("/pause", h.Pause)
		ss.POST("/resume", h.Resume)
		ss.GET("/status", h.GetChannelStatus)
		// Owner/Admin can force-stop an active screenshare by session ID.
		ss.POST("/:id/kick", h.ForceStop)

		// Legacy session-scoped endpoints retained for inspection.
		ss.GET("/sessions", h.GetSessions)
		ss.GET("/sessions/active", h.GetActiveSessions)
		ss.GET("/sessions/:sessionId", h.GetSessionStatus)
	}
}

// GetSessions returns active screenshare sessions in a space.
func (h *Handler) GetSessions(c *gin.Context) {
	h.getActiveSessions(c)
}

// GetActiveSessions returns active screenshare sessions in a space.
func (h *Handler) GetActiveSessions(c *gin.Context) {
	h.getActiveSessions(c)
}

func (h *Handler) getActiveSessions(c *gin.Context) {
	spaceID := c.Query("spaceId")
	if spaceID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	inSpace, err := h.service.IsUserInSpace(userID, spaceID)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if !inSpace {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	sessions, err := h.service.GetActiveSessionsBySpace(spaceID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{"sessions": sessions})
}

// GetSessionStatus returns a single screenshare session by ID.
func (h *Handler) GetSessionStatus(c *gin.Context) {
	sessionID := c.Param("sessionId")
	if sessionID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	session, err := h.service.GetSessionByID(sessionID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	userID := middleware.GetUserID(c)
	canManage, err := h.service.CanUserManageSession(userID, middleware.IsAdmin(c), session)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if !canManage {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	status, err := h.service.GetStatus(sessionID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, status)
}

// GetChannelStatus returns the current screenshare state for a channel.
func (h *Handler) GetChannelStatus(c *gin.Context) {
	channelID := c.Query("channelId")
	if channelID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	var channel model.Channel
	if err := h.db.First(&channel, "id = ?", channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	userID := middleware.GetUserID(c)
	inSpace, err := h.service.IsUserInSpace(userID, channel.SpaceID)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if !inSpace {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	status, err := h.service.GetStatusByChannel(channelID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, status)
}

// Start creates a new screenshare session bound to a voice channel.
func (h *Handler) Start(c *gin.Context) {
	var body struct {
		ChannelID     string `json:"channelId"`
		ShareType     string `json:"shareType"`
		SourceID      string `json:"sourceId"`
		Resolution    string `json:"resolution"`
		FrameRate     int    `json:"frameRate"`
		MaxBitrate    int    `json:"maxBitrate"`
		ShareAudio    bool   `json:"shareAudio"`
		SuppressVoice bool   `json:"suppressVoice"`
		MaxViewers    int    `json:"maxViewers"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if body.ChannelID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// Web clients are not allowed to start screen shares.
	clientType := c.GetHeader("X-Client-Type")
	if clientType == "web" {
		errors.JSONError(c, errors.New(errors.SCREENSHARE_WEB_FORBIDDEN, "screen sharing is not allowed on web client"))
		return
	}

	var channel model.Channel
	if err := h.db.First(&channel, "id = ?", body.ChannelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	userID := middleware.GetUserID(c)

	canStart, err := h.service.CanUserStartScreenShare(userID, channel.SpaceID)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if !canStart {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	req := StartRequest{
		ShareType:     body.ShareType,
		SourceID:      body.SourceID,
		Resolution:    body.Resolution,
		FrameRate:     body.FrameRate,
		MaxBitrate:    body.MaxBitrate,
		ShareAudio:    body.ShareAudio,
		SuppressVoice: body.SuppressVoice,
		MaxViewers:    body.MaxViewers,
	}
	sessionID, err := h.service.Start(userID, channel.SpaceID, body.ChannelID, req)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	status, err := h.service.GetStatusByChannel(body.ChannelID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	h.broadcastScreenShareState(body.ChannelID, status, true)

	errors.Success(c, gin.H{"sessionId": sessionID})
}

// Stop ends the active screenshare on a channel.
func (h *Handler) Stop(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if body.ChannelID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)

	session, err := h.service.GetActiveSessionByChannel(body.ChannelID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	state := h.service.toSessionResponse(session)

	if err := h.service.StopByChannel(userID, body.ChannelID); err != nil {
		errors.JSONError(c, err)
		return
	}

	h.broadcastScreenShareState(body.ChannelID, state, false)
	errors.Success(c, gin.H{"message": "screen share stopped"})
}

// Pause pauses the active screenshare on a channel.
func (h *Handler) Pause(c *gin.Context) {
	h.changeChannelState(c, "pause")
}

// Resume resumes the active screenshare on a channel.
func (h *Handler) Resume(c *gin.Context) {
	h.changeChannelState(c, "resume")
}

func (h *Handler) changeChannelState(c *gin.Context, action string) {
	var body struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if body.ChannelID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)

	session, err := h.service.GetActiveSessionByChannel(body.ChannelID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	state := h.service.toSessionResponse(session)

	var opErr error
	switch action {
	case "pause":
		opErr = h.service.PauseByChannel(userID, body.ChannelID)
	case "resume":
		opErr = h.service.ResumeByChannel(userID, body.ChannelID)
	}
	if opErr != nil {
		errors.JSONError(c, opErr)
		return
	}

	h.broadcastScreenShareState(body.ChannelID, state, true)
	errors.Success(c, gin.H{"message": "screen share " + action + "d"})
}

// ForceStop allows Owner/Admin to force-stop an active screenshare.
func (h *Handler) ForceStop(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)

	session, err := h.service.GetSessionByID(sessionID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Only OWNER, ADMIN (space-level) or global admin/owner can force-stop.
	isGlobalAdmin := middleware.IsAdmin(c)
	isSpaceAdmin, err := h.service.IsUserSpaceAdmin(userID, session.SpaceID)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if !isGlobalAdmin && !isSpaceAdmin {
		errors.JSONError(c, errors.New(errors.SCREENSHARE_FORCE_STOP_FORBIDDEN, "only admin/owner can force-stop screen share"))
		return
	}

	state := h.service.toSessionResponse(session)

	if err := h.service.ForceStopByChannel(userID, session.ChannelID); err != nil {
		errors.JSONError(c, err)
		return
	}

	h.broadcastScreenShareState(session.ChannelID, state, false)
	errors.Success(c, gin.H{"message": "screen share force-stopped"})
}

// broadcastScreenShareState emits a screenshare_state event to the channel.
func (h *Handler) broadcastScreenShareState(channelID string, session *Session, active bool) {
	if h.hub == nil || channelID == "" {
		return
	}

	payload := map[string]interface{}{
		"active":        active,
		"channelId":     channelID,
		"userId":        "",
		"username":      "",
		"shareType":     "",
		"sourceId":      "",
		"resolution":    "",
		"frameRate":     0,
		"maxBitrate":    0,
		"shareAudio":    false,
		"suppressVoice": false,
		"viewerCount":   0,
		"maxViewers":    0,
		"status":        "stopped",
	}
	if session != nil {
		payload["userId"] = session.UserID
		payload["username"] = session.Username
		payload["shareType"] = session.ShareType
		payload["sourceId"] = session.SourceID
		payload["resolution"] = session.Resolution
		payload["frameRate"] = session.FrameRate
		payload["maxBitrate"] = session.MaxBitrate
		payload["shareAudio"] = session.ShareAudio
		payload["suppressVoice"] = session.SuppressVoice
		payload["viewerCount"] = session.ViewerCount
		payload["maxViewers"] = session.MaxViewers
		payload["status"] = session.Status
	}

	data, _ := json.Marshal(map[string]interface{}{
		"type":    "screenshare_state",
		"payload": payload,
	})
	h.hub.BroadcastToChannel(channelID, data)
}
