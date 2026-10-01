package remoteassist

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/admin"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles remote assist HTTP requests.
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
}

// NewHandler creates a new remote assist handler.
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub, auditSvc *admin.Service) *Handler {
	return &Handler{
		service: NewService(db, hub, auditSvc),
		db:      db,
		cfg:     cfg,
		hub:     hub,
	}
}

// ValidateControlEvent authorizes a WebSocket remote control event before the
// realtime hub forwards it to the target (H14). It is wired into the hub in
// internal/server/routes.go; see Service.ValidateControlEvent for the rules.
func (h *Handler) ValidateControlEvent(sessionID, requesterID, targetID string) error {
	return h.service.ValidateControlEvent(sessionID, requesterID, targetID)
}

// ValidateFromTarget authorizes a clipboard pull-data reply sent by the
// session target before the hub relays it to the requester (A6-S1,
// DES-20261001-01 §7.2). Wired into the hub in internal/server/routes.go;
// see Service.ValidateFromTarget for the rules. Returns the requester ID so
// the hub can route without a second lookup.
func (h *Handler) ValidateFromTarget(sessionID, senderID string) (string, error) {
	return h.service.ValidateFromTarget(sessionID, senderID)
}

// ValidateChatParticipant authorizes an in-session chat message before the
// hub relays it to both parties (A7-S1, DES-20261001-01 §8.1). Wired into the
// hub in internal/server/routes.go; see Service.ValidateChatParticipant for
// the rules. Returns the peer (the other party) ID on success.
func (h *Handler) ValidateChatParticipant(sessionID, senderID string) (string, error) {
	return h.service.ValidateChatParticipant(sessionID, senderID)
}

// RegisterRoutes registers remote assist routes on the given router group.
// All routes require authentication via middleware.AuthRequired.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	ra := r.Group("/remote-assist")
	ra.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		ra.POST("/request", h.CreateRequest)
		ra.POST("/:id/authorize", h.Authorize)
		ra.POST("/:id/reject", h.Reject)
		ra.POST("/:id/end", h.End)
		ra.GET("/sessions", h.ListSessions)
	}
}

// CreateRequest handles POST /remote-assist/request.
// Body: { targetId, channelId, permissions: { mouse, keyboard, screen } }
// "screen" (画面回传) is optional; older clients omit it and it defaults to
// false, i.e. the requester still operates blind (DES-2026-0912-07 §4.1).
func (h *Handler) CreateRequest(c *gin.Context) {
	var body struct {
		TargetID    string      `json:"targetId"`
		ChannelID   string      `json:"channelId"`
		Permissions Permissions `json:"permissions"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if body.TargetID == "" || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.MISSING_REQUIRED_FIELD, "targetId and channelId are required"))
		return
	}

	requesterID := middleware.GetUserID(c)
	session, err := h.service.CreateRequest(requesterID, body.TargetID, body.ChannelID, body.Permissions)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Push the request to the target via WebSocket so their client can show
	// the authorization prompt with a 30-second countdown.
	if h.hub != nil {
		payload, _ := json.Marshal(map[string]interface{}{
			"type": "remote_assist_request",
			"payload": map[string]interface{}{
				"sessionId":   session.ID,
				"requesterId": requesterID,
				"channelId":   session.ChannelID,
				"permissions": session.Permissions,
				"expiresAt":   session.ExpiresAt,
			},
		})
		h.hub.BroadcastToUser(body.TargetID, payload)
	}

	errors.Success(c, gin.H{"session": session})
}

// Authorize handles POST /remote-assist/:id/authorize.
func (h *Handler) Authorize(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	targetID := middleware.GetUserID(c)

	session, err := h.service.Authorize(sessionID, targetID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Notify the requester that authorization was granted.
	if h.hub != nil {
		payload, _ := json.Marshal(map[string]interface{}{
			"type": "remote_assist_authorize",
			"payload": map[string]interface{}{
				"sessionId":   session.ID,
				"targetId":    targetID,
				"channelId":   session.ChannelID,
				"permissions": session.Permissions,
			},
		})
		h.hub.BroadcastToUser(session.RequesterID, payload)
	}

	errors.Success(c, gin.H{"session": session})
}

// Reject handles POST /remote-assist/:id/reject.
func (h *Handler) Reject(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	targetID := middleware.GetUserID(c)

	session, err := h.service.Reject(sessionID, targetID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Notify the requester that the request was rejected.
	if h.hub != nil {
		payload, _ := json.Marshal(map[string]interface{}{
			"type": "remote_assist_reject",
			"payload": map[string]interface{}{
				"sessionId": sessionID,
				"targetId":  targetID,
				"reason":    "rejected",
			},
		})
		h.hub.BroadcastToUser(session.RequesterID, payload)
	}

	errors.Success(c, gin.H{"session": session})
}

// End handles POST /remote-assist/:id/end.
// Either the requester or the target may end an active session.
func (h *Handler) End(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	userID := middleware.GetUserID(c)

	session, err := h.service.End(sessionID, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Notify the other party that the session has ended.
	if h.hub != nil {
		otherUserID := session.TargetID
		if userID == session.TargetID {
			otherUserID = session.RequesterID
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"type": "remote_assist_end",
			"payload": map[string]interface{}{
				"sessionId": sessionID,
				"endedBy":   userID,
				"reason":    reasonUserEnded,
				"status":    session.Status,
			},
		})
		h.hub.BroadcastToUser(otherUserID, payload)
	}

	errors.Success(c, gin.H{"session": session})
}

// ListSessions handles GET /remote-assist/sessions.
// Returns all active (pending or authorized) sessions involving the caller.
func (h *Handler) ListSessions(c *gin.Context) {
	userID := middleware.GetUserID(c)
	sessions, err := h.service.ListActiveSessions(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"sessions": sessions})
}
