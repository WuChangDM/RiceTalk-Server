package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/version"
	"ridgericetalk/features/cloudfs"
	"ridgericetalk/internal/bots"
	"ridgericetalk/internal/channel"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/network"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/serverstate"
	"ridgericetalk/middleware"
)

// Handler handles HTTP requests for admin
type Handler struct {
	service    *Service
	db         *gorm.DB
	cfg        *config.Config
	hub        *realtime.Hub
	breakGlass *middleware.OwnerBreakGlassProtector // M9: Owner Break-Glass protection
	stateMgr   *serverstate.Manager
	detector   *network.Detector
	channelSvc *channel.Service // C-3: admin channel management
	cloudfsSvc *cloudfs.Service // E4: admin 全局分享列表/撤销
	// restartFunc executes a systemctl restart for the named service. It
	// defaults to exec.CommandContext-based restart and is overridable for
	// tests. (admin-port-config-panel Task 6)
	restartFunc func(ctx context.Context, service string) error
}

// NewHandler creates a new admin handler without a serverstate.Manager. This
// constructor is kept for tests and legacy callers; production code should use
// NewHandlerWithState.
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub, breakGlass *middleware.OwnerBreakGlassProtector) *Handler {
	return NewHandlerWithState(db, cfg, hub, breakGlass, nil)
}

// NewHandlerWithState creates a new admin handler with the server state manager.
func NewHandlerWithState(db *gorm.DB, cfg *config.Config, hub *realtime.Hub, breakGlass *middleware.OwnerBreakGlassProtector, stateMgr *serverstate.Manager) *Handler {
	return NewHandlerWithService(db, cfg, hub, breakGlass, stateMgr, nil)
}

// NewHandlerWithService creates an admin handler using the supplied service.
// The application reuses one service across all compatible admin route groups
// so runtime statistics and repository ownership stay process-scoped.
func NewHandlerWithService(db *gorm.DB, cfg *config.Config, hub *realtime.Hub, breakGlass *middleware.OwnerBreakGlassProtector, stateMgr *serverstate.Manager, service *Service) *Handler {
	if service == nil {
		service = NewService(db, cfg, hub)
	}
	return &Handler{
		service:     service,
		db:          db,
		cfg:         cfg,
		hub:         hub,
		breakGlass:  breakGlass,
		stateMgr:    stateMgr,
		detector:    network.NewDetector(),
		channelSvc:  channel.NewService(db),
		cloudfsSvc:  cloudfs.NewService(db, cfg),
		restartFunc: defaultRestartFunc,
	}
}

// RegisterRoutes registers admin routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	admin := r.Group("/admin")
	{
		admin.GET("/config", h.GetConfig)
		admin.PUT("/config", h.UpdateConfig)
		admin.GET("/users", h.GetUsers)
		admin.PUT("/users/:uid/role", h.UpdateUserRole)
		admin.PATCH("/users/:uid/status", h.UpdateUserStatus)
		admin.POST("/users/:uid/reset-password", h.ResetUserPassword)
		admin.DELETE("/users/:uid", h.DeleteUser)
		admin.GET("/storage", h.GetStorage)
		admin.GET("/modules", h.GetModules)
		admin.POST("/modules/:module/actions", h.ModuleAction)
		admin.GET("/runtime", h.GetRuntime)
		admin.GET("/system/usage", h.GetSystemUsage)
		admin.GET("/audit-logs", h.GetAuditLogs)
		admin.GET("/audit-logs/export", h.ExportAuditLogs)
		admin.GET("/security-audit-logs", h.GetSecurityAuditLogs)
		// A11 (DES-20261001-01 §12.3): 监控告警列表与静默
		admin.GET("/alerts", h.GetAlerts)
		admin.POST("/alerts/:id/mute", h.MuteAlert)
		// C-3: Admin channel management
		admin.GET("/channels", h.AdminListChannels)
		admin.POST("/channels", h.AdminCreateChannel)
		admin.PATCH("/channels/:channelId", h.AdminUpdateChannel)
		admin.DELETE("/channels/:channelId", h.AdminDeleteChannel)
		admin.POST("/channels/:channelId/transfer", h.TransferChannelOwnership)
		// H2: channel permission management UI
		admin.GET("/channels/:channelId/permissions", h.GetChannelPermissions)
		admin.PUT("/channels/:channelId/permissions", h.UpdateChannelPermissions)
		// E4: admin 全局分享管理（跨空间列表 + 撤销）
		admin.GET("/cloudfs/shares", h.AdminListCloudShares)
		admin.DELETE("/cloudfs/shares/:id", h.AdminRevokeCloudShare)
		admin.GET("/update/check", h.CheckUpdate)
		admin.GET("/network", h.GetNetwork)
		admin.PUT("/network", h.UpdateNetwork)
		admin.GET("/network/detect", h.DetectNetwork)
		admin.POST("/network/verify", h.VerifyNetwork)
		// admin-port-config-panel Task 5: external port metadata list
		admin.GET("/ports", h.GetPorts)
		// admin-port-config-panel Task 6: Owner-only service restart (Break-Glass protected)
		admin.POST("/service/restart", h.RestartService)
		// TTS engine status and re-initialization
		admin.GET("/tts/status", h.GetTTSStatus)
		admin.POST("/tts/reinit", h.ReinitTTS)
	}
}

