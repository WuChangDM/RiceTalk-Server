// Package server wires and runs the RidgeRiceTalk HTTP/WebSocket application.
package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/core/version"
	"ridgericetalk/features/cloudfs"
	"ridgericetalk/features/minigames"
	"ridgericetalk/features/schedule"
	"ridgericetalk/features/sharedoc"
	"ridgericetalk/internal/admin"
	"ridgericetalk/internal/auth"
	"ridgericetalk/internal/bots"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/database"
	cacheinfra "ridgericetalk/internal/infra/cache"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/livekitmgr"
	"ridgericetalk/internal/logger"
	"ridgericetalk/internal/message"
	"ridgericetalk/internal/metrics"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/neteaseapi"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/serverstate"
	"ridgericetalk/internal/sfx"
	"ridgericetalk/internal/storage"
	"ridgericetalk/internal/thirdparty"
	"ridgericetalk/internal/user"
	"ridgericetalk/internal/voice"
	"ridgericetalk/middleware"
)

// safeGo runs a function in a goroutine with panic recovery.
func safeGo(fn func(), moduleName string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "[PANIC] module=%s recover=%v\n", moduleName, r)
			}
		}()
		fn()
	}()
}

// App holds the wired application dependencies and lifecycle state.
type App struct {
	cfg                 *config.Config
	log                 *logger.Logger
	db                  *database.DB
	stateMgr            *serverstate.Manager
	messageService      *message.Service
	cloudfsHandler      *cloudfs.Handler
	authHandler         *auth.Handler
	appStorage          storage.Storage
	hub                 *realtime.Hub
	voiceHandler        *voice.Handler
	botsHandler         *bots.Handler
	sharedocHandler     *sharedoc.Handler
	scheduleHandler     *schedule.Handler
	minigamesHandler    *minigames.Handler
	sfxService          *sfx.Service
	globalLimiter       *middleware.RateLimiter
	adminWSHandler      *admin.AdminWSHandler
	adminSvc            *admin.Service
	breakGlassProtector *middleware.OwnerBreakGlassProtector
	livekitMgr          *livekitmgr.Manager
	neteaseMgr          *neteaseapi.Manager
	mainEngine          *gin.Engine
	adminEngine         *gin.Engine
	mainServer          *http.Server
	adminServer         *http.Server
	backgroundCtx       context.Context
	backgroundCancel    context.CancelFunc
}

