package bots

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles HTTP requests for bots
type Handler struct {
	service *Service
	db      *gorm.DB
	cfg     *config.Config
}

// NewHandler creates a new bot handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{service: NewService(db, cfg, hub), db: db, cfg: cfg}
}

// resolveDisplayName 查询用户显示名（username 已退役，展示一律用 displayName）。
// 查库失败回退传入的 fallback。
func (h *Handler) resolveDisplayName(userID, fallback string) string {
	if userID == "" {
		return fallback
	}
	var u model.User
	if err := h.db.Select("display_name, username").First(&u, "id = ?", userID).Error; err != nil {
		return fallback
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	if u.Username != "" {
		return u.Username
	}
	return fallback
}

// HealthCheck returns the NeteaseCloudMusicApi dependency status.
// It is intended to be called once at application startup for observability.
func (h *Handler) HealthCheck() error {
	return h.service.NeteaseHealthCheck()
}

// Stop stops the handler-owned background cleanup loop.
func (h *Handler) Stop() {
	h.service.Stop()
}

// currentActor returns the user ID (from JWT) and bot ID (from X-Bot-Token).
// For requests authenticated via X-Bot-Token, userID is empty and botID is set.
func (h *Handler) currentActor(c *gin.Context) (userID, botID string) {
	userID = middleware.GetUserID(c)
	if bid, ok := c.Get("bot_id"); ok {
		botID = bid.(string)
	}
	return userID, botID
}

// actorID returns the caller identifier to store as addedBy. For bot-token
// auth there is no user identity, so an empty string is returned.
func (h *Handler) actorID(c *gin.Context) string {
	return middleware.GetUserID(c)
}

// authorizeBot ensures the caller may operate on the path-supplied bot.
func (h *Handler) authorizeBot(c *gin.Context, botID string) bool {
	userID, _ := h.currentActor(c)
	if err := h.service.AuthorizeBot(userID, botID); err != nil {
		errors.JSONError(c, err)
		return false
	}
	return true
}

// checkBotsModuleEnabled returns 503 when the bots module has been explicitly
// disabled via module_runtime_status. When no record exists the module defaults
// to enabled for backward compatibility.
func (h *Handler) checkBotsModuleEnabled(c *gin.Context) {
	var status model.ModuleRuntimeStatus
	if err := h.db.Where("module_name = ?", "bots").First(&status).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.Next()
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		c.Abort()
		return
	}
	if !status.Enabled {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "bots module disabled"})
		return
	}
	c.Next()
}

