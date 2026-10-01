package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/version"
	"ridgericetalk/features/virtualnet"
	"ridgericetalk/features/whiteboard"
	"ridgericetalk/internal/admin"
	"ridgericetalk/internal/channel"
	"ridgericetalk/internal/files"
	"ridgericetalk/internal/message"
	"ridgericetalk/internal/metrics"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/og"
	"ridgericetalk/internal/remoteassist"
	"ridgericetalk/internal/screenshare"
	"ridgericetalk/internal/serverstate"
	"ridgericetalk/internal/sfx"
	"ridgericetalk/internal/user"
	"ridgericetalk/middleware"
)

// registerRoutes mounts all HTTP/WebSocket routes on the main and admin engines.
func (a *App) registerRoutes() {
	healthHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "ok",
			"version":   version.Server,
			"commit":    version.Commit,
			"buildTime": version.BuildTime,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	}

	// Public CSRF token endpoint — explicitly sets the csrf_token cookie and
	// returns the token value. This lets clients that need a CSRF token obtain
	// one without relying on a side-effect of /health or another safe endpoint.
	csrfHandler := func(c *gin.Context) {
		token := middleware.SetCSRFCookie(c, a.cfg)
		c.JSON(http.StatusOK, gin.H{"code": "OK", "data": gin.H{"csrfToken": token}})
	}

	// Register API routes under /api/v1
	v1 := a.mainEngine.Group("/api/v1")
	v1.Use(middleware.CSRFProtection(a.cfg))
	v1.GET("/health", healthHandler)
	v1.HEAD("/health", healthHandler)
	v1.GET("/auth/csrf", csrfHandler)
	a.registerAPIRoutes(v1)

	// Legacy API routes under /api (frontend compatibility)
	legacy := a.mainEngine.Group("/api")
	legacy.Use(middleware.CSRFProtection(a.cfg))
	legacy.GET("/health", healthHandler)
	legacy.HEAD("/health", healthHandler)
	legacy.GET("/auth/csrf", csrfHandler)
	legacy.GET("/csrf", csrfHandler)
	a.registerAPIRoutes(legacy)

	// NEW-002: Admin CRUD routes must also be reachable on the main API port so
	// that /api/admin/channels and /api/admin/users work when the caller hits
	// port 8080 (e.g. curl, proxies, or AdminPortShared=false deployments).
	adminV1Main := a.mainEngine.Group("/api/v1")
	adminV1Main.Use(middleware.CSRFProtection(a.cfg))
	a.registerAdminRoutes(adminV1Main)
	adminLegacyMain := a.mainEngine.Group("/api")
	adminLegacyMain.Use(middleware.CSRFProtection(a.cfg))
	a.registerAdminRoutes(adminLegacyMain)

	// LiveKit webhook endpoint (public, no CSRF — called by LiveKit server)
	a.mainEngine.POST("/api/v1/livekit/webhook", a.voiceHandler.HandleWebhook)
	a.mainEngine.POST("/api/livekit/webhook", a.voiceHandler.HandleWebhook)

	// Avatar static files (must be before NoRoute to avoid SPA fallback)
	a.registerAvatarRoutes()

	// DES-2026-0912-05：云文件分享链接——本产品唯一免鉴权的业务接口。
	// 独立的顶层 group，刻意不并入 /api/v1 与 /api 的既有 group：
	//   - 不挂 AuthRequired（否则公开链接无法访问）；
	//   - 不挂 CSRFProtection（该中间件会拒绝未持有 CSRF cookie 的 POST，
	//     把「浏览器直接打开分享链接」这条路堵死；这里没有会话可被 CSRF 利用，
	//     token 本身就是凭据，防护由独立限流 + 爆破防护 + 原子扣减承担）；
	//   - 限流由 RegisterShareRoutes 内部用独立 limiter 实例提供。
	// 只注册三条路由，任何新增都必须先改设计文档。
	shareGroup := a.mainEngine.Group("/api/v1/share")
	a.cloudfsHandler.RegisterShareRoutes(shareGroup)

	// WebSocket endpoint
	a.registerWebSocketRoute()

	// Voice SPA fallback
	a.registerSPAFallback()

	// Admin API routes (dedicated admin port must expose bootstrap/login/auth
	// endpoints so the admin SPA served on this port can reach them).
	a.registerAdminAPIRoutes(healthHandler)

	// Backward compatibility: also expose admin routes on the main port when configured.
	if a.cfg.AdminPortShared {
		a.registerSharedAdminRoutes(healthHandler)
	}
}