// checkBreakGlass verifies that the current Owner user is not locked out by
// the Break-Glass protector (M9, design §9.7). Returns true if the request
// may proceed, false if it was aborted with AUTH_ACCOUNT_LOCKED.
// Non-Owner users always pass — Break-Glass only applies to the Owner account.
func (h *Handler) checkBreakGlass(c *gin.Context) bool {
	if h.breakGlass == nil {
		return true
	}
	role := middleware.GetRole(c)
	if role != middleware.RoleOwner {
		return true
	}
	userID := middleware.GetUserID(c)
	if userID == "" {
		return true
	}
	if allowed, remaining := h.breakGlass.Allow(userID); !allowed {
		lockErr := errors.New(errors.AUTH_ACCOUNT_LOCKED,
			"Owner account locked due to repeated sensitive operation failures; try again in "+remaining.String())
		errors.JSONError(c, lockErr)
		return false
	}
	return true
}

// recordBreakGlassFailure records a failed sensitive operation for the current
// Owner user (M9). Call this when a sensitive operation is rejected due to
// invalid confirmation.
func (h *Handler) recordBreakGlassFailure(c *gin.Context) {
	if h.breakGlass == nil {
		return
	}
	role := middleware.GetRole(c)
	if role != middleware.RoleOwner {
		return
	}
	userID := middleware.GetUserID(c)
	if userID == "" {
		return
	}
	h.breakGlass.RecordFailure(userID)
}

// resetBreakGlass clears the Break-Glass failure count for the current Owner
// user after a successful sensitive operation (M9).
func (h *Handler) resetBreakGlass(c *gin.Context) {
	if h.breakGlass == nil {
		return
	}
	role := middleware.GetRole(c)
	if role != middleware.RoleOwner {
		return
	}
	userID := middleware.GetUserID(c)
	if userID == "" {
		return
	}
	h.breakGlass.Reset(userID)
}

// GetConfig returns server configuration
func (h *Handler) GetConfig(c *gin.Context) {
	config, err := h.service.GetConfig()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, config)
}