// RegisterRoutes registers bot routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	// Worker 回调使用独立的内部鉴权，不能经过用户 JWT 中间件。
	r.GET("/internal/music-bot/resolve", h.ResolveWorkerTrack)

	bots := r.Group("/bots")
	bots.Use(middleware.AuthRequired(h.cfg, h.db))
	// M22: BotToken verification (optional — falls back to user auth if no X-Bot-Token header)
	bots.Use(middleware.BotTokenRequired(h.db))
	bots.Use(h.checkBotsModuleEnabled)
	{
		// Read-only — any authenticated user can view status & queue
		bots.GET("/:botId/status", h.GetStatus)
		bots.GET("/:botId/queue", h.GetBotQueue)

		// Control operations — any authenticated user (per design doc §7.7.1)
		bots.POST("/:botId/queue", h.AddToQueue)
		bots.DELETE("/queue/:id", h.RemoveFromQueue)
		bots.POST("/:botId/queue/clear", h.ClearQueue)
		bots.PUT("/:botId/queue/reorder", h.ReorderQueue)
		bots.POST("/:botId/skip", h.Skip)
		bots.POST("/:botId/previous", h.Previous)
		bots.POST("/:botId/pause", h.Pause)
		bots.POST("/:botId/resume", h.Resume)
		bots.POST("/:botId/seek", h.Seek)
		bots.POST("/:botId/playnow", h.PlayNow)
		bots.POST("/:botId/mode", h.SetPlayMode)
		bots.POST("/:botId/volume", h.SetVolume)
		bots.POST("/:botId/tts", h.TTS)
		bots.POST("/tts/consent", h.RecordTTSConsent)
		// TTS consent 状态查询（前端打开 TTS 面板时调用，取代 localStorage 读取）
		bots.GET("/tts/consent", h.GetTTSConsent)
		// TTS consent 撤回（与 RecordTTSConsent 对称，写入 revoked_at 审计字段）
		bots.DELETE("/tts/consent", h.RevokeTTSConsent)
		// TTS 音色能力发现（N8）：返回引擎真实音色清单，取代客户端硬编码音色名
		// （静态段优先于下方 /tts/:jobId 匹配，与 /tts/consent 同理）
		bots.GET("/tts/voices", h.GetTTSVoices)
		// TTS job 状态兜底查询（WS 断线重连后用，参考 Replicate GET /predictions/:id）
		bots.GET("/tts/:jobId", h.GetTTSJobStatus)

		// Channel-based aliases used by the frontend (resolve bot via channelId)
		bots.GET("/status", h.GetStatusByChannel)
		bots.GET("/queue/:channelId", h.GetBotQueueByChannel)
		bots.POST("/queue", h.AddToQueueByChannel)
		bots.POST("/skip", h.SkipByChannel)
		bots.POST("/previous", h.PreviousByChannel)
		bots.POST("/pause", h.PauseByChannel)
		bots.POST("/resume", h.ResumeByChannel)
		bots.POST("/seek", h.SeekByChannel)
		bots.POST("/playnow", h.PlayNowByChannel)
		bots.POST("/mode", h.SetPlayModeByChannel)
		bots.POST("/volume", h.SetVolumeByChannel)
		bots.POST("/tts", h.TTSByChannel)
		bots.POST("/queue/clear", h.ClearQueueByChannel)
		bots.PUT("/queue/reorder", h.ReorderQueueByChannel)
		bots.POST("/uploads/:id/enqueue", h.EnqueueUploadByChannel)

		bots.POST("/upload", h.UploadAudio)
		bots.GET("/uploads", h.GetUploads)
		bots.DELETE("/uploads/:id", h.DeleteUpload)

		// Upload audio enqueue (explicit botId variant)
		bots.POST("/:botId/uploads/:id/enqueue", h.EnqueueUpload)

		// Queue priority (move to head)
		bots.POST("/:botId/queue/:id/priority", h.PriorityQueue)

		// Netease proxy — read-only
		bots.GET("/netease/search", h.NeteaseSearch)
		bots.GET("/netease/song/:id/url", h.NeteaseSongURL)
		bots.GET("/netease/lyric/:id", h.NeteaseLyric)
		bots.GET("/netease/playlist/:id", h.NeteasePlaylist)
		bots.GET("/netease/playlists", h.NeteasePlaylists)
		bots.GET("/netease/recommend/songs", h.NeteaseRecommendSongs)

		// Netease QR login
		bots.GET("/netease/login/qr/key", h.NeteaseQRKey)
		bots.GET("/netease/login/qr/create", h.NeteaseQRCreate)
		bots.GET("/netease/login/qr/check", h.NeteaseQRCheck)
		bots.GET("/netease/login/status", h.NeteaseLoginStatus)
		bots.GET("/netease/user/account", h.NeteaseUserAccount)

		// Netease password/cookie login & logout
		bots.POST("/netease/login/password", h.NeteasePasswordLogin)
		bots.POST("/netease/login/cookie", h.NeteaseCookieLogin)
		bots.POST("/netease/logout", h.NeteaseLogout)

		// Shared folder audio import
		bots.GET("/shared-folder/audio", h.SharedFolderAudio)
		bots.POST("/shared-folder/import", h.SharedFolderImport)

		// M22: Bot token management (OWNER/ADMIN only — enforced inside handlers)
		bots.POST("/tokens", h.CreateBotToken)
		bots.GET("/tokens/:spaceId", h.ListBotTokens)
		bots.DELETE("/tokens/:id", h.DeleteBotToken)
	}

	// Legacy /netease aliases under /api/v1/bots/netease for frontend compatibility
	// (The parent group r is already /api/v1, so /netease here becomes /api/v1/netease)
	// We keep these for backward compatibility but they are deprecated.
	netease := r.Group("/netease")
	netease.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		netease.GET("/search", h.NeteaseSearch)
		netease.GET("/song/:id/url", h.NeteaseSongURL)
		netease.GET("/song/url", h.NeteaseSongURLByQuery) // query param: ?id=xxx
		netease.GET("/lyric/:id", h.NeteaseLyric)
		netease.GET("/playlist/:id", h.NeteasePlaylist)
		netease.GET("/playlists", h.NeteasePlaylists)
		netease.GET("/recommend/songs", h.NeteaseRecommendSongs)
		netease.GET("/login/qr/key", h.NeteaseQRKey)
		netease.GET("/login/qr/create", h.NeteaseQRCreate)
		netease.GET("/login/qr/check", h.NeteaseQRCheck)
		netease.GET("/login/status", h.NeteaseLoginStatus)
		netease.GET("/user/account", h.NeteaseUserAccount)
		netease.GET("/user/playlist", h.NeteasePlaylistsByUID)
	}
}