// NewApp loads config, initializes all dependencies and returns a runnable App.
// The supplied exitFunc is installed on the logger so that log.Fatal behaves
// the same as in the previous cmd/server/main.go implementation.
func NewApp(cfg *config.Config, exitFunc func(int)) (*App, error) {
	// Wire runtime-configurable security parameters
	crypto.SetBcryptCost(cfg.BcryptCost)

	// Initialize logger
	log, err := logger.New(cfg.LogLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to init logger: %w", err)
	}
	log.ExitFunc = exitFunc
	defer func() {
		if err != nil {
			log.Sync()
		}
	}()

	log.Info("RidgeRiceTalk server starting...", "version", version.Server, "env", cfg.Env)

	// Start LiveKit subprocess (auto-discover, auto-config, auto-restart)
	lkMgr := livekitmgr.New(log)
	if err := lkMgr.Start(cfg); err != nil {
		if cfg.Env == "production" {
			log.Fatal("failed to start livekit", "error", err)
		} else {
			log.Warn("livekit auto-start failed, voice features may be unavailable", "error", err)
		}
	}

	// Start NeteaseCloudMusicApi subprocess for music bots
	neteaseMgr := neteaseapi.New(log)
	if err := neteaseMgr.Start(cfg); err != nil {
		log.Warn("netease api auto-start failed, music bot may be unavailable", "error", err)
	}

	// Resolve FFmpeg binaries (PATH or embedded download) for music bot/TTS.
	if err := thirdparty.ResolveFFmpeg(cfg, func(msg string, args ...any) {
		log.Info(fmt.Sprintf(msg, args...))
	}); err != nil {
		log.Warn("ffmpeg resolution failed, music bot and TTS may be unavailable", "error", err)
	} else {
		log.Info("ffmpeg resolved", "path", cfg.FFmpegBinaryPath, "ffprobe", cfg.FFprobeBinaryPath)
	}

	// Initialize ID generator
	if err := idgen.Init(1, 1); err != nil {
		log.Fatal("failed to init id generator", "error", err)
	}

	// Initialize database
	db, err := database.New(cfg.DatabaseURL, log)
	if err != nil {
		log.Fatal("failed to connect database", "error", err)
	}

	// Run migrations
	// H36: 生产环境（PostgreSQL）AutoMigrate 已禁用，需通过 'go run ./cmd/migrate up' 执行版本化迁移
	// 开发环境（SQLite）继续使用 AutoMigrate
	if err := database.Migrate(db, log); err != nil {
		log.Fatal("failed to run migrations", "error", err)
	}

	// H36: 生产环境提示运维人员使用版本化迁移
	if cfg.Env == "production" && cfg.DBDriver == "postgres" {
		log.Warn("生产环境 AutoMigrate 已禁用。请确保已执行 'go run ./cmd/migrate up' 应用版本化迁移。")
	}

	// Initialize server lifecycle state manager
	stateMgr, err := serverstate.NewManager(cfg)
	if err != nil {
		log.Fatal("failed to init server state manager", "error", err)
	}

	// Migration path: if no persisted state file exists but the database already
	// contains users (deployments created before the state manager was
	// introduced), complete bootstrap with sensible defaults.
	if !stateMgr.IsInitialized() {
		var userCount int64
		if err := db.DB.Model(&model.User{}).Count(&userCount).Error; err != nil {
			log.Fatal("failed to check existing user count for state migration", "error", err)
		} else if userCount > 0 {
			log.Info("existing users detected but no server state file found; migrating to state manager")
			network := serverstate.DefaultNetworkConfig()
			network.ExternalHost = stateMgr.ResolveExternalHost()
			if err := stateMgr.CompleteBootstrap(network); err != nil {
				log.Fatal("failed to migrate server state", "error", err)
			}
			log.Info("server state migration completed")
		}
	}

	// C5: shared TTL cache for Repository cache wrappers.
	var sharedCache cacheinfra.Cache
	ristrettoCache, rerr := cacheinfra.NewRistrettoCache(cfg.CacheMaxCost, cfg.CacheNumCounters, 64)
	if rerr != nil {
		log.Warn("ristretto init failed, fallback to memoryCache", "error", rerr)
		sharedCache = cacheinfra.NewMemoryCache()
	} else {
		sharedCache = ristrettoCache
		log.Info("ristretto cache initialized",
			"max_cost", cfg.CacheMaxCost, "num_counters", cfg.CacheNumCounters)
	}

	// C6: cache-wrapped repositories for auth/message services.
	userRepo := cacheinfra.NewCachedUserRepository(gormrepo.NewGormUserRepository(db.DB), sharedCache)
	messageRepo := cacheinfra.NewCachedMessageRepository(gormrepo.NewGormMessageRepository(db.DB), sharedCache)

	// Auto-provision bootstrap token if server is not initialized and no valid token exists.
	authSvc := auth.NewServiceWithRepos(db.DB, cfg, userRepo)
	if !stateMgr.IsInitialized() {
		// H1: Owner environment variable pre-configuration — skip bootstrap token flow
		if cfg.Owner.IsConfigured() {
			log.Info("RRT_OWNER_* environment variables detected, starting auto-initialization...")
			displayName := cfg.Owner.DisplayName
			if displayName == "" {
				displayName = cfg.Owner.Username
			}
			spaceName := cfg.Owner.ServerName
			if spaceName == "" {
				spaceName = "RidgeRiceTalk"
			}
			ownerReq := &auth.RegisterRequest{
				Username:    cfg.Owner.Username,
				Email:       cfg.Owner.Email,
				Password:    cfg.Owner.Password,
				DisplayName: displayName,
			}
			_, envErr := authSvc.CreateOwnerFromEnv(ownerReq, spaceName)
			if envErr != nil {
				log.Error("env-based owner initialization failed, falling back to bootstrap token flow", "error", envErr)
			} else {
				log.Info("env-based owner initialization succeeded, Owner created")
				network := serverstate.DefaultNetworkConfig()
				network.ExternalHost = stateMgr.ResolveExternalHost()
				if err := stateMgr.CompleteBootstrap(network); err != nil {
					log.Error("failed to persist bootstrap network config", "error", err)
				}
			}
		}

		// Fall back to bootstrap token flow if env-based init was not attempted or failed
		if !stateMgr.IsInitialized() {
			hasToken, err := authSvc.GetBootstrapStatus()
			if err != nil {
				log.Fatal("failed to check bootstrap status", "error", err)
			}
			if !hasToken {
				token, err := authSvc.CreateBootstrapToken()
				if err != nil {
					log.Error("failed to create bootstrap token", "error", err)
				} else {
					// Write token to a protected file (not logs) for the launcher to read
					tokenFile := cfg.LocalDataPath + "/.bootstrap_token"
					if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
						log.Error("failed to write bootstrap token file", "error", err)
					}
					log.Info("============================================================")
					log.Info("SERVER NOT INITIALIZED — Bootstrap token created")
					log.Info("Use this token to initialize the server via /admin")
					log.Info("============================================================")
				}
			}
		}
	}

	// Initialize storage
	appStorage, err := storage.NewLocalStorage(
		cfg.LocalDataPath,
		cfg.PublicAddress+"/api/v1/files",
	)
	if err != nil {
		log.Fatal("failed to init storage", "error", err)
	}

	// Initialize WebSocket hub
	hub := realtime.NewHub(log)

	// Create voice handler once (used both inside routes and for webhook).
	voiceHandler := voice.NewHandler(db.DB, cfg, hub)
	botsHandler := bots.NewHandler(db.DB, cfg, hub)
	adminSvc := admin.NewService(db.DB, cfg, hub)
	cloudfsHandler := cloudfs.NewHandler(db.DB, cfg, hub)
	messageService := message.NewServiceWithRepos(db.DB, messageRepo)
	messageService.SetLocalDataPath(cfg.LocalDataPath)
	messageService.SetArchiver(cloudfsHandler.Service())

	// Create sfx service (entrance/exit sounds) and wire it into voice so joins/leaves
	// trigger the user's bound sound. Must be built before the voice handler uses it.
	sfxService := sfx.NewService(db.DB, cfg, hub)
	voiceHandler.SetSfxTrigger(sfxService)

	sharedocHandler := sharedoc.NewHandler(db.DB, cfg, hub)
	scheduleHandler := schedule.NewHandler(db.DB, cfg, hub)
	minigamesHandler := minigames.NewHandler(db.DB, cfg, hub)
	breakGlassProtector := middleware.NewOwnerBreakGlassProtector()
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())

	// Seed the built-in preset sounds (embedded in the binary) into the library so
	// every user can pick them without uploading anything.
	sfxService.SeedPresets()

	// Reset all user_presences to offline on startup and reuse this service for
	// Hub presence callbacks.
	userSvc := user.NewService(db.DB)
	if err := userSvc.ResetAllToOffline(); err != nil {
		log.Warn("failed to reset all presence to offline", "error", err)
	}

	// Wire hub callbacks
	wireHubCallbacks(hub, db, userSvc, voiceHandler, sharedocHandler, log)

	safeGo(func() { hub.Run() }, "hub")
	log.Info("websocket hub started")

	// Clean up any voice participant records that may have been left over from
	// the previous server run.
	safeGo(func() { voiceHandler.CleanupOnStartup() }, "voice_startup_cleanup")

	// Setup Gin main engine
	if cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	mainEngine := buildBaseEngine(log, cfg, stateMgr)
	adminEngine := buildBaseEngine(log, cfg, stateMgr)

	globalLimiter := middleware.NewRateLimiter(cfg.RateLimitRequests, cfg.RateLimitBurst)
	// ISSUE-031: Authenticated users get a higher threshold so normal activity
	// (rapidly switching channels, fetching messages, etc.) does not falsely
	// trigger rate limiting.
	globalLimiter.SetAuthLimits(cfg.RateLimitRequests*3, cfg.RateLimitBurst*2)

	app := &App{
		cfg:                 cfg,
		log:                 log,
		db:                  db,
		stateMgr:            stateMgr,
		messageService:      messageService,
		cloudfsHandler:      cloudfsHandler,
		authHandler:         auth.NewHandlerWithState(db.DB, cfg, stateMgr),
		appStorage:          appStorage,
		hub:                 hub,
		voiceHandler:        voiceHandler,
		botsHandler:         botsHandler,
		sharedocHandler:     sharedocHandler,
		scheduleHandler:     scheduleHandler,
		minigamesHandler:    minigamesHandler,
		breakGlassProtector: breakGlassProtector,
		sfxService:          sfxService,
		globalLimiter:       globalLimiter,
		livekitMgr:          lkMgr,
		neteaseMgr:          neteaseMgr,
		adminSvc:            adminSvc,
		mainEngine:          mainEngine,
		adminEngine:         adminEngine,
		backgroundCtx:       backgroundCtx,
		backgroundCancel:    backgroundCancel,
	}

	if err := botsHandler.HealthCheck(); err != nil {
		log.Warn("NeteaseCloudMusicApi unavailable — music bot search will fail", "error", err)
	} else {
		log.Info("NeteaseCloudMusicApi healthy")
	}

	app.registerRoutes()
	app.startBackgroundJobs()

	return app, nil
}