func (a *App) registerAPIRoutes(api *gin.RouterGroup) {
	api.Use(middleware.RateLimit(a.globalLimiter))

	// Auth routes
	a.authHandler.RegisterRoutes(api)

	// A4-S2（DES-20261001-01 §5.2）：异地登录提醒广播装配。登录命中
	// shouldAlertNewLogin 判定后，auth.Service 经此回调把 security_alert
	// 投递给 realtime.Hub（auth 不反向依赖 realtime，避免循环依赖；与
	// OnRemoteAssistControl 的装配方式一致）。payload 由 service 侧构造并
	// 已脱敏（ip 打码），这里只负责序列化与定向广播——跳过触发登录的那条
	// 新会话连接，用户的其他在线设备收到提醒。
	a.authHandler.Service().OnNewLoginAlert = func(userID, exceptSessionID string, payload map[string]interface{}) {
		data, err := json.Marshal(map[string]interface{}{
			"type":    "security_alert",
			"payload": payload,
		})
		if err != nil {
			return
		}
		a.hub.BroadcastToUserExcept(userID, exceptSessionID, data)
	}

	// User routes
	userHandler := user.NewHandler(a.db.DB, a.cfg, a.hub)
	userHandler.RegisterRoutes(api)

	// Channel routes
	channelHandler := channel.NewHandler(a.db.DB, a.cfg, a.hub)
	channelHandler.RegisterRoutes(api)

	// Message routes
	messageHandler := message.NewHandler(a.db.DB, a.cfg, a.hub, a.messageService)
	messageHandler.RegisterRoutes(api)

	// CloudFS routes (must be before message archiver injection)
	a.cloudfsHandler.RegisterRoutes(api)

	// Voice routes
	a.voiceHandler.RegisterRoutes(api)

	// Bot routes
	a.botsHandler.RegisterRoutes(api)

	// OG preview routes
	ogHandler := og.NewHandler(a.db.DB, a.cfg)
	ogHandler.RegisterRoutes(api)

	// File routes
	filesHandler := files.NewHandler(a.db.DB, a.appStorage, a.cfg)
	filesHandler.RegisterRoutes(api)

	// SFX (entrance/exit sounds) routes — shares the service built in app.go
	sfxHandler := sfx.NewHandler(a.db.DB, a.cfg, a.sfxService)
	sfxHandler.RegisterRoutes(api)

	// Screen share routes
	screenshareHandler := screenshare.NewHandler(a.db.DB, a.cfg, a.hub)
	screenshareHandler.RegisterRoutes(api)

	// Remote assist routes (T49)
	remoteAssistHandler := remoteassist.NewHandler(a.db.DB, a.cfg, a.hub, a.adminSvc)
	remoteAssistHandler.RegisterRoutes(api)
	// H14: WebSocket 控制事件（remote_assist_control）转发前的授权校验。
	// hub 无法自行查库校验会话归属，因此把校验函数挂到 hub 上（与
	// lifecycle.go 里 OnDocEditorJoin 的装配方式一致），避免 realtime 包
	// 反向依赖 remoteassist 造成循环依赖。
	a.hub.OnRemoteAssistControl = remoteAssistHandler.ValidateControlEvent
	// A6-S1（DES-20261001-01 §7.2）：被控端剪贴板回传（remote_assist_clipboard_data）
	// 转发前的校验——sender 必须是会话 target，返回 requester 供路由。控制端
	// 方向（remote_assist_clipboard 的 push/pull）复用上方 OnRemoteAssistControl
	// 四项校验，不另设钩子。fail-closed：未装配时 hub 侧一律丢弃。
	a.hub.OnRemoteAssistFromTarget = remoteAssistHandler.ValidateFromTarget
	// A7-S1（DES-20261001-01 §8.1）：会话内聊天（remote_assist_chat）转发前
	// 的校验——sender 必须是会话 requester 或 target，返回对端供路由。
	a.hub.OnRemoteAssistChatParticipant = remoteAssistHandler.ValidateChatParticipant

	// Whiteboard routes
	whiteboardHandler := whiteboard.NewHandler(a.db.DB, a.cfg, a.hub)
	whiteboardHandler.RegisterRoutes(api)

	// SharedDoc routes
	a.sharedocHandler.RegisterRoutes(api)

	// Schedule routes
	a.scheduleHandler.RegisterRoutes(api)

	// Minigames routes
	a.minigamesHandler.RegisterRoutes(api)

	// VirtualNet routes
	virtualnetHandler := virtualnet.NewHandler(a.db.DB, a.cfg, a.hub)
	virtualnetHandler.RegisterRoutes(api)

	// Server version endpoint (client compatibility check)
	api.GET("/server/version", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"version":            version.Server,
			"build_time":         "dev",
			"git_commit":         "dev",
			"go_version":         "go1.22+",
			"min_client_version": version.MinClient,
			"features": gin.H{
				"voice":        true,
				"screen_share": true,
				"whiteboard":   true,
				"bots":         true,
				"sfx":          true,
				"vpn":          false,
			},
		})
	})

	// Server compatibility matrix
	api.GET("/server/compatibility", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"server_version":     version.Server,
			"min_client_version": version.MinClient,
			"max_client_version": version.MaxClient,
			"supported_features": []string{"voice", "screen_share", "whiteboard", "bots", "sfx"},
		})
	})

	// Client update check (public, no auth)
	api.GET("/client/update", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"current":   version.Server,
			"latest":    version.Server,
			"required":  false,
			"changelog": "Initial release",
		})
	})

	// Notifications (stub)
	api.GET("/notifications", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"notifications": []gin.H{}})
	})
	api.POST("/notifications/read-all", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})
	api.POST("/notifications/:id/read", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	// Mention search (stub)
	api.GET("/mentions/search", func(c *gin.Context) {
		q := c.Query("q")
		if q == "" {
			c.JSON(http.StatusOK, []gin.H{})
			return
		}
		// Search users by username/display_name
		var users []struct {
			ID          string `json:"user_id"`
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Avatar      string `json:"avatar_url"`
			Role        string `json:"role"`
		}
		a.db.DB.Table("users").
			Select("id, username, display_name, avatar, role").
			Where("username LIKE ? OR display_name LIKE ?", "%"+q+"%", "%"+q+"%").
			Limit(10).Find(&users)
		c.JSON(http.StatusOK, users)
	})

	api.GET("/server/info", func(c *gin.Context) {
		c.JSON(http.StatusOK, buildServerInfo(a.stateMgr, a.cfg, c.Request.Host))
	})

	api.GET("/server/network", func(c *gin.Context) {
		if a.stateMgr == nil || !a.stateMgr.IsInitialized() {
			c.JSON(http.StatusOK, serverstate.NetworkResponse{
				Network: serverstate.NetworkConfig{},
			})
			return
		}
		c.JSON(http.StatusOK, a.stateMgr.BuildNetworkResponse())
	})
}