// authorizeMusicWorker verifies calls originating from the local music Worker.
// An explicit shared token is required in production. When no token is
// configured, only direct loopback calls are accepted for local development.
func (h *Handler) authorizeMusicWorker(c *gin.Context) bool {
	expected := h.cfg.MusicBotWorkerToken
	if expected != "" {
		provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1 {
			return true
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid worker token"})
		return false
	}

	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		host = c.Request.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "worker resolver is loopback-only"})
	return false
}

// ResolveWorkerTrack lazily resolves a queue track immediately before playback.
func (h *Handler) ResolveWorkerTrack(c *gin.Context) {
	if !h.authorizeMusicWorker(c) {
		return
	}
	source := c.Query("source")
	trackID := c.Query("trackId")
	if source == "" || trackID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source and trackId are required"})
		return
	}
	audioURL, expiresAt, err := h.service.ResolveWorkerTrack(source, trackID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": audioURL, "expiresAt": expiresAt})
}

// GetStatus returns bot status for a bot
func (h *Handler) GetStatus(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	status, err := h.service.GetStatus(botID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, status)
}

// GetBotQueue returns the play queue for a bot
func (h *Handler) GetBotQueue(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	queue, err := h.service.GetBotQueue(botID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"items": queue})
}

// AddToQueue adds a track to the queue
func (h *Handler) AddToQueue(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	var body struct {
		TrackID  string `json:"trackId"`
		Title    string `json:"title"`
		Artist   string `json:"artist"`
		Duration int    `json:"duration"`
		Cover    string `json:"cover"`
		Album    string `json:"album"`
		Source   string `json:"source"`
		Position *int   `json:"position"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	addedBy := h.actorID(c)
	queue, err := h.service.AddToQueue(botID, body.TrackID, body.Title, body.Artist, body.Duration, body.Cover, body.Album, body.Source, addedBy, body.Position)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, queue)
}

// RemoveFromQueue removes a track from the queue
func (h *Handler) RemoveFromQueue(c *gin.Context) {
	id := c.Param("id")

	var item model.BotPlayQueue
	if err := h.db.Where("id = ?", id).First(&item).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.ErrNotFound)
		} else {
			errors.JSONError(c, errors.ErrInternal)
		}
		return
	}
	if !h.authorizeBot(c, item.BotID) {
		return
	}

	if err := h.service.RemoveFromQueue(id); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "removed from queue"})
}

// ClearQueue clears the queue
func (h *Handler) ClearQueue(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	if err := h.service.ClearQueue(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "queue cleared"})
}

// ReorderQueue reorders the queue
func (h *Handler) ReorderQueue(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	var body struct {
		QueueIDs []string `json:"queueIds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if err := h.service.ReorderQueue(botID, body.QueueIDs); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "queue reordered"})
}

// Skip skips current track
func (h *Handler) Skip(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}
	if err := h.service.Skip(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "skipped"})
}

// Previous plays the previous track from history
func (h *Handler) Previous(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}
	if err := h.service.PlayPrevious(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "previous"})
}

// Pause pauses playback
func (h *Handler) Pause(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}
	if err := h.service.Pause(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "paused"})
}

