package user

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles HTTP requests for users
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
}

// NewHandler creates a new user handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{service: NewService(db), db: db, cfg: cfg, hub: hub}
}

// RegisterRoutes registers user routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	users := r.Group("/users")
	users.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		users.GET("", h.GetUsers)
		users.GET("/me", h.GetMe)
		users.PATCH("/me", h.UpdateMe)
		users.GET("/:id", h.GetUser)
		users.PATCH("/:id", h.UpdateProfile)
		users.PUT("/:id/avatar", h.UpdateAvatar)
		users.POST("/:id/avatar-upload", h.UploadAvatar)
		users.PUT("/:id/status", h.UpdateStatus)
		users.GET("/:id/status", h.GetStatus)
		users.PUT("/:id/heartbeat", h.Heartbeat)
	}

	// Presence endpoint (optional auth)
	r.GET("/space/members", middleware.AuthRequired(h.cfg, h.db), h.GetMembers)
}

// validStatuses 允许的用户在线状态枚举值
var validStatuses = map[string]bool{
	"online":    true,
	"away":      true,
	"dnd":       true,
	"offline":   true,
	"invisible": true,
	"gaming":    true,
}

// GetUser retrieves a user by ID
func (h *Handler) GetUser(c *gin.Context) {
	userID := c.Param("id")
	if userID == "me" {
		userID = middleware.GetUserID(c)
	}
	user, err := h.service.GetUser(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	user.PasswordHash = ""
	errors.Success(c, gin.H{
		"id":            user.ID,
		"username":      user.Username,
		"email":         user.Email,
		"avatar":        user.Avatar,
		"displayName":   user.DisplayName,
		"customStatus":  user.CustomStatus,
		"role":          user.Role,
		"isActive":      user.IsActive,
		"tokenVersion":  user.TokenVersion,
		"emailVerified": user.EmailVerified,
		"theme":         user.Theme,
		"lastLoginAt":   user.LastLoginAt,
		"createdAt":     user.CreatedAt,
		"updatedAt":     user.UpdatedAt,
	})
}

// GetUsers retrieves a paginated list of users
func (h *Handler) GetUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))

	users, total, err := h.service.GetUsers(page, pageSize)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Hide password hashes
	for i := range users {
		users[i].PasswordHash = ""
	}

	errors.Success(c, gin.H{
		"items": users,
		"total": total,
		"page":  page,
		"size":  pageSize,
	})
}