func (a *App) registerAdminRoutes(adminAPI *gin.RouterGroup) {
	// CSRF protection is applied once by the caller's API group. Full API groups
	// also own rate limiting before reaching this registration path.
	adminAPI.Use(middleware.AuthRequiredWithState(a.cfg, a.db.DB, a.stateMgr))
	adminAPI.Use(middleware.RequireAdmin())

	// M9: Owner Break-Glass protector for sensitive admin operations
	adminHandler := admin.NewHandlerWithService(a.db.DB, a.cfg, a.hub, a.breakGlassProtector, a.stateMgr, a.adminSvc)
	adminHandler.RegisterRoutes(adminAPI)
}

func (a *App) registerAdminAPIRoutes(healthHandler gin.HandlerFunc) {
	adminV1 := a.adminEngine.Group("/api/v1")
	adminV1.Use(middleware.CSRFProtection(a.cfg))
	a.registerAPIRoutes(adminV1)
	a.registerAdminRoutes(adminV1)

	adminLegacy := a.adminEngine.Group("/api")
	adminLegacy.Use(middleware.CSRFProtection(a.cfg))
	a.registerAPIRoutes(adminLegacy)
	a.registerAdminRoutes(adminLegacy)

	// Prometheus metrics endpoint - protected by admin auth
	a.adminEngine.GET("/metrics", middleware.AuthRequired(a.cfg, a.db.DB), middleware.RequireAdmin(), gin.WrapH(metrics.Handler()))

	// C2: Admin WebSocket endpoint on the dedicated admin engine.
	// Admin static files
	adminFS := http.FS(os.DirFS(webhostDir("admin")))
	adminStaticHandler := http.StripPrefix("/admin", http.FileServer(adminFS))
	a.adminEngine.GET("/admin", gin.WrapH(adminStaticHandler))
	// 合并 /admin/ws 和 /admin/*filepath 为单个 catch-all，避免 gin 路由冲突
	a.adminEngine.GET("/admin/*filepath", func(c *gin.Context) {
		if c.Param("filepath") == "/ws" {
			middleware.CSRFProtection(a.cfg)(c)
			if !c.IsAborted() {
				a.adminWSHandler.HandleWS(c)
			}
			return
		}
		gin.WrapH(adminStaticHandler)(c)
	})

	// Admin SPA fallback
	a.adminEngine.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || c.Request.URL.Path == "/metrics" {
			errors.JSONError(c, errors.ErrNotFound)
			return
		}
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.FileFromFS("/", adminFS)
	})
}