// Resume resumes playback
func (h *Handler) Resume(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}
	if err := h.service.Resume(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "resumed"})
}

// PlayNow starts playing a specific track immediately
func (h *Handler) PlayNow(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	var body struct {
		QueueID string `json:"queueId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if err := h.service.PlayNow(botID, body.QueueID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "playing now"})
}

// Seek seeks to a specific time in the current track
func (h *Handler) Seek(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	var body struct {
		Time int `json:"time"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if err := h.service.Seek(botID, body.Time); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "seeked", "time": body.Time})
}

// SetPlayMode sets the bot play mode
func (h *Handler) SetPlayMode(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	var body struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if err := h.service.SetPlayMode(botID, body.Mode); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "mode set"})
}

// SetVolume sets playback volume
func (h *Handler) SetVolume(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	var body struct {
		Volume int `json:"volume"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}
	if err := h.service.SetVolume(botID, body.Volume); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"volume": body.Volume})
}

// TTS generates TTS audio
func (h *Handler) TTS(c *gin.Context) {
	botID := c.Param("botId")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	var body struct {
		Text   string  `json:"text"`
		Voice  string  `json:"voice"`
		Speed  float64 `json:"speed"`
		Pitch  float64 `json:"pitch"`
		Volume float64 `json:"volume"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	// M18: Pass displayName so PlayTTS can synthesize "[显示名] 说" prefix audio cue
	username := h.resolveDisplayName(userID, middleware.GetUsername(c))
	jobID, err := h.service.SynthesizeTTS(userID, username, botID, body.Text, body.Voice, body.Speed, body.Pitch, body.Volume)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"jobId":  jobID,
		"status": "pending",
		"text":   body.Text,
	})
}

// RecordTTSConsent records the current user's consent to TTS data processing.
func (h *Handler) RecordTTSConsent(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.service.RecordTTSConsent(userID); err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("failed to record tts consent"))
		return
	}
	errors.Success(c, gin.H{"message": "consent recorded"})
}

// GetTTSConsent 查询当前用户的 TTS consent 状态。
//
// 用途：前端打开 TTS 面板时调用，取代 localStorage 读取，避免共享设备的跨用户持久化问题。
// 设计参考：Mattermost Preferences API（后端为权威，前端不直接读 localStorage）。
// 路由：GET /api/v1/bots/tts/consent
//
// 响应：200 OK + { "consented": bool }
func (h *Handler) GetTTSConsent(c *gin.Context) {
	userID := middleware.GetUserID(c)
	consented, err := h.service.GetTTSConsent(userID)
	if err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("failed to query tts consent"))
		return
	}
	errors.Success(c, gin.H{"consented": consented})
}

// RevokeTTSConsent 撤回当前用户的 TTS consent。
//
// 用途：与 RecordTTSConsent 对称，写入 consented=false 与 revoked_at 审计字段。
// 路由：DELETE /api/v1/bots/tts/consent
//
// 响应：200 OK + { "message": "consent revoked" }
func (h *Handler) RevokeTTSConsent(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.service.RevokeTTSConsent(userID); err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("failed to revoke tts consent"))
		return
	}
	errors.Success(c, gin.H{"message": "consent revoked"})
}

// GetTTSJobStatus 查询 TTS 任务状态（兜底查询接口）
//
// 用途：WS 断线重连后，前端通过此接口补偿查询 pending TTS 任务的状态，
// 避免因 WS 断线期间 goroutine 失败恰好发生导致前端永久 stuck 在 "pending"。
//
// 参考：Replicate GET /v1/predictions/:id 兜底查询模式
// 路由：GET /api/v1/bots/tts/:jobId
//
// 响应：
//
//	200 OK + job 对象（status/error/progress/timestamps）
//	404 Not Found（job 已过期 5 分钟 TTL 或从未创建）
func (h *Handler) GetTTSJobStatus(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("jobId required"))
		return
	}

	job := h.service.GetTTSJobStatus(jobID)
	if job == nil {
		// job 已过期（5 分钟 TTL）或从未创建：返回 404，前端应清除 loading 状态
		errors.JSONError(c, errors.ErrNotFound.WithDetails("tts job not found or expired"))
		return
	}

	errors.Success(c, job)
}