// UpdateProfile updates the current user's profile
func (h *Handler) UpdateProfile(c *gin.Context) {
	currentUserID := middleware.GetUserID(c)
	targetID := c.Param("id")

	// "me" is an alias for the current authenticated user
	if targetID == "me" {
		targetID = currentUserID
	}

	// Users can only update their own profile
	if currentUserID != targetID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	var body struct {
		DisplayName  string `json:"displayName"`
		CustomStatus string `json:"customStatus"`
		Theme        string `json:"theme"`
		Email        string `json:"email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// Update email if provided
	if body.Email != "" {
		if err := h.service.UpdateEmail(targetID, body.Email); err != nil {
			errors.JSONError(c, err)
			return
		}
	}

	updates := map[string]interface{}{}
	if body.DisplayName != "" {
		updates["display_name"] = body.DisplayName
	}
	if body.CustomStatus != "" {
		updates["custom_status"] = body.CustomStatus
	}
	if body.Theme != "" {
		updates["theme"] = body.Theme
	}

	if len(updates) > 0 {
		if err := h.service.UpdateProfile(targetID, updates); err != nil {
			// FIX-2026-0808-01: pass through the service error so validation
			// errors like AUTH_DISPLAY_NAME_EXISTS keep their HTTP status.
			errors.JSONError(c, err)
			return
		}
	}

	errors.Success(c, gin.H{"message": "profile updated"})
}

// GetMe returns the current authenticated user
func (h *Handler) GetMe(c *gin.Context) {
	userID := middleware.GetUserID(c)
	user, err := h.service.GetUser(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	user.PasswordHash = ""
	errors.Success(c, user)
}

// UpdateMe updates the current authenticated user's profile
func (h *Handler) UpdateMe(c *gin.Context) {
	userID := middleware.GetUserID(c)
	var body struct {
		DisplayName  string `json:"displayName"`
		CustomStatus string `json:"customStatus"`
		Theme        string `json:"theme"`
		Email        string `json:"email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// Update email if provided
	if body.Email != "" {
		if err := h.service.UpdateEmail(userID, body.Email); err != nil {
			errors.JSONError(c, err)
			return
		}
	}

	updates := map[string]interface{}{}
	if body.DisplayName != "" {
		updates["display_name"] = body.DisplayName
	}
	if body.CustomStatus != "" {
		updates["custom_status"] = body.CustomStatus
	}
	if body.Theme != "" {
		updates["theme"] = body.Theme
	}

	if len(updates) > 0 {
		if err := h.service.UpdateProfile(userID, updates); err != nil {
			// FIX-2026-0808-01: pass through the service error (e.g. display-name conflict).
			errors.JSONError(c, err)
			return
		}
	}

	errors.Success(c, gin.H{"message": "profile updated"})
}

// UpdateAvatar updates user avatar
func (h *Handler) UpdateAvatar(c *gin.Context) {
	currentUserID := middleware.GetUserID(c)
	targetID := c.Param("id")
	if targetID == "me" {
		targetID = currentUserID
	}

	if currentUserID != targetID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	var body struct {
		Avatar string `json:"avatar"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if err := h.service.UpdateAvatar(targetID, body.Avatar); err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	errors.Success(c, gin.H{"message": "avatar updated"})
}

// UploadAvatar handles multipart avatar upload
func (h *Handler) UploadAvatar(c *gin.Context) {
	currentUserID := middleware.GetUserID(c)
	targetID := c.Param("id")
	if targetID == "me" {
		targetID = currentUserID
	}

	if currentUserID != targetID && !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	file, header, err := c.Request.FormFile("avatar")
	if err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("avatar file required"))
		return
	}
	defer file.Close()

	baseURL := h.cfg.PublicAddress
	if baseURL == "" {
		baseURL = "http://localhost:" + fmt.Sprintf("%d", h.cfg.Port)
	}

	avatarURL, err := h.service.SaveAvatar(targetID, header, baseURL)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Update avatar URL in database
	if err := h.service.UpdateAvatar(targetID, avatarURL); err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{"avatar": avatarURL})
}

// GetStatus returns the online status of a user.
func (h *Handler) GetStatus(c *gin.Context) {
	currentUserID := middleware.GetUserID(c)
	targetID := c.Param("id")
	if targetID == "me" {
		targetID = currentUserID
	}

	// Any authenticated user may read any other user's status; this matches the
	// existing presence broadcast behavior.
	presence, err := h.service.GetPresence(targetID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"userId":       presence.UserID,
		"status":       presence.Status,
		"customStatus": presence.CustomStatus,
		"lastSeenAt":   presence.LastSeenAt,
	})
}

// UpdateStatus updates user online status
func (h *Handler) UpdateStatus(c *gin.Context) {
	currentUserID := middleware.GetUserID(c)
	targetID := c.Param("id")
	if targetID == "me" {
		targetID = currentUserID
	}

	if currentUserID != targetID {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	var body struct {
		Status       string `json:"status"`
		CustomStatus string `json:"customStatus"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// 枚举校验：拒绝非法 status 值
	if body.Status != "" && !validStatuses[body.Status] {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails(
			"invalid status, must be one of: online, away, dnd, offline, invisible, gaming"))
		return
	}

	// 获取旧状态用于判断是否需要广播
	oldPresence, _ := h.service.GetPresence(targetID)
	oldStatus := "offline"
	if oldPresence != nil {
		oldStatus = oldPresence.Status
	}

	if err := h.service.UpdateStatus(targetID, body.Status, body.CustomStatus); err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// 仅在 status 实际变化时广播，避免心跳等无变化场景产生冗余广播
	if h.hub != nil && body.Status != oldStatus {
		// If the user explicitly comes online or goes offline via the HTTP API,
		// cancel any pending graceful-offline timer to avoid duplicate broadcasts.
		if body.Status == "online" || body.Status == "offline" {
			h.hub.CancelGracefulOffline(targetID)
		}

		broadcastStatus := body.Status
		if broadcastStatus == "invisible" {
			broadcastStatus = "offline"
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type": "presence_update",
			"payload": map[string]interface{}{
				"userId":       targetID,
				"status":       broadcastStatus,
				"customStatus": body.CustomStatus,
			},
		})
		h.hub.Broadcast(data)
	}

	errors.Success(c, gin.H{"message": "status updated"})
}

// Heartbeat 仅刷新 LastSeenAt，不改变状态，不触发广播。
// 供客户端定期心跳调用，避免每次心跳都产生 presence_update 全局广播。
func (h *Handler) Heartbeat(c *gin.Context) {
	currentUserID := middleware.GetUserID(c)
	targetID := c.Param("id")
	if targetID == "me" {
		targetID = currentUserID
	}

	if currentUserID != targetID {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	if err := h.service.TouchLastSeen(targetID); err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	errors.Success(c, gin.H{"message": "heartbeat ok"})
}

// GetMembers returns all space members with their presence.
// It bypasses the paginated GetUsers so the full member list is available to
// the client for real-time presence rendering.
func (h *Handler) GetMembers(c *gin.Context) {
	var users []model.User
	if err := h.db.Find(&users).Error; err != nil {
		errors.JSONError(c, err)
		return
	}

	presences, err := h.service.GetAllPresences()
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	presenceMap := make(map[string]*model.UserPresence)
	for i := range presences {
		presenceMap[presences[i].UserID] = &presences[i]
	}

	type MemberResponse struct {
		UserID       string `json:"userId"`
		Username     string `json:"username"`
		DisplayName  string `json:"displayName"`
		Avatar       string `json:"avatar"`
		Role         string `json:"role"`
		Status       string `json:"status"`
		CustomStatus string `json:"customStatus"`
	}

	members := make([]MemberResponse, 0, len(users))
	for _, u := range users {
		presence := presenceMap[u.ID]
		stat := "offline"
		customStatus := ""
		if presence != nil {
			stat = presence.Status
			customStatus = presence.CustomStatus
		}
		displayName := u.DisplayName
		if displayName == "" {
			displayName = u.Username
		}
		members = append(members, MemberResponse{
			UserID:       u.ID,
			Username:     u.Username,
			DisplayName:  displayName,
			Avatar:       u.Avatar,
			Role:         u.Role,
			Status:       stat,
			CustomStatus: customStatus,
		})
	}

	errors.Success(c, members)
}
