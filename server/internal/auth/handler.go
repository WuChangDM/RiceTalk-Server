package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/httpbind"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/serverstate"
	"ridgericetalk/middleware"
)

// Handler handles HTTP requests for authentication
type Handler struct {
	service    *Service
	db         *gorm.DB
	cfg        *config.Config
	bruteForce *middleware.BruteForceProtector
	stateMgr   *serverstate.Manager
}

// NewHandler creates a new auth handler without a serverstate.Manager. This
// constructor is kept for tests and legacy callers; production code should use
// NewHandlerWithState so that bootstrap can persist network configuration.
func NewHandler(db *gorm.DB, cfg *config.Config) *Handler {
	return NewHandlerWithState(db, cfg, nil)
}

// NewHandlerWithState creates a new auth handler with the server state manager.
func NewHandlerWithState(db *gorm.DB, cfg *config.Config, stateMgr *serverstate.Manager) *Handler {
	return &Handler{
		service:    NewService(db, cfg),
		db:         db,
		cfg:        cfg,
		bruteForce: middleware.NewBruteForceProtector(),
		stateMgr:   stateMgr,
	}
}

// audit records a security event. Errors are ignored to avoid breaking the
// request flow if audit logging fails.
func (h *Handler) audit(c *gin.Context, userID, action, resourceType, resourceID string, details map[string]interface{}) {
	if details == nil {
		details = make(map[string]interface{})
	}
	_ = h.service.LogSecurityEvent(userID, action, resourceType, resourceID, c.ClientIP(), c.Request.UserAgent(), details)
}

// clearAuthCookies invalidates the browser auth cookies set on login.
func clearAuthCookies(c *gin.Context, secure bool) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("rrt_token", "", -1, "/", "", secure, true)
	c.SetCookie("rrt_refresh_token", "", -1, "/", "", secure, true)
}

// RegisterRoutes registers auth routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	auth := r.Group("/auth")
	auth.Use(middleware.RateLimit(middleware.AuthRateLimit()))
	{
		auth.POST("/register", h.Register)
		auth.POST("/login", h.Login)
		auth.POST("/logout", middleware.AuthRequired(h.cfg, h.db), h.Logout)
		auth.POST("/refresh", h.RefreshToken)
		auth.GET("/me", middleware.AuthRequired(h.cfg, h.db), h.GetMe)
		auth.POST("/password", middleware.AuthRequired(h.cfg, h.db), h.ChangePassword)
		auth.POST("/forgot-password", h.ForgotPassword)
		auth.POST("/forgot-password/verify", h.VerifySecurityQuestions)
		auth.POST("/reset-password", h.ResetPassword)

		// S-2 多设备标识：当前用户自己的会话管理（列表 / 踢除其他设备）
		auth.GET("/sessions", middleware.AuthRequired(h.cfg, h.db), h.ListSessions)
		auth.DELETE("/sessions/:id", middleware.AuthRequired(h.cfg, h.db), h.RevokeSession)
		// A4-S1（DES-20261001-01 §5.2）：一键下线当前用户的其他全部设备。
		auth.POST("/sessions/revoke-others", middleware.AuthRequired(h.cfg, h.db), h.RevokeOthersSessions)
	}

	// Admin bootstrap routes
	admin := r.Group("/admin")
	admin.Use(middleware.CSRFProtection(h.cfg))
	{
		admin.GET("/bootstrap/status", h.GetBootstrapStatus)
		admin.POST("/bootstrap/verify", h.VerifyBootstrapToken)
		admin.POST("/bootstrap/register", h.CreateOwner)
		admin.POST("/login", h.AdminLogin)
		admin.POST("/logout", middleware.AuthRequired(h.cfg, h.db), h.AdminLogout)
	}
}

// Service exposes the underlying auth service so the server assembly layer
// can wire cross-cutting callbacks (e.g. A4-S2 OnNewLoginAlert → hub broadcast)
// without changing the Handler constructor signature.
func (h *Handler) Service() *Service {
	return h.service
}

// captureClientContext 把请求的来源网络上下文写入目标变量（A4-S1）。对应
// 字段均为 json:"-"：只能由服务端从连接上下文采集，客户端 JSON 无法注入。
func captureClientContext(ip *string, userAgent *string, c *gin.Context) {
	*ip = sanitizeClientIP(c.ClientIP())
	*userAgent = sanitizeUserAgent(c.GetHeader("User-Agent"))
}