func (a *App) registerSharedAdminRoutes(healthHandler gin.HandlerFunc) {
	if a.cfg.Env == "production" {
		a.log.Error("admin_port_shared is IGNORED in production: admin routes must use dedicated port",
			"admin_port", a.cfg.AdminPort)
		return
	}

	a.log.Warn("admin_port_shared is enabled: admin routes exposed on API port (insecure, dev only)")

	// registerRoutes already mounts /api/v1/health, /api/health, and the full
	// admin API surface (/api/v1/admin/* and /api/admin/*) on the main engine
	// (see the NEW-002 block above). gin panics when the same method+path is
	// registered twice, so re-registering them here would abort startup with
	// "handlers are already registered for path ...". Only the routes that are
	// unique to the admin side are added here: the metrics endpoint and the
	// /admin static file server.
	a.mainEngine.GET("/metrics", middleware.AuthRequired(a.cfg, a.db.DB), middleware.RequireAdmin(), gin.WrapH(metrics.Handler()))

	adminFS := http.FS(os.DirFS(webhostDir("admin")))
	adminStaticHandler := http.StripPrefix("/admin", http.FileServer(adminFS))
	a.mainEngine.GET("/admin", gin.WrapH(adminStaticHandler))
	// L1: /admin/health and /admin/healthz must be answered by the health handler
	// rather than the static file server. gin forbids a static segment (e.g.
	// /admin/health) alongside the catch-all /admin/*filepath, so the aliases are
	// resolved from inside the catch-all handler.
	a.mainEngine.GET("/admin/*filepath", func(c *gin.Context) {
		switch c.Param("filepath") {
		case "/health", "/healthz":
			healthHandler(c)
		default:
			gin.WrapH(adminStaticHandler)(c)
		}
	})
}

func (a *App) registerAvatarRoutes() {
	avatarFS := http.Dir("./storage/avatars")
	a.mainEngine.GET("/api/avatars/:userID/*filepath", func(c *gin.Context) {
		userID := c.Param("userID")
		filePath := c.Param("filepath")
		// Path traversal protection using forward slashes for http.Dir
		cleanPath := userID + "/" + strings.TrimPrefix(path.Clean("/"+filePath), "/")
		if strings.Contains(cleanPath, "..") {
			errors.JSONError(c, errors.New(errors.AUTH_FORBIDDEN, "invalid path"))
			return
		}
		c.FileFromFS(cleanPath, avatarFS)
	})
	// Whiteboard thumbnail static files
	// Use the same configured data root as the upload/read handlers. Production
	// deployments commonly set LocalDataPath outside the process working
	// directory, so a hard-coded ./storage path would serve a different folder.
	whiteboardFS := http.Dir(filepath.Join(a.cfg.LocalDataPath, "whiteboard"))
	a.mainEngine.GET("/storage/whiteboard/*filepath", func(c *gin.Context) {
		filePath := c.Param("filepath")
		cleanPath := strings.TrimPrefix(path.Clean("/"+filePath), "/")
		if strings.Contains(cleanPath, "..") {
			errors.JSONError(c, errors.New(errors.AUTH_FORBIDDEN, "invalid path"))
			return
		}
		c.FileFromFS(cleanPath, whiteboardFS)
	})
}