// GetTTSVoices 返回 TTS 引擎真实可用的音色清单（N8 音色真实化）。
//
// 用途：客户端初始化时拉取本接口，音色下拉只展示服务端返回的真实清单，
// 不再使用硬编码音色名（此前默认值是 Azure 命名的 zh-CN-XiaoxiaoNeural，
// 与 Sherpa-ONNX 引擎完全不匹配）。
//
// 路由：GET /api/v1/bots/tts/voices
//
// 响应：
//
//	200 OK + {available, engine, model, numSpeakers, voices: [{id, name}]}
//	引擎未初始化/模型缺失时 available=false、voices 为空数组
//	（前端据此显示「使用引擎默认音色」，不阻断 TTS 功能）
func (h *Handler) GetTTSVoices(c *gin.Context) {
	errors.Success(c, h.service.GetTTSVoices())
}

// UploadAudio handles audio upload
func (h *Handler) UploadAudio(c *gin.Context) {
	userID := middleware.GetUserID(c)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "file required"))
		return
	}
	defer file.Close()

	upload, err := h.service.UploadAudio(userID, header.Filename, header.Header.Get("Content-Type"), header.Size, file)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{
		"id":        upload.ID,
		"name":      upload.Title,
		"duration":  upload.Duration,
		"fileSize":  upload.FileSize,
		"mimeType":  upload.MimeType,
		"createdAt": upload.CreatedAt,
	})
}

// GetUploads returns uploaded audio list
func (h *Handler) GetUploads(c *gin.Context) {
	userID := middleware.GetUserID(c)
	uploads, err := h.service.GetUploads(userID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	items := make([]map[string]interface{}, 0, len(uploads))
	for _, u := range uploads {
		items = append(items, map[string]interface{}{
			"id":               u.ID,
			"name":             u.Title,
			"duration":         u.Duration,
			"fileSize":         u.FileSize,
			"mimeType":         u.MimeType,
			"createdAt":        u.CreatedAt,
			"uploaderUsername": u.UploaderUsername,
		})
	}
	errors.Success(c, gin.H{"items": items})
}

// DeleteUpload deletes an uploaded audio
func (h *Handler) DeleteUpload(c *gin.Context) {
	userID := middleware.GetUserID(c)
	id := c.Param("id")
	if err := h.service.DeleteUpload(userID, id); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "deleted"})
}

// NeteaseSearch searches songs on Netease
func (h *Handler) NeteaseSearch(c *gin.Context) {
	keyword := c.Query("keyword")
	if keyword == "" {
		keyword = c.Query("keywords")
	}
	results, err := h.service.GetNeteaseSearch(keyword, 20)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, results)
}

// NeteaseSongURL gets song URL (path param: /song/:id/url)
func (h *Handler) NeteaseSongURL(c *gin.Context) {
	id := c.Param("id")
	result, err := h.service.NeteaseProxy("/song/url?id=" + url.QueryEscape(id))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseSongURLByQuery gets song URL (query param: ?id=xxx) — frontend compatibility
func (h *Handler) NeteaseSongURLByQuery(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "id required"))
		return
	}
	result, err := h.service.NeteaseProxy("/song/url?id=" + url.QueryEscape(id))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseRecommendSongs gets daily recommended songs