// UpdateConfig updates server configuration
func (h *Handler) UpdateConfig(c *gin.Context) {
	// M9: Break-Glass check for sensitive operation (modifying server config / ports)
	if !h.checkBreakGlass(c) {
		return
	}

	var body struct {
		ServerName    string `json:"serverName"`
		AllowRegister *bool  `json:"allowRegister"`
		MaxUsers      *int   `json:"maxUsers"`
		PublicAddress string `json:"publicAddress"`
		MaxFileSize   *int   `json:"maxFileSize"`
		MaxStorageGB  *int   `json:"maxStorageGB"`
		// P0-4: 基本配置的端口输入框此前被静默忽略（body 未映射这些字段），
		// 现接入并转发到 service 的 allowlist（api_port / live_kit_port / vpn_port）。
		APIPort     *int `json:"apiPort"`
		LiveKitPort *int `json:"livekitPort"`
		VPNPort     *int `json:"vpnPort"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	updates := map[string]interface{}{}
	if body.ServerName != "" {
		updates["server_name"] = body.ServerName
	}
	if body.AllowRegister != nil {
		updates["allow_register"] = *body.AllowRegister
	}
	if body.MaxUsers != nil {
		updates["max_users"] = *body.MaxUsers
	}
	if body.PublicAddress != "" {
		updates["public_address"] = body.PublicAddress
	}
	if body.MaxFileSize != nil {
		if *body.MaxFileSize < 0 {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("maxFileSize must be >= 0"))
			return
		}
		updates["max_file_size"] = *body.MaxFileSize
	}
	if body.MaxStorageGB != nil {
		if *body.MaxStorageGB < 0 {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("maxStorageGB must be >= 0"))
			return
		}
		updates["max_storage_gb"] = *body.MaxStorageGB
	}
	// P0-4: 端口字段校验（1-65535），再转发到 service。
	if body.APIPort != nil {
		if *body.APIPort < 1 || *body.APIPort > 65535 {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("apiPort must be between 1 and 65535"))
			return
		}
		updates["api_port"] = *body.APIPort
	}
	if body.LiveKitPort != nil {
		if *body.LiveKitPort < 1 || *body.LiveKitPort > 65535 {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("livekitPort must be between 1 and 65535"))
			return
		}
		updates["live_kit_port"] = *body.LiveKitPort
	}
	if body.VPNPort != nil {
		if *body.VPNPort < 1 || *body.VPNPort > 65535 {
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("vpnPort must be between 1 and 65535"))
			return
		}
		updates["vpn_port"] = *body.VPNPort
	}

	if err := h.service.UpdateConfig(updates); err != nil {
		errors.JSONError(c, err)
		return
	}

	// M9: reset Break-Glass on successful sensitive operation
	h.resetBreakGlass(c)

	h.service.LogAudit(
		middleware.GetUserID(c), "update_config", "server",
		"", c.ClientIP(), c.Request.UserAgent(), true,
	)

	errors.Success(c, gin.H{"message": "config updated"})
}

// GetUsers returns all users for admin
func (h *Handler) GetUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	keyword := c.Query("keyword")

	users, total, err := h.service.GetUsers(page, pageSize, keyword)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// Build per-user online status from the realtime hub.
	onlineSet := map[string]bool{}
	if h.hub != nil {
		for _, id := range h.hub.OnlineUserIDs() {
			onlineSet[id] = true
		}
	}

	items := make([]map[string]interface{}, len(users))
	for i, u := range users {
		u.PasswordHash = ""
		var m map[string]interface{}
		b, _ := json.Marshal(u)
		_ = json.Unmarshal(b, &m)
		m["online"] = onlineSet[u.ID]
		items[i] = m
	}

	errors.Success(c, gin.H{
		"items":   items,
		"total":   total,
		"page":    page,
		"size":    pageSize,
		"summary": h.service.GetUserSummary(total),
	})
}

// GetStorage returns storage statistics
func (h *Handler) GetStorage(c *gin.Context) {
	storage, err := h.service.GetStorage()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, storage)
}

// GetModules returns module status
func (h *Handler) GetModules(c *gin.Context) {
	modules, err := h.service.GetModulesStatus()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{
		"items": modules,
	})
}

// ModuleAction handles module enable/disable actions
func (h *Handler) ModuleAction(c *gin.Context) {
	moduleName := c.Param("module")
	var body struct {
		Action string `json:"action"` // enable or disable
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	enabled := body.Action == "enable"
	if err := h.service.ToggleModule(moduleName, enabled, middleware.GetUserID(c)); err != nil {
		errors.JSONError(c, err)
		return
	}

	// C2/H29: audit log for module toggle
	h.service.LogAudit(
		middleware.GetUserID(c), "module_action", "module",
		moduleName, c.ClientIP(), c.Request.UserAgent(), true,
	)
	_ = h.service.LogSecurityEvent(
		middleware.GetUserID(c), "module_action", "module", moduleName,
		c.ClientIP(), c.Request.UserAgent(), map[string]interface{}{"action": body.Action, "enabled": enabled},
	)

	// H28: Broadcast module status change to all clients so frontend can show countdown banner
	if h.hub != nil {
		var gracePeriodEndsAt interface{} = nil
		if !enabled {
			// ToggleModule sets 5-minute grace period when disabling
			gracePeriodEndsAt = time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
		}
		data, _ := json.Marshal(map[string]interface{}{
			"type": "module_status_changed",
			"payload": map[string]interface{}{
				"module":            moduleName,
				"enabled":           enabled,
				"gracePeriodEndsAt": gracePeriodEndsAt,
			},
		})
		h.hub.Broadcast(data)
	}

	errors.Success(c, gin.H{
		"message": "module " + body.Action + "d",
		"module":  moduleName,
		"enabled": enabled,
	})
}

// GetRuntime returns runtime performance metrics
func (h *Handler) GetRuntime(c *gin.Context) {
	runtime, err := h.service.GetRuntime()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, runtime)
}

// GetSystemUsage returns system disk usage
func (h *Handler) GetSystemUsage(c *gin.Context) {
	usage, err := h.service.GetSystemUsage()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, usage)
}

// GetAuditLogs returns audit logs
func (h *Handler) GetAuditLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))

	logs, total, err := h.service.GetAuditLogs(page, pageSize)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"items": logs,
		"total": total,
		"page":  page,
		"size":  pageSize,
	})
}

// GetSecurityAuditLogs returns security audit logs with optional filtering.
// C-5: SecurityAuditLog query interface.
func (h *Handler) GetSecurityAuditLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "50"))
	eventType := c.Query("eventType")
	userID := c.Query("userId")

	logs, total, err := h.service.GetSecurityAuditLogs(page, pageSize, eventType, userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"items": logs,
		"total": total,
		"page":  page,
		"size":  pageSize,
	})
}

// ExportAuditLogs exports audit logs as CSV.
// C-6: Audit log export.
func (h *Handler) ExportAuditLogs(c *gin.Context) {
	logs, err := h.service.GetAllAuditLogsForExport(10000)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=audit-logs.csv")

	writer := csv.NewWriter(c.Writer)
	// BOM for Excel UTF-8 compatibility
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	writer.Write([]string{"ID", "UserID", "Action", "Resource", "Details", "IPAddress", "UserAgent", "Success", "CreatedAt"})
	for _, log := range logs {
		success := "false"
		if log.Success {
			success = "true"
		}
		writer.Write([]string{
			log.ID,
			log.UserID,
			log.Action,
			log.Resource,
			log.Details,
			log.IPAddress,
			log.UserAgent,
			success,
			log.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writer.Flush()
}

// CheckUpdate reports that server update checking is not implemented.
//
// P1-3: 此前该接口无条件返回 update_available=false 且 latest_version=当前版本，
// 并附带"当前已是最新版本"的说明——服务端并没有接入任何发布渠道/更新源，
// 这只是编造出来的结论。现在显式返回 implemented=false，latest_version 留空，
// 管理 UI 也不再展示"版本更新"卡片，避免长期暗示"已是最新版本"。
//
// 字段名保持与 openapi.yaml /api/v1/admin/update/check 的既有 schema 兼容，
// 待真正接入更新源后再补全语义。
//
// GET /api/admin/update/check
func (h *Handler) CheckUpdate(c *gin.Context) {
	errors.Success(c, gin.H{
		"implemented":      false,
		"update_available": false,
		"current_version":  version.Server,
		"latest_version":   "",
		"severity":         "none",
		"breaking_changes": false,
		"changelog":        "版本更新检查尚未实现：服务端当前未接入发布渠道/更新源",
		"published_at":     nil,
	})
}

// DeleteUser deletes a user (cannot delete owner)
func (h *Handler) DeleteUser(c *gin.Context) {
	// M9: Break-Glass check for sensitive operation (deleting member)
	if !h.checkBreakGlass(c) {
		return
	}

	userID := c.Param("uid")

	// Check if user exists
	var user model.User
	if err := h.db.First(&user, "id = ?", userID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.ErrNotFound)
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// Cannot delete owner
	if user.Role == middleware.RoleOwner {
		errors.JSONError(c, errors.New(errors.ADMIN_CANNOT_DELETE_OWNER, "cannot delete the server owner"))
		return
	}

	if err := h.db.Delete(&model.User{}, "id = ?", userID).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// M9: reset Break-Glass on successful sensitive operation
	h.resetBreakGlass(c)

	// C2/H29: audit log for user deletion
	h.service.LogAudit(
		middleware.GetUserID(c), "delete_user", "user",
		userID, c.ClientIP(), c.Request.UserAgent(), true,
	)
	_ = h.service.LogSecurityEvent(
		middleware.GetUserID(c), "user_deleted", "user", userID,
		c.ClientIP(), c.Request.UserAgent(), map[string]interface{}{"deletedUsername": user.Username, "deletedRole": user.Role},
	)

	errors.Success(c, gin.H{"message": "user deleted"})
}

// UpdateUserRole updates a user's role (OWNER/ADMIN/MEMBER)
func (h *Handler) UpdateUserRole(c *gin.Context) {
	// M9: Break-Glass check for sensitive operation (modifying user role)
	if !h.checkBreakGlass(c) {
		return
	}

	userID := c.Param("uid")

	// H4: defensive check - cannot change own role
	if userID == middleware.GetUserID(c) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CANNOT_CHANGE_OWN_ROLE", "message": "不能修改自己的角色"})
		return
	}

	var body struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Role == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("role required"))
		return
	}

	if err := h.service.UpdateUserRole(userID, body.Role, middleware.GetUserID(c), c.ClientIP(), c.Request.UserAgent()); err != nil {
		errors.JSONError(c, err)
		return
	}

	// M9: reset Break-Glass on successful sensitive operation
	h.resetBreakGlass(c)

	// C2/H29: security audit log for role change
	_ = h.service.LogSecurityEvent(
		middleware.GetUserID(c), "role_changed", "user", userID,
		c.ClientIP(), c.Request.UserAgent(), map[string]interface{}{"newRole": body.Role},
	)

	errors.Success(c, gin.H{"message": "role updated"})
}

// UpdateUserStatus enables or disables a user account (C38).
// Disabling a user immediately revokes all their existing tokens.
func (h *Handler) UpdateUserStatus(c *gin.Context) {
	// M9: Break-Glass check for sensitive operation (modifying user status)
	if !h.checkBreakGlass(c) {
		return
	}

	userID := c.Param("uid")
	var body struct {
		IsActive *bool `json:"isActive"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.IsActive == nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("isActive required"))
		return
	}

	if err := h.service.UpdateUserStatus(userID, *body.IsActive, middleware.GetUserID(c), c.ClientIP(), c.Request.UserAgent()); err != nil {
		errors.JSONError(c, err)
		return
	}

	// M9: reset Break-Glass on successful sensitive operation
	h.resetBreakGlass(c)

	// C2/H29: security audit log for status change
	_ = h.service.LogSecurityEvent(
		middleware.GetUserID(c), "user_status_changed", "user", userID,
		c.ClientIP(), c.Request.UserAgent(), map[string]interface{}{"isActive": *body.IsActive},
	)

	errors.Success(c, gin.H{"message": "user status updated"})
}