// Register handles user registration
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}
	// A4-S1：注册自动登录会话也记录来源网络（服务端采集）。
	captureClientContext(&req.IP, &req.UserAgent, c)

	resp, err := h.service.Register(&req)
	if err != nil {
		h.audit(c, "", "register_failure", "user", req.Username, map[string]interface{}{"email": req.Email, "reason": err.Error()})
		errors.JSONError(c, err)
		return
	}

	h.audit(c, resp.User.ID, "register_success", "user", resp.User.ID, map[string]interface{}{"username": req.Username, "email": req.Email})

	// Set auth cookie for Web clients (design doc §2.1, §3.4: register should auto-login)
	isProd := h.cfg.CookieSecureEnabled()
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("rrt_token", resp.AccessToken, 86400, "/", "", isProd, true)

	errors.Success(c, resp)
}

// Login handles user login
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}

	// Login identity is the email (L4). Brute-force is keyed on the email so
	// email-based logins are rate-limited on the actual account.
	loginID := strings.TrimSpace(req.Email)
	if loginID == "" {
		loginID = req.Username // backward compatibility: old clients sent email in username field
	}

	ip := c.ClientIP()

	// Brute-force protection: account-level check
	if allowed, remaining := h.bruteForce.AllowAccount(loginID); !allowed {
		errors.JSONError(c, errors.New(errors.AUTH_ACCOUNT_LOCKED,
			"account locked; try again in "+remaining.String()))
		return
	}
	if allowed, remaining := h.bruteForce.AllowIP(ip); !allowed {
		errors.JSONError(c, errors.New(errors.AUTH_ACCOUNT_LOCKED,
			"too many attempts; try again in "+remaining.String()))
		return
	}

	// A4-S1：登录会话记录来源网络上下文（服务端采集，客户端不可自报）。
	captureClientContext(&req.IP, &req.UserAgent, c)

	resp, err := h.service.Login(&req)
	if err != nil {
		// Record failed attempt for brute-force protection
		h.bruteForce.RecordFailure(loginID, ip)
		h.audit(c, "", "login_failure", "user", loginID, map[string]interface{}{"email": loginID, "error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	// Reset failure count on successful login
	h.bruteForce.ResetAccount(loginID)

	userID := ""
	if resp.User != nil {
		userID = resp.User.ID
	}
	h.audit(c, userID, "login_success", "user", userID, map[string]interface{}{"email": loginID})

	// Set auth cookie for Web clients
	isProd := h.cfg.CookieSecureEnabled()
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("rrt_token", resp.AccessToken, 86400, "/", "", isProd, true)

	errors.Success(c, resp)
}

// Logout handles user logout
func (h *Handler) Logout(c *gin.Context) {
	userID := middleware.GetUserID(c)
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	// SEC-001: 允许空请求体（refreshToken 走 cookie 兜底），但拒绝非法 JSON
	if err := httpbind.BindJSONAllowEmpty(c, &body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid JSON body: "+err.Error()))
		return
	}

	refreshToken := body.RefreshToken
	if refreshToken == "" {
		refreshToken, _ = c.Cookie("rrt_refresh_token")
	}

	if refreshToken == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("refresh token required"))
		return
	}

	if err := h.service.Logout(userID, refreshToken); err != nil {
		h.audit(c, userID, "logout_failure", "user", userID, map[string]interface{}{"error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	h.audit(c, userID, "logout_success", "user", userID, nil)
	clearAuthCookies(c, h.cfg.CookieSecureEnabled())
	errors.Success(c, gin.H{"message": "logged out"})
}

// RefreshToken handles token refresh
func (h *Handler) RefreshToken(c *gin.Context) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
		// S-2 多设备标识（可选）：客户端 refresh 时同步上报，覆盖轮换前会话行
		// 携带的设备字段；老客户端不传则继承旧值。
		DeviceType string `json:"deviceType,omitempty"`
		DeviceName string `json:"deviceName,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("refresh token required"))
		return
	}

	// A4-S1：网络上下文（IP/UA）每次 refresh 都从当前请求采集并覆盖会话行
	//（「最近活跃 IP」语义），与设备字段的「可选上报、缺省继承」不同——因此
	// device 始终非 nil（Type/Name 可能为空，service 侧对设备字段保持继承语义）。
	device := &SessionDevice{
		IP:        sanitizeClientIP(c.ClientIP()),
		UserAgent: sanitizeUserAgent(c.GetHeader("User-Agent")),
	}
	if body.DeviceType != "" || body.DeviceName != "" {
		d := sanitizeSessionDevice(body.DeviceType, body.DeviceName)
		device.Type = d.Type
		device.Name = d.Name
	}

	resp, err := h.service.RefreshToken(body.RefreshToken, device)
	if err != nil {
		// C2: audit log for token refresh failure
		h.audit(c, "", "token_refresh_failure", "auth", "", map[string]interface{}{"error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	// C2: audit log for token refresh success
	userID := ""
	if resp.User != nil {
		userID = resp.User.ID
	}
	h.audit(c, userID, "token_refresh_success", "auth", userID, nil)

	// L2: refresh the auth cookie so browser clients pick up the new access token
	// (design doc §2.3). Without this, the browser keeps sending the stale token
	// from the previous login and subsequent requests fail with 401.
	if resp.AccessToken != "" {
		secure := h.cfg.CookieSecureEnabled()
		c.SetSameSite(http.SameSiteStrictMode)
		c.SetCookie("rrt_token", resp.AccessToken, 86400, "/", "", secure, true)
	}

	errors.Success(c, resp)
}

// GetMe returns the current authenticated user
func (h *Handler) GetMe(c *gin.Context) {
	userID := middleware.GetUserID(c)
	user, err := h.service.GetUserByID(userID)
	if err != nil {
		if err == errors.ErrNotFound {
			errors.JSONError(c, errors.ErrUnauthorized)
			return
		}
		errors.JSONError(c, err)
		return
	}

	// Don't expose password hash
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

// GetBootstrapStatus returns server initialization status.
// Does NOT return or create bootstrap tokens — tokens must be provisioned out-of-band.
func (h *Handler) GetBootstrapStatus(c *gin.Context) {
	initialized, err := h.service.CheckServerInitialized()
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	if initialized {
		errors.Success(c, gin.H{
			"initialized":    true,
			"needsBootstrap": false,
		})
		return
	}

	// Check if there's a valid bootstrap token available
	hasToken, err := h.service.GetBootstrapStatus()
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"initialized":    false,
		"needsBootstrap": hasToken,
	})
}

// VerifyBootstrapToken checks a bootstrap token without consuming it.
func (h *Handler) VerifyBootstrapToken(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("token required"))
		return
	}

	if err := h.service.VerifyBootstrapToken(body.Token); err != nil {
		// C2: audit log for bootstrap token verification (failure)
		h.audit(c, "", "bootstrap_token_verified", "auth", "", map[string]interface{}{"success": false, "error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	// C2: audit log for bootstrap token verification (success)
	h.audit(c, "", "bootstrap_token_verified", "auth", "", map[string]interface{}{"success": true})

	errors.Success(c, gin.H{"valid": true})
}

// CreateOwner creates the initial owner account and, when a state manager is
// wired, completes the bootstrap by persisting the supplied network config.
func (h *Handler) CreateOwner(c *gin.Context) {
	var body struct {
		RegisterRequest
		BootstrapToken string                   `json:"bootstrapToken"`
		SpaceName      string                   `json:"spaceName"`
		Network        serverstate.NetworkConfig `json:"network"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}

	resp, err := h.service.CreateOwner(&body.RegisterRequest, body.BootstrapToken, body.SpaceName)
	if err != nil {
		// C2: audit log for owner initialization (failure)
		h.audit(c, "", "owner_initialized", "user", "", map[string]interface{}{"username": body.Username, "success": false, "error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	// Persist network configuration as part of bootstrap when state manager is available.
	if h.stateMgr != nil {
		network := body.Network
		if network.ExternalHost == "" {
			network.ExternalHost = h.stateMgr.ResolveExternalHost()
		}
		// N25：端口默认值取**本进程实际配置**而非硬编 443 —— 443 是「反代生产」口径，
		// embedded/直连部署（如 8080/17880）用它会把对外广播地址写错。
		if network.ExternalHTTPPort == 0 {
			network.ExternalHTTPPort = h.cfg.Port
		}
		if network.ExternalLiveKitWSPort == 0 {
			network.ExternalLiveKitWSPort = h.cfg.LiveKitPort
		}
		if network.ExternalMediaUDPPort == 0 {
			network.ExternalMediaUDPPort = 7882
		}
		// Defaults for ports introduced by the admin-port-config-panel spec.
		// Keep in sync with serverstate.DefaultNetworkConfig().
		if network.ExternalAdminPort == 0 {
			network.ExternalAdminPort = h.cfg.AdminPort
		}
		if network.ExternalLiveKitTCPPort == 0 {
			network.ExternalLiveKitTCPPort = 7881
		}
		// N25：bootstrap 时无法区分「未传」与「显式 false」（bool 零值），而
		// 全新初始化场景不可能有意禁用桌面客户端 —— 一律启用，否则初始化完成
		// 后客户端立即被「该服务器当前禁止客户端接入」拒绝（serverstate 的默认
		// true 只作用于新建状态，会被这里的持久化覆盖）。
		network.ClientAccessEnabled = true
		// N29：Web 语音同 N25 口径改为缺省启用 —— 本产品定位是自托管语音平台，
		// 语音是全新部署的第一核心需求，bootstrap 后即被锁定属于 onboarding
		// 阻断（竞品实测 N29）。管理员初始化完成后仍可在管理后台显式关闭；
		// 已部署机器的 server-state 不回写，此缺省只影响新建 bootstrap。
		network.WebVoiceEnabled = true
		if err := h.stateMgr.CompleteBootstrap(network); err != nil {
			// C2: audit log for bootstrap state persistence failure
			ownerID := ""
			if resp.User != nil {
				ownerID = resp.User.ID
			}
			h.audit(c, ownerID, "bootstrap_state_persist_failed", "server", "", map[string]interface{}{"error": err.Error()})
			errors.JSONError(c, err)
			return
		}
	}

	// C2: audit log for owner initialization (success)
	ownerID := ""
	if resp.User != nil {
		ownerID = resp.User.ID
	}
	h.audit(c, ownerID, "owner_initialized", "user", ownerID, map[string]interface{}{"username": body.Username, "success": true})

	errors.Success(c, resp)
}

// AdminLogin handles admin login
func (h *Handler) AdminLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}
	loginID := strings.TrimSpace(req.Email)
	if loginID == "" {
		loginID = req.Username // backward compatibility: old clients sent email in username field
	}
	if loginID == "" || req.Password == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("email and password required"))
		return
	}

	loginReq := &LoginRequest{
		Username: loginID,
		Email:    loginID,
		Password: req.Password,
	}

	ip := c.ClientIP()

	if allowed, remaining := h.bruteForce.AllowAccount(loginID); !allowed {
		h.audit(c, "", "admin_login_failure", "user", loginID, map[string]interface{}{"email": loginID, "reason": "account locked"})
		errors.JSONError(c, errors.New(errors.AUTH_ACCOUNT_LOCKED,
			"account locked; try again in "+remaining.String()))
		return
	}

	resp, err := h.service.AdminLogin(loginReq)
	if err != nil {
		h.bruteForce.RecordFailure(loginID, ip)
		h.audit(c, "", "admin_login_failure", "user", loginID, map[string]interface{}{"email": loginID, "error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	userRole := resp.User.Role
	userID := resp.User.ID

	if userRole != middleware.RoleAdmin && userRole != middleware.RoleOwner {
		h.audit(c, userID, "admin_login_failure", "user", userID, map[string]interface{}{"email": loginID, "reason": "not admin"})
		errors.JSONError(c, errors.New(errors.ADMIN_UNAUTHORIZED, "admin access required"))
		return
	}

	h.bruteForce.ResetAccount(loginID)
	h.audit(c, userID, "admin_login_success", "user", userID, map[string]interface{}{"email": loginID})

	// Set auth cookie for Web clients (same as regular login)
	isProd := h.cfg.CookieSecureEnabled()
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("rrt_token", resp.AccessToken, 86400, "/", "", isProd, true)

	errors.Success(c, resp)
}

// AdminLogout handles admin logout
func (h *Handler) AdminLogout(c *gin.Context) {
	h.Logout(c)
}

// ChangePassword handles password change for authenticated users
func (h *Handler) ChangePassword(c *gin.Context) {
	userID := middleware.GetUserID(c)
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.CurrentPassword == "" || body.NewPassword == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("currentPassword and newPassword required"))
		return
	}

	if err := h.service.ChangePassword(userID, body.CurrentPassword, body.NewPassword); err != nil {
		h.audit(c, userID, "password_change_failure", "user", userID, map[string]interface{}{"error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	h.audit(c, userID, "password_change_success", "user", userID, nil)
	errors.Success(c, gin.H{"message": "password changed successfully"})
}

// ForgotPassword returns the security questions for the given email.
func (h *Handler) ForgotPassword(c *gin.Context) {
	var req ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}

	resp, err := h.service.ForgotPassword(&req)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	h.audit(c, "", "forgot_password_questions", "user", "", map[string]interface{}{"email": req.Email})
	errors.Success(c, resp)
}

// VerifySecurityQuestions verifies security question answers and issues a reset token.
func (h *Handler) VerifySecurityQuestions(c *gin.Context) {
	var req VerifySecurityQuestionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}

	resp, err := h.service.VerifySecurityQuestions(&req)
	if err != nil {
		h.audit(c, "", "forgot_password_verify_failure", "user", "", map[string]interface{}{"email": req.Email, "error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	h.audit(c, "", "forgot_password_verify_success", "user", "", map[string]interface{}{"email": req.Email})
	errors.Success(c, resp)
}

// ResetPassword resets the password using a one-time reset token.
func (h *Handler) ResetPassword(c *gin.Context) {
	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid request body"))
		return
	}

	if err := h.service.ResetPassword(&req); err != nil {
		h.audit(c, "", "reset_password_failure", "user", "", map[string]interface{}{"error": err.Error()})
		errors.JSONError(c, err)
		return
	}

	h.audit(c, "", "reset_password_success", "user", "", nil)
	errors.Success(c, gin.H{"message": "password reset successfully"})
}

// ListSessions returns the caller's own active login sessions with device
// identity (S-2 多设备标识). The current session is flagged via the access
// token's session_id claim.
func (h *Handler) ListSessions(c *gin.Context) {
	userID := middleware.GetUserID(c)
	currentSessionID := middleware.GetSessionID(c)

	sessions, err := h.service.ListSessions(userID, currentSessionID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"sessions": sessions})
}

// RevokeSession kicks one of the caller's other devices ("下线此设备").
// Revoking the current session is rejected with 400 (use /auth/logout);
// unknown or foreign session IDs return 404 without ownership leaks.
func (h *Handler) RevokeSession(c *gin.Context) {
	userID := middleware.GetUserID(c)
	currentSessionID := middleware.GetSessionID(c)
	sessionID := c.Param("id")

	if err := h.service.RevokeSession(userID, currentSessionID, sessionID); err != nil {
		errors.JSONError(c, err)
		return
	}

	h.audit(c, userID, "session_revoked", "auth", sessionID, nil)
	errors.Success(c, gin.H{"message": "session revoked"})
}

// RevokeOthersSessions kicks every session of the caller except the current
// one（A4-S1「下线其他全部设备」，DES-20261001-01 §5.2）。当前会话来自
// access token 的 session_id 声明；老 token 无该声明时拒绝（400
// AUTH_NO_SESSION_CONTEXT）——否则无法识别「自己」，会把自己也踢下线。
func (h *Handler) RevokeOthersSessions(c *gin.Context) {
	userID := middleware.GetUserID(c)
	currentSessionID := middleware.GetSessionID(c)
	if currentSessionID == "" {
		errors.JSONError(c, errors.New(errors.AUTH_NO_SESSION_CONTEXT,
			"current session context missing; re-login required"))
		return
	}

	revoked, err := h.service.RevokeOthersSessions(userID, currentSessionID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	// 审计只记数量，不记任何 token（DES-20261001-01 §5.2）。
	h.audit(c, userID, "auth.sessions.revoke_others", "auth", currentSessionID,
		map[string]interface{}{"revoked": revoked})
	errors.Success(c, gin.H{"revoked": revoked})
}