func (h *Handler) NeteaseRecommendSongs(c *gin.Context) {
	result, err := h.service.GetNeteaseRecommend()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteasePlaylistsByUID gets playlists by user ID (query param: ?uid=xxx)
func (h *Handler) NeteasePlaylistsByUID(c *gin.Context) {
	uid := c.Query("uid")
	result, err := h.service.GetNeteasePlaylists(uid)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseLyric gets song lyric
func (h *Handler) NeteaseLyric(c *gin.Context) {
	id := c.Param("id")
	// Use /lyric/new which is more stable than /lyric in recent Netease API versions
	result, err := h.service.NeteaseProxy("/lyric/new?id=" + url.QueryEscape(id))
	if err != nil {
		// Fallback to legacy /lyric endpoint
		result, err = h.service.NeteaseProxy("/lyric?id=" + url.QueryEscape(id))
		if err != nil {
			errors.JSONError(c, err)
			return
		}
	}
	errors.Success(c, result)
}

// NeteasePlaylist gets playlist details
func (h *Handler) NeteasePlaylist(c *gin.Context) {
	id := c.Param("id")
	result, err := h.service.NeteaseProxy("/playlist/detail?id=" + url.QueryEscape(id))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteasePlaylists gets user's playlists
func (h *Handler) NeteasePlaylists(c *gin.Context) {
	uid := c.Query("uid")
	if uid == "" {
		// 尝试从已登录的用户信息中获取 uid
		profile, err := h.service.NeteaseProxy("/user/account")
		if err == nil {
			if data, ok := profile["account"].(map[string]interface{}); ok {
				if id, ok := data["id"].(float64); ok {
					uid = strconv.FormatFloat(id, 'f', 0, 64)
				}
			}
		}
	}
	if uid == "" {
		errors.Success(c, map[string]interface{}{"code": 200, "playlist": []interface{}{}})
		return
	}
	result, err := h.service.NeteaseProxy("/user/playlist?uid=" + url.QueryEscape(uid))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseQRKey generates a QR login key
func (h *Handler) NeteaseQRKey(c *gin.Context) {
	key, err := h.service.GetNeteaseQRKey()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"code": 200, "data": gin.H{"unikey": key}})
}

// NeteaseQRCreate creates a QR code image
func (h *Handler) NeteaseQRCreate(c *gin.Context) {
	key := c.Query("key")
	qrimg := c.Query("qrimg") == "true"
	result, err := h.service.GetNeteaseQRCreate(key, qrimg)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseQRCheck checks QR scan status
func (h *Handler) NeteaseQRCheck(c *gin.Context) {
	key := c.Query("key")
	result, err := h.service.GetNeteaseQRCheck(key)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseLoginStatus gets login status
func (h *Handler) NeteaseLoginStatus(c *gin.Context) {
	result, err := h.service.GetNeteaseLoginStatus()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseUserAccount gets user account info
func (h *Handler) NeteaseUserAccount(c *gin.Context) {
	result, err := h.service.GetNeteaseUserAccount()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// EnqueueUpload adds an uploaded audio to the play queue
func (h *Handler) EnqueueUpload(c *gin.Context) {
	botID := c.Param("botId")
	id := c.Param("id")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}

	queue, err := h.service.EnqueueUpload(botID, id, h.actorID(c))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, queue)
}

// PriorityQueue moves a queue item to the head (position 0) and starts playing
func (h *Handler) PriorityQueue(c *gin.Context) {
	botID := c.Param("botId")
	id := c.Param("id")
	if botID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "botId required"))
		return
	}
	if !h.authorizeBot(c, botID) {
		return
	}
	if err := h.service.PriorityQueue(botID, id); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "moved to priority"})
}

// NeteasePasswordLogin handles phone/password login for Netease
func (h *Handler) NeteasePasswordLogin(c *gin.Context) {
	var body struct {
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Phone == "" || body.Password == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("phone and password required"))
		return
	}
	result, err := h.service.NeteasePasswordLogin(body.Phone, body.Password)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseCookieLogin handles cookie-based login for Netease
func (h *Handler) NeteaseCookieLogin(c *gin.Context) {
	var body struct {
		Cookie string `json:"cookie"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Cookie == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("cookie required"))
		return
	}
	result, err := h.service.NeteaseCookieLogin(body.Cookie)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// NeteaseLogout logs out from Netease
func (h *Handler) NeteaseLogout(c *gin.Context) {
	if err := h.service.NeteaseLogout(); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "logged out"})
}

// SharedFolderAudio lists audio files in shared folders
func (h *Handler) SharedFolderAudio(c *gin.Context) {
	result, err := h.service.GetSharedFolderAudio()
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, result)
}

// SharedFolderImport imports an audio file from shared folder to uploads
func (h *Handler) SharedFolderImport(c *gin.Context) {
	var body struct {
		FilePath string `json:"filePath"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.FilePath == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("filePath required"))
		return
	}
	userID := middleware.GetUserID(c)
	upload, err := h.service.ImportSharedFolderAudio(userID, body.FilePath)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, upload)
}