// ResetUserPassword allows an admin to reset a user's password.
// The new password must be at least 8 characters; it is bcrypt-hashed before
// storage and TokenVersion is bumped to revoke all existing sessions.
func (h *Handler) ResetUserPassword(c *gin.Context) {
	// M9: Break-Glass check for sensitive operation (resetting user password)
	if !h.checkBreakGlass(c) {
		return
	}

	userID := c.Param("uid")
	var body struct {
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.NewPassword == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("new_password required"))
		return
	}

	if err := h.service.ResetUserPassword(userID, body.NewPassword, middleware.GetUserID(c), c.ClientIP(), c.Request.UserAgent()); err != nil {
		errors.JSONError(c, err)
		return
	}

	// M9: reset Break-Glass on successful sensitive operation
	h.resetBreakGlass(c)

	_ = h.service.LogSecurityEvent(
		middleware.GetUserID(c), "reset_user_password", "user", userID,
		c.ClientIP(), c.Request.UserAgent(), nil,
	)

	errors.Success(c, gin.H{"message": "password reset"})
}

// TransferChannelOwnership transfers a channel's ownership to a new user.
func (h *Handler) TransferChannelOwnership(c *gin.Context) {
	// M9: Break-Glass check for sensitive operation (transferring channel ownership)
	if !h.checkBreakGlass(c) {
		return
	}

	channelID := c.Param("channelId")
	var body struct {
		NewOwnerID string `json:"new_owner_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.NewOwnerID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("new_owner_id required"))
		return
	}

	if err := h.service.TransferChannelOwnership(channelID, body.NewOwnerID, middleware.GetUserID(c), c.ClientIP(), c.Request.UserAgent()); err != nil {
		errors.JSONError(c, err)
		return
	}

	// M9: reset Break-Glass on successful sensitive operation
	h.resetBreakGlass(c)

	h.service.LogAudit(
		middleware.GetUserID(c), "transfer_channel_ownership", "channel",
		channelID, c.ClientIP(), c.Request.UserAgent(), true,
	)
	_ = h.service.LogSecurityEvent(
		middleware.GetUserID(c), "transfer_channel_ownership", "channel", channelID,
		c.ClientIP(), c.Request.UserAgent(), map[string]interface{}{"newOwner": body.NewOwnerID},
	)

	errors.Success(c, gin.H{"message": "channel ownership transferred"})
}

// GetNetwork returns the current external network configuration.
func (h *Handler) GetNetwork(c *gin.Context) {
	if h.stateMgr == nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("server state manager not available"))
		return
	}
	errors.Success(c, h.stateMgr.BuildNetworkResponse())
}

// UpdateNetwork updates the external network configuration.
func (h *Handler) UpdateNetwork(c *gin.Context) {
	if h.stateMgr == nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("server state manager not available"))
		return
	}

	// M9: Break-Glass check for sensitive operation (modifying network config)
	if !h.checkBreakGlass(c) {
		return
	}

	var network serverstate.NetworkConfig
	if err := c.ShouldBindJSON(&network); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}

	// N29：保存前先取旧配置，供成功后计算「实际变更的键名」。
	prevNetwork := h.stateMgr.Network()

	if err := h.stateMgr.UpdateNetwork(network); err != nil {
		errors.JSONError(c, err)
		return
	}

	h.resetBreakGlass(c)

	h.service.LogAudit(
		middleware.GetUserID(c), "update_network_config", "server",
		"", c.ClientIP(), c.Request.UserAgent(), true,
	)
	_ = h.service.LogSecurityEvent(
		middleware.GetUserID(c), "update_network_config", "server", "",
		c.ClientIP(), c.Request.UserAgent(), map[string]interface{}{"network": network},
	)

	// N29：保存成功后广播全局事件 server_config_updated，让在线客户端立即
	// 重新拉取 server-info（横幅/锁图标即时生效，不再等重启）。payload 只给
	// 变更键名不给值——事件会发往所有已连接客户端，不能把完整网络配置泄露给
	// 非 Owner。旧客户端事件分发为白名单 switch，未知事件落 default 忽略，
	// 天然兼容。无变更（幂等保存）时不广播，避免无效刷新风暴。
	if h.hub != nil {
		if keys := changedNetworkKeys(prevNetwork, network); len(keys) > 0 {
			data, _ := json.Marshal(map[string]interface{}{
				"type": "server_config_updated",
				"payload": map[string]interface{}{
					"keys": keys,
				},
			})
			h.hub.Broadcast(data)
		}
	}

	errors.Success(c, h.stateMgr.BuildNetworkResponse())
}

// changedNetworkKeys 对比新旧网络配置，返回发生变化的字段的 JSON 键名（与
// serverstate.NetworkConfig 的 json tag 一致）。用于 server_config_updated
// 事件的 keys 载荷；无变化时返回 nil。
func changedNetworkKeys(prev, next serverstate.NetworkConfig) []string {
	var keys []string
	add := func(changed bool, name string) {
		if changed {
			keys = append(keys, name)
		}
	}
	add(prev.ExternalHost != next.ExternalHost, "externalHost")
	add(prev.ExternalHTTPPort != next.ExternalHTTPPort, "externalHttpPort")
	add(prev.ExternalLiveKitWSPort != next.ExternalLiveKitWSPort, "externalLiveKitWsPort")
	add(prev.ExternalMediaUDPPort != next.ExternalMediaUDPPort, "externalMediaUdpPort")
	add(prev.ExternalAdminPort != next.ExternalAdminPort, "externalAdminPort")
	add(prev.ExternalLiveKitTCPPort != next.ExternalLiveKitTCPPort, "externalLiveKitTcpPort")
	add(prev.UseHTTPS != next.UseHTTPS, "useHttps")
	add(prev.UPNPEnabled != next.UPNPEnabled, "upnpEnabled")
	add(prev.TURNTCPFallbackEnabled != next.TURNTCPFallbackEnabled, "turnTcpFallbackEnabled")
	add(prev.WebVoiceEnabled != next.WebVoiceEnabled, "webVoiceEnabled")
	add(prev.ClientAccessEnabled != next.ClientAccessEnabled, "clientAccessEnabled")
	return keys
}

// DetectNetwork runs a one-shot network environment detection and returns the
// result together with recommended connectivity strategies. It does not mutate
// the persisted configuration.
func (h *Handler) DetectNetwork(c *gin.Context) {
	result, err := h.detector.Detect()
	if err != nil {
		// Return a partial result together with the error so the UI can still
		// show whatever we managed to discover.
		if result == nil {
			result = &network.DetectionResult{}
		}
		c.JSON(http.StatusOK, gin.H{
			"success":         false,
			"error":           err.Error(),
			"result":          result,
			"recommendations": network.RecommendStrategies(result),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":         true,
		"result":          result,
		"recommendations": network.RecommendStrategies(result),
	})
}

// VerifyNetwork performs a lightweight verification of the current or provided
// network configuration. It resolves the external host and reports whether the
// configured ports are reachable from the server's perspective.
func (h *Handler) VerifyNetwork(c *gin.Context) {
	if h.stateMgr == nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("server state manager not available"))
		return
	}

	var body struct {
		Network *serverstate.NetworkConfig `json:"network"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}

	network := h.stateMgr.Network()
	if body.Network != nil {
		network = *body.Network
	}

	result := map[string]interface{}{
		"hostResolvable":        network.ExternalHost != "",
		"externalHost":          network.ExternalHost,
		"externalHttpPort":      network.ExternalHTTPPort,
		"externalLiveKitWsPort": network.ExternalLiveKitWSPort,
		"externalMediaUdpPort":  network.ExternalMediaUDPPort,
		"useHttps":              network.UseHTTPS,
		"serverUrl":             network.ServerURL(),
		"liveKitUrl":            network.LiveKitURL(),
		"notes": []string{
			"Port reachability from the public internet cannot be verified locally; use an external port checker.",
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"result":  result,
	})
}