func (a *App) registerWebSocketRoute() {
	a.mainEngine.GET("/ws",
		middleware.CSRFProtection(a.cfg),
		func(c *gin.Context) {
			// M6: Prefer the JWT in the Sec-WebSocket-Protocol header so it does
			// not leak into access/proxy logs. The client sends one protocol
			// entry of the form "access_token.<token>" plus a fallback "rrt"
			// entry; we negotiate "rrt" in the response.
			token := ""
			if protoHeader := c.GetHeader("Sec-WebSocket-Protocol"); protoHeader != "" {
				for _, p := range strings.Split(protoHeader, ",") {
					p = strings.TrimSpace(p)
					if strings.HasPrefix(p, "access_token.") {
						token = strings.TrimPrefix(p, "access_token.")
					}
					if p == "rrt" {
						// Only negotiate a subprotocol we recognise.
						c.Set("ws_subprotocol", "rrt")
					}
				}
			}
			// Legacy fallback: query string. Per WebSocket protocol spec §2.1,
			// the canonical query parameter name is `token`; `access_token`
			// is kept for backward compatibility with older clients.
			if token == "" {
				token = c.Query("token")
				if token != "" {
					a.log.Warn("websocket token received via 'token' query parameter; prefer Sec-WebSocket-Protocol header", "client_ip", c.ClientIP())
				}
			}
			if token == "" {
				token = c.Query("access_token")
				if token != "" {
					a.log.Warn("websocket token received via legacy 'access_token' query parameter; this is deprecated and insecure", "client_ip", c.ClientIP())
				}
			}
			if token != "" {
				claims, err := middleware.ParseToken(token, a.cfg)
				if err == nil && claims.Type == "access" {
					// Validate TokenVersion against database
					var user model.User
					if err := a.db.DB.First(&user, "id = ?", claims.UserID).Error; err == nil {
						if claims.TokenVersion == user.TokenVersion {
							c.Set("user_id", claims.UserID)
							c.Set("username", claims.Username)
							// A4-S2：把 session_id 声明带入上下文，供
							// hub.HandleWebSocket 存到连接上（security_alert
							// 广播据此跳过触发登录的那条新连接）。
							c.Set("session_id", claims.SessionID)
							a.hub.HandleWebSocket(c)
							return
						}
					}
				}
			}
			errors.JSONError(c, errors.New(errors.AUTH_UNAUTHORIZED, "websocket authentication required"))
		})
}

func (a *App) registerSPAFallback() {
	voiceStaticPath := webhostDir("voice")
	a.mainEngine.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") ||
			strings.HasPrefix(c.Request.URL.Path, "/admin") ||
			c.Request.URL.Path == "/metrics" {
			errors.JSONError(c, errors.ErrNotFound)
			return
		}

		requestedPath := c.Request.URL.Path
		filePath := voiceStaticPath + requestedPath

		// Check if file exists and is not a directory
		info, err := os.Stat(filePath)
		fileExists := err == nil && info != nil && !info.IsDir()

		// For static assets, return 404 if file doesn't exist
		isStaticAsset := strings.HasPrefix(requestedPath, "/assets/") ||
			strings.HasSuffix(requestedPath, ".js") ||
			strings.HasSuffix(requestedPath, ".css") ||
			strings.HasSuffix(requestedPath, ".svg") ||
			strings.HasSuffix(requestedPath, ".png") ||
			strings.HasSuffix(requestedPath, ".jpg") ||
			strings.HasSuffix(requestedPath, ".ico") ||
			requestedPath == "/vite.svg"

		if isStaticAsset && !fileExists {
			c.String(http.StatusNotFound, "not found")
			return
		}

		// Serve existing static files directly
		if fileExists {
			c.FileFromFS(requestedPath, http.Dir(voiceStaticPath))
			return
		}

		// SPA fallback: return index.html with no-cache header
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.File(voiceStaticPath + "/index.html")
	})
}