// ===== M22: Bot Token Management Handlers =====
//
// These endpoints allow OWNER/ADMIN to create, list, and revoke bot tokens
// for a channel. Tokens enable external bot integrations to invoke bot
// control APIs via the X-Bot-Token header.

// CreateBotToken creates a new bot token for a space (OWNER/ADMIN only)
func (h *Handler) CreateBotToken(c *gin.Context) {
	// M22: Only OWNER and ADMIN can create bot tokens
	if !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.ErrForbidden.WithDetails("only admin/owner can create bot tokens"))
		return
	}
	var body struct {
		SpaceID string  `json:"spaceId"`
		RoomID  *string `json:"roomId"`
		Name    string  `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.SpaceID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("spaceId required"))
		return
	}
	bot, err := h.service.CreateBotToken(body.SpaceID, body.RoomID, body.Name, middleware.GetUserID(c))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	// Return the plaintext token — only time it is exposed to the client
	errors.Success(c, gin.H{
		"id":           bot.ID,
		"spaceId":      bot.SpaceID,
		"outputRoomId": bot.OutputRoomID,
		"name":         bot.Name,
		"token":        bot.Token,
		"createdBy":    bot.CreatedBy,
		"createdAt":    bot.CreatedAt,
	})
}

// ListBotTokens lists all bot tokens for a space (OWNER/ADMIN only)
func (h *Handler) ListBotTokens(c *gin.Context) {
	// M22: Only OWNER and ADMIN can list bot tokens
	if !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.ErrForbidden.WithDetails("only admin/owner can list bot tokens"))
		return
	}
	spaceID := c.Param("spaceId")
	if spaceID == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("spaceId required"))
		return
	}
	items, err := h.service.ListBotTokens(spaceID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"items": items})
}

// DeleteBotToken revokes a bot token by ID (OWNER/ADMIN only)
func (h *Handler) DeleteBotToken(c *gin.Context) {
	// M22: Only OWNER and ADMIN can revoke bot tokens
	if !middleware.IsAdmin(c) {
		errors.JSONError(c, errors.ErrForbidden.WithDetails("only admin/owner can revoke bot tokens"))
		return
	}
	id := c.Param("id")
	if id == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("id required"))
		return
	}
	if err := h.service.DeleteBotToken(id); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "bot token revoked"})
}

// ===== Channel-based aliases =====
//
// These endpoints resolve the target bot from a channelId query/body field
// instead of requiring the client to know the bot's internal ID. They mirror
// the behavior of the /:botId/... handlers and are consumed by the web client.

// resolveChannelBot validates the channelId, finds (or auto-creates) the
// associated bot, and verifies that the caller is allowed to operate on it.
// 当频道没有绑定 bot 时，会自动创建一个默认 MusicBot 并绑定到该频道。
func (h *Handler) resolveChannelBot(c *gin.Context, channelID string) (string, bool) {
	if channelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return "", false
	}
	userID := middleware.GetUserID(c)
	bot, err := h.service.FindOrCreateBotForChannel(channelID, userID)
	if err != nil {
		errors.JSONError(c, err)
		return "", false
	}
	if !h.authorizeBot(c, bot.ID) {
		return "", false
	}
	return bot.ID, true
}

// GetStatusByChannel returns bot status using a channelId query parameter.
func (h *Handler) GetStatusByChannel(c *gin.Context) {
	botID, ok := h.resolveChannelBot(c, c.Query("channelId"))
	if !ok {
		return
	}
	status, err := h.service.GetStatus(botID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, status)
}

// GetBotQueueByChannel returns the play queue using a channelId path parameter.
func (h *Handler) GetBotQueueByChannel(c *gin.Context) {
	botID, ok := h.resolveChannelBot(c, c.Param("channelId"))
	if !ok {
		return
	}
	queue, err := h.service.GetBotQueue(botID)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"items": queue})
}

// SkipByChannel skips the current track using a channelId body field.
func (h *Handler) SkipByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.Skip(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "skipped"})
}

// PauseByChannel pauses playback using a channelId body field.
func (h *Handler) PauseByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.Pause(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "paused"})
}

// PreviousByChannel plays the previous track using a channelId body field.
func (h *Handler) PreviousByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.PlayPrevious(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "previous"})
}

// ResumeByChannel resumes playback using a channelId body field.
func (h *Handler) ResumeByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.Resume(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "resumed"})
}

// SeekByChannel seeks to a specific time using channelId and time body fields.
func (h *Handler) SeekByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
		Time      int    `json:"time"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.Seek(botID, body.Time); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "seeked", "time": body.Time})
}