// PortInfo describes a single exposed server port for the admin panel.
// admin-port-config-panel Task 5
type PortInfo struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Protocol     string `json:"protocol"`
	InternalPort int    `json:"internalPort"`
	ExternalPort int    `json:"externalPort"`
	External     bool   `json:"external"`
	Description  string `json:"description"`
	NatRequired  bool   `json:"natRequired"`
}

// GetPorts returns metadata for all externally-exposed server ports.
// GET /api/admin/ports
// admin-port-config-panel Task 5
func (h *Handler) GetPorts(c *gin.Context) {
	// 1. AdminConfig (Service.GetConfig already applies 0-value fallback for
	//    LiveKitTCPPort/LiveKitUDPPort on persisted rows).
	cfg, err := h.service.GetConfig()
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// 2. NetworkConfig from the serverstate Manager.
	if h.stateMgr == nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("server state manager not available"))
		return
	}
	network := h.stateMgr.Network()

	// 3. Build the 6-port list. VPN's ExternalPort mirrors InternalPort
	//    (WireGuard runs on the same port inside and outside).
	ports := []PortInfo{
		{Key: "api", Label: "API / Web 端口", Protocol: "tcp", InternalPort: cfg.APIPort, ExternalPort: network.ExternalHTTPPort, External: true, Description: "HTTP API + WebSocket + Voice SPA", NatRequired: false},
		{Key: "admin", Label: "Admin 后台", Protocol: "tcp", InternalPort: cfg.AdminPort, ExternalPort: network.ExternalAdminPort, External: true, Description: "管理 UI + Admin API + /metrics", NatRequired: false},
		{Key: "lkWs", Label: "LiveKit WS", Protocol: "tcp", InternalPort: cfg.LiveKitPort, ExternalPort: network.ExternalLiveKitWSPort, External: true, Description: "LiveKit 信令 WebSocket", NatRequired: false},
		{Key: "lkTcp", Label: "LiveKit TCP", Protocol: "tcp", InternalPort: cfg.LiveKitTCPPort, ExternalPort: network.ExternalLiveKitTCPPort, External: true, Description: "LiveKit 媒体 TCP fallback", NatRequired: true},
		{Key: "lkUdp", Label: "LiveKit UDP", Protocol: "udp", InternalPort: cfg.LiveKitUDPPort, ExternalPort: network.ExternalMediaUDPPort, External: true, Description: "LiveKit 媒体流（必须 UDP）", NatRequired: true},
		{Key: "vpn", Label: "VPN（可选）", Protocol: "udp", InternalPort: cfg.VPNPort, ExternalPort: cfg.VPNPort, External: false, Description: "WireGuard 虚拟组网", NatRequired: false},
	}

	errors.Success(c, gin.H{"ports": ports})
}