// buildBaseEngine creates a Gin engine with the common middleware stack.
func buildBaseEngine(log *logger.Logger, cfg *config.Config, stateMgr *serverstate.Manager) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.SecurityHeadersMiddleware())
	r.Use(middleware.CORSMiddlewareWithConfig(cfg.AllowedOrigins))
	r.Use(metrics.GinMiddleware())
	r.Use(logger.GinMiddleware(log))
	r.Use(errors.GinErrorHandler())
	r.Use(middleware.InitCheckWithState(stateMgr))
	return r
}

// LogError logs an error through the application logger.
func (a *App) LogError(msg string, keysAndValues ...interface{}) {
	a.log.Error(msg, keysAndValues...)
}

// Sync flushes the logger.
func (a *App) Sync() {
	a.log.Sync()
}

// PrintStartupInfo prints the console banner if ownConsole is true.
func (a *App) PrintStartupInfo(ownConsole bool) {
	if ownConsole {
		printStartupInfo(a.cfg)
	}
}

// Run starts the public and admin HTTP servers and blocks until a shutdown
// signal is received.
func (a *App) Run() error {
	defer a.neteaseMgr.Stop()
	defer a.livekitMgr.Stop()
	if a.botsHandler != nil {
		defer a.botsHandler.Stop()
	}
	defer a.log.Sync()
	defer a.stopBackgroundJobs()

	// Start public API server
	a.mainServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", a.cfg.Port),
		Handler:      a.mainEngine,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		a.log.Info("HTTP server listening", "addr", a.mainServer.Addr)
		if err := a.mainServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.log.Fatal("server failed to start", "error", err)
		}
	}()

	// Start admin server
	a.adminServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", a.cfg.AdminPort),
		Handler:      a.adminEngine,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		a.log.Info("Admin server listening", "addr", a.adminServer.Addr)
		if err := a.adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.log.Fatal("admin server failed to start", "error", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	a.log.Info("shutting down servers...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.mainServer.Shutdown(ctx); err != nil {
		a.log.Error("server forced to shutdown", "error", err)
	}
	if err := a.adminServer.Shutdown(ctx); err != nil {
		a.log.Error("admin server forced to shutdown", "error", err)
	}
	a.log.Info("server exited")
	return nil
}