// PlayNowByChannel starts playing a specific track using channelId and queueId body fields.
func (h *Handler) PlayNowByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
		QueueID   string `json:"queueId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.PlayNow(botID, body.QueueID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "playing now"})
}

// SetPlayModeByChannel sets the play mode using channelId and mode body fields.
func (h *Handler) SetPlayModeByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
		Mode      string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.SetPlayMode(botID, body.Mode); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "mode set"})
}

// SetVolumeByChannel sets the volume using channelId and volume body fields.
func (h *Handler) SetVolumeByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
		Volume    int    `json:"volume"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.SetVolume(botID, body.Volume); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"volume": body.Volume})
}

// TTSByChannel generates TTS audio using a channelId body field.
func (h *Handler) TTSByChannel(c *gin.Context) {
	var body struct {
		ChannelID string  `json:"channelId"`
		Text      string  `json:"text"`
		Voice     string  `json:"voice"`
		Speed     float64 `json:"speed"`
		Pitch     float64 `json:"pitch"`
		Volume    float64 `json:"volume"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}

	userID := middleware.GetUserID(c)
	username := h.resolveDisplayName(userID, middleware.GetUsername(c))
	jobID, err := h.service.SynthesizeTTS(userID, username, botID, body.Text, body.Voice, body.Speed, body.Pitch, body.Volume)
	if err != nil {
		errors.JSONError(c, err)
		return
	}

	errors.Success(c, gin.H{
		"jobId":  jobID,
		"status": "pending",
		"text":   body.Text,
	})
}

// ClearQueueByChannel clears the play queue using a channelId body field.
func (h *Handler) ClearQueueByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.ClearQueue(botID); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "queue cleared"})
}

// ReorderQueueByChannel reorders the play queue using channelId and queueIds body fields.
func (h *Handler) ReorderQueueByChannel(c *gin.Context) {
	var body struct {
		ChannelID string   `json:"channelId"`
		QueueIDs  []string `json:"queueIds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	if err := h.service.ReorderQueue(botID, body.QueueIDs); err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, gin.H{"message": "queue reordered"})
}

// AddToQueueByChannel adds a track to the play queue using a channelId body field.
func (h *Handler) AddToQueueByChannel(c *gin.Context) {
	var body struct {
		ChannelID string `json:"channelId"`
		Title     string `json:"title"`
		Artist    string `json:"artist"`
		Duration  int    `json:"duration"`
		Source    string `json:"source"`
		TrackID   string `json:"trackId"`
		Cover     string `json:"cover"`
		Album     string `json:"album"`
		Position  *int   `json:"position"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}
	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}
	queue, err := h.service.AddToQueue(botID, body.TrackID, body.Title, body.Artist, body.Duration, body.Cover, body.Album, body.Source, "", body.Position)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, queue)
}

// EnqueueUploadByChannel adds an uploaded audio to the play queue using a
// channelId body field. This mirrors TTSByChannel and the other channel-based
// aliases used by the web client.
func (h *Handler) EnqueueUploadByChannel(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "id required"))
		return
	}

	var body struct {
		ChannelID string `json:"channelId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ChannelID == "" {
		errors.JSONError(c, errors.New(errors.SYSTEM_BAD_REQUEST, "channelId required"))
		return
	}

	botID, ok := h.resolveChannelBot(c, body.ChannelID)
	if !ok {
		return
	}

	queue, err := h.service.EnqueueUpload(botID, id, h.actorID(c))
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	errors.Success(c, queue)
}