// ===== C-3: Admin Channel Management =====

// AdminListChannels returns all channels (admin sees everything).
func (h *Handler) AdminListChannels(c *gin.Context) {
	channels, err := h.channelSvc.GetChannels("", middleware.RoleAdmin)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, channels)
}

// AdminCreateChannel creates a new channel via admin API.
func (h *Handler) AdminCreateChannel(c *gin.Context) {
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
	body.Type = strings.ToLower(body.Type)
	if body.Type != "text" && body.Type != "voice" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid channel type"))
		return
	}

	ch, err := h.channelSvc.CreateChannel(body.Name, body.Type, body.IsPrivate, body.AudioQuality, body.Visibility, body.SortGroup)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	h.service.LogAudit(
		middleware.GetUserID(c), "admin_create_channel", "channel",
		ch.ID, c.ClientIP(), c.Request.UserAgent(), true,
	)

	errors.Success(c, ch)
}

// AdminUpdateChannel updates a channel via admin API.
func (h *Handler) AdminUpdateChannel(c *gin.Context) {
	channelID := c.Param("channelId")
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
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid visibility"))
			return
		}
	} else if body.IsPrivate != nil {
		if *body.IsPrivate {
			updates["visibility"] = "admin-only"
		} else {
			updates["visibility"] = "public"
		}
	}
	if body.Position != nil {
		updates["position"] = *body.Position
	}
	// A1-S1：分组名指针语义，取值域与校验和 channel/handler.go 的 UpdateChannel
	// 保持一致（共用 channel.NormalizeSortGroup：trim + rune 数 ≤32）。
	// 未传（nil）不修改；传空串 = 移出分组（sort_group 置空）。
	if body.SortGroup != nil {
		normalized, err := channel.NormalizeSortGroup(*body.SortGroup)
		if err != nil {
			errors.JSONError(c, err)
			return
		}
		updates["sort_group"] = normalized
	}
	// P1-1: admin 频道路径此前不接收 audioQuality，管理页的音频质量选择器
	// 会被静默丢弃。取值域与 channel/handler.go 的 UpdateChannel 保持一致
	// （fluent/standard/high/ultra），避免 admin 通道绕过上一批的音质修复。
	if body.AudioQuality != "" {
		switch body.AudioQuality {
		case "fluent", "standard", "high", "ultra":
			updates["voice_quality"] = body.AudioQuality
		default:
			errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid audioQuality, must be one of: fluent, standard, high, ultra"))
			return
		}
	}

	if err := h.channelSvc.UpdateChannel(channelID, updates); err != nil {
		errors.JSONError(c, err)
		return
	}

	h.service.LogAudit(
		middleware.GetUserID(c), "admin_update_channel", "channel",
		channelID, c.ClientIP(), c.Request.UserAgent(), true,
	)

	errors.Success(c, gin.H{"message": "channel updated"})
}

// AdminDeleteChannel deletes a channel via admin API.
func (h *Handler) AdminDeleteChannel(c *gin.Context) {
	channelID := c.Param("channelId")

	if err := h.channelSvc.DeleteChannel(channelID); err != nil {
		errors.JSONError(c, err)
		return
	}

	h.service.LogAudit(
		middleware.GetUserID(c), "admin_delete_channel", "channel",
		channelID, c.ClientIP(), c.Request.UserAgent(), true,
	)

	errors.Success(c, gin.H{"message": "channel deleted"})
}

// ChannelPermissions is the JSON-serializable permission model for a channel
// (H2). It is stored as a JSON blob in the channels.permissions column and is
// also the request/response body for the admin permission management API.
//
// Empty role lists mean "all roles" (no restriction). IsPublic=true is a
// convenience flag that overrides ReadRoles to allow every role to read.
type ChannelPermissions struct {
	ReadRoles    []string `json:"read_roles"`
	WriteRoles   []string `json:"write_roles"`
	VisibleRoles []string `json:"visible_roles"`
	IsPublic     bool     `json:"is_public"`
}

// defaultChannelPermissions returns the default permission set used when a
// channel has no persisted permission overrides (empty permissions column).
func defaultChannelPermissions() ChannelPermissions {
	return ChannelPermissions{
		ReadRoles:    []string{},
		WriteRoles:   []string{},
		VisibleRoles: []string{},
		IsPublic:     true,
	}
}

// GetChannelPermissions returns the per-channel permission overrides.
// GET /api/admin/channels/:channelId/permissions
func (h *Handler) GetChannelPermissions(c *gin.Context) {
	channelID := c.Param("channelId")

	var ch model.Channel
	if err := h.db.Select("id, permissions").First(&ch, "id = ?", channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	perms := defaultChannelPermissions()
	if strings.TrimSpace(ch.Permissions) != "" {
		if err := json.Unmarshal([]byte(ch.Permissions), &perms); err != nil {
			// Corrupt JSON — fall back to defaults so the UI can recover.
			perms = defaultChannelPermissions()
		}
	}
	errors.Success(c, perms)
}

// UpdateChannelPermissions replaces the per-channel permission overrides.
// PUT /api/admin/channels/:channelId/permissions
func (h *Handler) UpdateChannelPermissions(c *gin.Context) {
	channelID := c.Param("channelId")

	var body ChannelPermissions
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	// Normalize nil slices to empty slices so the JSON output is `[]`
	// rather than `null`, which keeps the frontend simple.
	if body.ReadRoles == nil {
		body.ReadRoles = []string{}
	}
	if body.WriteRoles == nil {
		body.WriteRoles = []string{}
	}
	if body.VisibleRoles == nil {
		body.VisibleRoles = []string{}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	result := h.db.Model(&model.Channel{}).
		Where("id = ?", channelID).
		Update("permissions", string(payload))
	if result.Error != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}
	if result.RowsAffected == 0 {
		errors.JSONError(c, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found"))
		return
	}

	h.service.LogAudit(
		middleware.GetUserID(c), "admin_update_channel_permissions", "channel",
		channelID, c.ClientIP(), c.Request.UserAgent(), true,
	)

	errors.Success(c, body)
}

// allowedRestartServices is the whitelist of systemd service names that may be
// restarted via the admin API. (admin-port-config-panel Task 6)
var allowedRestartServices = map[string]bool{
	"ridgericetalk": true,
	"livekit":       true,
	"netease-api":   true,
}

// defaultRestartFunc is the production implementation of restartFunc: it runs
// `systemctl restart <service>` synchronously with a context-bound timeout.
func defaultRestartFunc(ctx context.Context, service string) error {
	cmd := exec.CommandContext(ctx, "systemctl", "restart", service)
	return cmd.Run()
}

// RestartService triggers an asynchronous systemd service restart.
//
// This is a highly privileged operation:
//   - Only the Owner role may invoke it (RequireAdmin already permits
//     ADMIN/OWNER; this handler adds the Owner-only check).
//   - It is gated by the Break-Glass lockout protector (M9, design §9.7).
//   - The service name is validated against a strict whitelist.
//
// The systemctl restart itself runs in a background goroutine so the HTTP
// response is not blocked. Failures are logged but never surfaced to the
// client (to avoid leaking system internals).
//
// POST /api/admin/service/restart  body: {"service": "ridgericetalk"|"livekit"|"netease-api"}
// GetTTSStatus returns the current TTS engine status.
func (h *Handler) GetTTSStatus(c *gin.Context) {
	ready, model, errStr := bots.TTSStatus()
	errors.Success(c, gin.H{
		"ready": ready,
		"model": model,
		"error": errStr,
	})
}

// ReinitTTS reinitializes the TTS engine. Accepts optional "modelDir" in body.
func (h *Handler) ReinitTTS(c *gin.Context) {
	var req struct {
		ModelDir string `json:"modelDir"`
	}
	if err := c.ShouldBindJSON(&req); err == nil && req.ModelDir != "" {
		if err := bots.ResetTTS(req.ModelDir); err != nil {
			errors.JSONError(c, errors.ErrInternal.WithDetails("TTS reinit failed: "+err.Error()))
			return
		}
		errors.Success(c, gin.H{"success": true, "message": "TTS engine reinitialized"})
		return
	}
	// Without explicit modelDir, re-init with the default path.
	modelDir := filepath.Join(filepath.Dir(h.cfg.LocalDataPath), "models", "tts", "vits-melo-tts-zh_en")
	if err := bots.ResetTTS(modelDir); err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("TTS reinit failed: "+err.Error()))
		return
	}
	errors.Success(c, gin.H{"success": true, "message": "TTS engine reinitialized"})
}

func (h *Handler) RestartService(c *gin.Context) {
	// 1. Owner-only check (additional to RequireAdmin middleware).
	if middleware.GetRole(c) != middleware.RoleOwner {
		errors.JSONError(c, errors.ErrForbidden.WithDetails("only owner can restart services"))
		return
	}

	// 2. Break-Glass lockout check (same mechanism as other sensitive ops).
	if !h.checkBreakGlass(c) {
		return
	}

	// 3. Parse and validate the requested service name against the whitelist.
	var body struct {
		Service string `json:"service"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Service == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("service required"))
		return
	}
	if !allowedRestartServices[body.Service] {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid service name"))
		return
	}

	// 4. Reset Break-Glass after a successful (validated) sensitive operation.
	h.resetBreakGlass(c)

	// 5. Record audit log synchronously before returning.
	h.service.LogAudit(
		middleware.GetUserID(c), "restart_service", "service",
		body.Service, c.ClientIP(), c.Request.UserAgent(), true,
	)
	_ = h.service.LogSecurityEvent(
		middleware.GetUserID(c), "restart_service", "service", body.Service,
		c.ClientIP(), c.Request.UserAgent(), map[string]interface{}{"service": body.Service},
	)

	// 6. Asynchronously execute systemctl restart so the HTTP response is not
	//    blocked. Failures are logged but not surfaced to the client.
	service := body.Service
	restart := h.restartFunc
	if restart == nil {
		restart = defaultRestartFunc
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := restart(ctx, service); err != nil {
			logrus.WithError(err).WithField("service", service).Error("failed to restart service")
		}
	}()

	// 7. Return immediately.
	errors.Success(c, gin.H{"success": true, "message": "restart scheduled"})
}
