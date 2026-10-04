package bots

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/internal/repositories"
)

// Service handles bot business logic
type Service struct {
	db                   *gorm.DB
	cfg                  *config.Config
	hub                  *realtime.Hub
	neteaseClient        *http.Client
	players              map[string]*BotPlayer
	playersMu            sync.RWMutex
	stopCh               chan struct{}
	stopOnce             sync.Once
	botRepo              repositories.BotRepository
	botPlayQueueRepo     repositories.BotPlayQueueRepository
	botUploadAudioRepo   repositories.BotUploadAudioRepository
	uploadCleanupMu      sync.Mutex
	uploadDeleteAttempts map[string]int
	// NJ-21：空闲态自动播放接力标志。AddToQueue 在 bot 空闲时触发异步
	// playNext，而 IsPlaying 要到真正出声才翻转——窗口期内第二次入队会
	// 再次触发 playNext 顶掉第一首。此标志保证每 bot 同一时刻只有一次
	// 空闲接力在进行。
	autoplayMu       sync.Mutex
	autoplayInFlight map[string]bool
}

// NewService creates a new bot service
func NewService(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Service {
	return NewServiceWithRepos(db, cfg, hub, nil, nil, nil)
}

// NewServiceWithRepos creates a new bot service with the given repositories.
func NewServiceWithRepos(db *gorm.DB, cfg *config.Config, hub *realtime.Hub, botRepo repositories.BotRepository, botPlayQueueRepo repositories.BotPlayQueueRepository, botUploadAudioRepo repositories.BotUploadAudioRepository) *Service {
	if botRepo == nil {
		botRepo = gormrepo.NewGormBotRepository(db)
	}
	if botPlayQueueRepo == nil {
		botPlayQueueRepo = gormrepo.NewGormBotPlayQueueRepository(db)
	}
	if botUploadAudioRepo == nil {
		botUploadAudioRepo = gormrepo.NewGormBotUploadAudioRepository(db)
	}
	jar, _ := cookiejar.New(nil)
	s := &Service{
		db:  db,
		cfg: cfg,
		hub: hub,
		neteaseClient: &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
		players:              make(map[string]*BotPlayer),
		stopCh:               make(chan struct{}),
		botRepo:              botRepo,
		botPlayQueueRepo:     botPlayQueueRepo,
		botUploadAudioRepo:   botUploadAudioRepo,
		uploadDeleteAttempts: make(map[string]int),
	}
	s.loadNeteaseAuth()
	go s.ttsCleanupLoop()
	// Initialize Sherpa-ONNX TTS engine in background (non-blocking).
	// If model files are missing, the engine will be unavailable until
	// re-initialized via the admin API.
	go func() {
		modelDir := filepath.Join(filepath.Dir(cfg.LocalDataPath), "models", "tts", "vits-melo-tts-zh_en")
		if err := InitTTS(modelDir); err != nil {
			log.Printf("[tts] engine init failed (will retry via admin API): %v", err)
		}
	}()
	return s
}

// ttsCleanupLoop periodically cleans up expired TTS audio files
func (s *Service) ttsCleanupLoop() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	// Run once immediately on startup
	s.cleanupExpiredTTS()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanupExpiredTTS()
		}
	}
}

// Stop signals the TTS cleanup goroutine to exit
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		// N16：同步关停 TTS worker 子进程（stdin EOF → 优雅退出），防孤儿进程。
		// globalTTSWorker 为全局单例，Close 幂等，多 Service 实例（测试）安全。
		CloseTTS()
	})
}

// getBot fetches a bot by ID.
func (s *Service) getBot(botID string) (*model.Bot, error) {
	bot, err := s.botRepo.GetByID(context.Background(), botID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, errors.ErrInternal
	}
	return bot, nil
}

// requireSpaceMembership verifies that the user is a member of the space.
// OWNER and ADMIN global roles still need a membership record in this design.
func (s *Service) requireSpaceMembership(userID, spaceID string) error {
	if userID == "" || spaceID == "" {
		return errors.ErrForbidden
	}
	var count int64
	if err := s.db.Model(&model.Membership{}).Where("space_id = ? AND user_id = ?", spaceID, userID).Count(&count).Error; err != nil {
		return errors.ErrInternal
	}
	if count == 0 {
		return errors.ErrForbidden
	}
	return nil
}

// requireBotAccess verifies that the user is a member of the bot's space and
// that the bot exists. When userID is empty (bot-token auth), only existence is
// checked.
func (s *Service) requireBotAccess(userID string, bot *model.Bot) error {
	if bot == nil {
		return errors.ErrNotFound
	}
	if userID == "" {
		return nil
	}
	return s.requireSpaceMembership(userID, bot.SpaceID)
}

// AuthorizeBot is a public helper for handlers to verify that a user/bot token
// may operate on the given bot. It ensures the bot exists and, for user JWT
// requests, that the user is a member of the bot's space.
func (s *Service) AuthorizeBot(userID, botID string) error {
	bot, err := s.getBot(botID)
	if err != nil {
		return err
	}
	return s.requireBotAccess(userID, bot)
}

// FindBotByChannelID looks up a bot by its output room/channel ID.
// It returns the first matching bot, or ErrNotFound if none exists.
func (s *Service) FindBotByChannelID(channelID string) (*model.Bot, error) {
	if channelID == "" {
		return nil, errors.ErrNotFound
	}
	var bot model.Bot
	if err := s.db.Where("output_room_id = ?", channelID).First(&bot).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, errors.ErrInternal
	}
	return &bot, nil
}

// FindOrCreateBotForChannel 查找频道绑定的 bot，不存在则自动创建一个默认 bot。
// 这是音乐机器人在语音频道上"按需创建"的入口：前端通过 channelId 访问 bot
// 功能时，后端自动确保该频道存在对应的 bot 实体，避免用户手动创建。
// userID 为触发创建的用户（需为该频道所属 space 的成员）。
func (s *Service) FindOrCreateBotForChannel(channelID, userID string) (*model.Bot, error) {
	// 1. 先尝试查找已存在的 bot
	bot, err := s.FindBotByChannelID(channelID)
	if err == nil {
		return bot, nil
	}
	if err != errors.ErrNotFound {
		return nil, err
	}

	// 2. 查询频道信息获取 spaceID
	var channel model.Channel
	if err := s.db.Where("id = ?", channelID).First(&channel).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, errors.ErrInternal
	}

	// 3. 检查用户是否是 space 成员（权限校验）
	if err := s.requireSpaceMembership(userID, channel.SpaceID); err != nil {
		return nil, err
	}

	// 4. 再次检查（防止并发创建期间其他请求已创建了 bot）
	bot, err = s.FindBotByChannelID(channelID)
	if err == nil {
		return bot, nil
	}

	// 5. 自动创建默认 bot，绑定到该频道
	bot, err = s.CreateBotToken(channel.SpaceID, &channelID, "MusicBot", userID)
	if err != nil {
		return nil, err
	}
	log.Printf("[bot] auto-created bot for channel %s (space=%s, creator=%s)", channelID, channel.SpaceID, userID)
	return bot, nil
}

// cleanupExpiredTTS deletes TTS records and files older than 7 days
func (s *Service) cleanupExpiredTTS() {
	var expired []model.BotTTSMessage
	if err := s.db.Where("expires_at < ?", time.Now()).Find(&expired).Error; err != nil {
		return
	}
	for _, tts := range expired {
		if tts.VoiceDataPath != "" {
			os.Remove(tts.VoiceDataPath)
		}
		s.db.Delete(&tts)
	}
}

// loadNeteaseAuth restores Netease cookie from database
func (s *Service) loadNeteaseAuth() {
	var auth model.NeteaseAuth
	if err := s.db.Order("updated_at DESC").First(&auth).Error; err != nil {
		return
	}
	if auth.ExpiresAt.After(time.Now()) && auth.Cookie != "" {
		// Parse and set cookies in the jar
		endpoint := s.neteaseAPIEndpoint()
		if endpoint != "" {
			u, _ := url.Parse(endpoint)
			if u != nil {
				// Simple cookie parsing: split by semicolon and create cookies
				parts := strings.Split(auth.Cookie, ";")
				var cookies []*http.Cookie
				for _, part := range parts {
					part = strings.TrimSpace(part)
					if part == "" {
						continue
					}
					kv := strings.SplitN(part, "=", 2)
					if len(kv) == 2 {
						cookies = append(cookies, &http.Cookie{
							Name:  kv[0],
							Value: kv[1],
						})
					}
				}
				s.neteaseClient.Jar.SetCookies(u, cookies)
			}
		}
	}
}

// saveNeteaseAuth persists Netease cookie to database
func (s *Service) saveNeteaseAuth(cookie string) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint == "" {
		return
	}
	u, _ := url.Parse(endpoint)
	if u == nil {
		return
	}
	cookies := s.neteaseClient.Jar.Cookies(u)
	var cookieParts []string
	for _, c := range cookies {
		cookieParts = append(cookieParts, c.Name+"="+c.Value)
	}
	fullCookie := strings.Join(cookieParts, "; ")
	if cookie != "" {
		fullCookie = cookie
	}
	var auth model.NeteaseAuth
	if err := s.db.Order("updated_at DESC").First(&auth).Error; err == nil {
		auth.Cookie = fullCookie
		auth.ExpiresAt = time.Now().Add(30 * 24 * time.Hour)
		if err := s.db.Save(&auth).Error; err != nil {
			log.Printf("failed to save netease auth: %v", err)
		}
	} else {
		auth = model.NeteaseAuth{
			ID:        idgen.GenerateID(idgen.PrefixBot),
			UserID:    "system",
			Cookie:    fullCookie,
			ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		}
		if err := s.db.Create(&auth).Error; err != nil {
			log.Printf("failed to create netease auth: %v", err)
		}
	}
}

// getOrCreatePlayer returns the player for a bot, creating if necessary.
// The player is NOT connected to LiveKit yet — Connect is deferred to
// startPlayback, following TSMusicBot's "lazy connect" pattern.
func (s *Service) getOrCreatePlayer(botID string) *BotPlayer {
	s.playersMu.Lock()
	defer s.playersMu.Unlock()

	if p, ok := s.players[botID]; ok {
		return p
	}

	p := NewBotPlayer(s.db, s.cfg, botID, s.hub)
	p.SetOnComplete(func() {
		s.playNext(botID, p, false) // 自然播放结束，repeat-one 模式下重复当前曲目
	})
	// Auto-release from the service map when the player disconnects
	p.SetOnDisconnect(func(id string) {
		s.playersMu.Lock()
		delete(s.players, id)
		s.playersMu.Unlock()
	})
	// Restore saved state (playMode, volume) from DB
	var state model.BotPlayerState
	if err := s.db.Where("bot_id = ?", botID).First(&state).Error; err == nil {
		if state.PlayMode != "" {
			p.SetPlayMode(state.PlayMode)
		}
		if state.Volume >= 0 {
			p.SetVolume(state.Volume)
		}
	}
	// Not connecting here — Connect is called in startPlayback
	s.players[botID] = p
	return p
}

// releasePlayer disconnects and removes a player
func (s *Service) releasePlayer(botID string) {
	s.playersMu.Lock()
	defer s.playersMu.Unlock()

	if p, ok := s.players[botID]; ok {
		p.Disconnect()
		delete(s.players, botID)
	}
}

// GetStatus returns bot status including player state and queue
func (s *Service) GetStatus(botID string) (map[string]interface{}, error) {
	queue, err := s.botPlayQueueRepo.ListByBotID(context.Background(), botID)
	if err != nil {
		return nil, errors.ErrInternal
	}

	tracks := make([]map[string]interface{}, 0, len(queue))
	for _, q := range queue {
		tracks = append(tracks, map[string]interface{}{
			"id":       q.ID,
			"title":    q.Title,
			"artist":   q.Artist,
			"duration": q.Duration,
			"cover":    q.Cover,
			"album":    q.Album,
			"source":   q.Source,
		})
	}

	// Get player state
	s.playersMu.RLock()
	player, hasPlayer := s.players[botID]
	s.playersMu.RUnlock()

	status := map[string]interface{}{
		"queue": tracks,
	}

	// 阶段 3：Worker 模式下优先调用 Worker /status 获取实时状态。
	// 注意：startPlayback 在 Worker 模式下仍会通过 getOrCreatePlayer 创建
	// player_cgo 实例（用于 startMu 串行化和 fallback），因此 hasPlayer=true
	// 不代表实际在用 player_cgo 播放。必须先检查 Worker，避免读到空状态。
	if s.cfg.UseMusicBotWorker {
		wstatus, err := s.getWorkerStatus(botID)
		if err != nil {
			log.Printf("[bot] GetStatus getWorkerStatus FAILED botID=%s err=%v", botID, err)
			// Worker 不可用时回退：优先用 player_cgo（fallback 播放时）
			// 再回退到 DB 持久化状态
			if hasPlayer {
				ps := player.Status()
				status["playing"] = ps["playing"]
				status["paused"] = ps["paused"]
				status["currentTime"] = ps["currentTime"]
				status["volume"] = ps["volume"]
				status["playMode"] = ps["playMode"]
				status["currentTrack"] = ps["currentTrack"]
			} else {
				var state model.BotPlayerState
				if err := s.db.Where("bot_id = ?", botID).First(&state).Error; err == nil {
					status["playing"] = false
					status["paused"] = false
					status["currentTime"] = 0
					status["volume"] = state.Volume
					status["playMode"] = state.PlayMode
				} else {
					status["playing"] = false
					status["paused"] = false
					status["currentTime"] = 0
					status["volume"] = 80
					status["playMode"] = "order"
				}
				status["currentTrack"] = nil
			}
		} else {
			status["playing"] = wstatus["playing"]
			status["paused"] = wstatus["paused"]
			status["currentTime"] = wstatus["currentTime"]
			status["volume"] = wstatus["volume"]
			status["playMode"] = wstatus["playMode"]
			status["currentTrack"] = wstatus["currentTrack"]
		}
	} else if hasPlayer {
		// 非 Worker 模式：从 player_cgo 获取实时状态
		ps := player.Status()
		status["playing"] = ps["playing"]
		status["paused"] = ps["paused"]
		status["currentTime"] = ps["currentTime"]
		status["volume"] = ps["volume"]
		status["playMode"] = ps["playMode"]
		status["currentTrack"] = ps["currentTrack"]
	} else {
		// No active player means nothing is actually playing. Use DB only for
		// persisted volume/playMode; otherwise report stopped so the frontend
		// doesn't show a stale "playing" state after a server restart.
		var state model.BotPlayerState
		if err := s.db.Where("bot_id = ?", botID).First(&state).Error; err == nil {
			status["playing"] = false
			status["paused"] = false
			status["currentTime"] = 0
			status["volume"] = state.Volume
			status["playMode"] = state.PlayMode
		} else {
			status["playing"] = false
			status["paused"] = false
			status["currentTime"] = 0
			status["volume"] = 80
			status["playMode"] = "order"
		}
		status["currentTrack"] = nil
	}

	return status, nil
}

// AddToQueue adds a track to the queue
func (s *Service) AddToQueue(botID, trackID, title, artist string, duration int, cover, album, source, addedBy string, position *int) (*model.BotPlayQueue, error) {
	bot, err := s.getBot(botID)
	if err != nil {
		return nil, err
	}
	if err := s.requireBotAccess(addedBy, bot); err != nil {
		return nil, err
	}

	var insertPos int
	if position != nil {
		insertPos = *position
		// Shift existing items if inserting at head
		if insertPos == 0 {
			if err := s.db.Model(&model.BotPlayQueue{}).Where("bot_id = ?", botID).UpdateColumn("position", gorm.Expr("position + 1")).Error; err != nil {
				return nil, errors.ErrInternal
			}
		}
	} else {
		var maxPos int
		if err := s.db.Model(&model.BotPlayQueue{}).Where("bot_id = ?", botID).Select("COALESCE(MAX(position), -1)").Scan(&maxPos).Error; err != nil {
			return nil, errors.ErrInternal
		}
		insertPos = maxPos + 1
	}

	queue := &model.BotPlayQueue{
		ID:       idgen.GenerateID(idgen.PrefixBot),
		BotID:    botID,
		TrackID:  trackID,
		Title:    title,
		Artist:   artist,
		Duration: duration,
		Cover:    cover,
		Album:    album,
		Source:   source,
		Position: insertPos,
		AddedBy:  addedBy,
	}

	if err := s.db.Create(queue).Error; err != nil {
		return nil, errors.ErrInternal
	}
	s.broadcastQueueForBot(botID)

	// 阶段 4：Worker 模式下，队列已通过 broadcastQueueForBot → updateQueueWithWorker
	// 同步到 Worker。Worker 收到队列后自主决定是否播放（队列非空且当前未播放时自动播放第一首）。
	// 因此 Worker 模式下跳过 Go 端的自动播放逻辑，避免 Go 与 Worker 同时触发播放导致冲突。
	if !s.cfg.UseMusicBotWorker {
		// 非 Worker 模式：如果当前没有在播放，自动开始播放。
		// NJ-21：tryBeginAutoplay 保证接力期内（LiveKit 连接+取 URL 的数秒窗口）
		// 后续入队只追加队列、不再并发触发 playNext 顶掉第一首。
		s.playersMu.RLock()
		player, hasPlayer := s.players[botID]
		s.playersMu.RUnlock()
		if (!hasPlayer || !player.IsPlaying()) && s.tryBeginAutoplay(botID) {
			go func() {
				defer s.endAutoplay(botID)
				if queue, err := s.GetBotQueue(botID); err == nil && len(queue) > 0 {
					s.playNext(botID, nil, false) // 自动开始播放，无当前曲目
				}
				// NJ-21 补充：p.Play 异步起流，playNext 返回时 IsPlaying 未必
				// 已翻转。等待翻转（上限 10s）再放开接力标志，否则窗口期内
				// 第二次入队仍会再起一次 playNext 顶掉第一首。
				s.playersMu.RLock()
				pl := s.players[botID]
				s.playersMu.RUnlock()
				if pl != nil {
					for i := 0; i < 100; i++ {
						if pl.IsPlaying() {
							break
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
			}()
		}
	}

	return queue, nil
}

// tryBeginAutoplay marks the idle→playing handoff as in-flight for a bot.
// Returns false if another autoplay handoff is already running.
func (s *Service) tryBeginAutoplay(botID string) bool {
	s.autoplayMu.Lock()
	defer s.autoplayMu.Unlock()
	if s.autoplayInFlight == nil {
		s.autoplayInFlight = make(map[string]bool)
	}
	if s.autoplayInFlight[botID] {
		return false
	}
	s.autoplayInFlight[botID] = true
	return true
}

// endAutoplay clears the in-flight handoff flag after the autoplay goroutine
// settles (success, empty queue, or failure all included).
func (s *Service) endAutoplay(botID string) {
	s.autoplayMu.Lock()
	defer s.autoplayMu.Unlock()
	delete(s.autoplayInFlight, botID)
}

// broadcastQueueForBot fetches queue and broadcasts via WebSocket if player exists.
//
// 阶段 4：在 Worker 模式下，同时将最新队列同步到 Worker（调用 updateQueueWithWorker）。
// Worker 收到队列后更新内部缓存，保证自主 playNext 时能读取到正确的队列。
//
// 重要：Worker 模式下可能没有 BotPlayer 实例（播放由 Worker 处理），因此不能在
// 无 BotPlayer 时提前返回 — 否则队列永远不会同步到 Worker，Worker 无法自主播放。
func (s *Service) broadcastQueueForBot(botID string) {
	s.playersMu.RLock()
	p, ok := s.players[botID]
	s.playersMu.RUnlock()

	// 有 BotPlayer 时走 WebSocket 广播（非 Worker 模式或 Worker fallback 场景）
	if ok {
		if queue, err := s.botPlayQueueRepo.ListByBotID(context.Background(), botID); err == nil {
			p.broadcastQueue(queue)
		}
	}

	// 阶段 4：Worker 模式下同步队列到 Worker（无论是否有 BotPlayer）
	if s.cfg.UseMusicBotWorker {
		if err := s.updateQueueWithWorker(botID); err != nil {
			log.Printf("[bot] broadcastQueueForBot updateQueueWithWorker FAILED botID=%s err=%v", botID, err)
		}
	}

	// 队列变化后异步回收已播放或待删除、且不再被任何队列引用的上传文件。
	go s.sweepUnreferencedUploads()
}

// broadcastWorkerState pushes the latest Worker state to WebSocket subscribers.
// Called after successful Pause/Resume/Seek/SetVolume in Worker mode so the
// frontend receives an immediate bot_state_update event (instead of waiting
// for the next polling cycle).
//
// 在 Worker 模式下，BotPlayer 本身没有真实播放状态（playing/paused/currentTime
// 都是初始值），直接调用 p.broadcastState() 会推送错误的状态。因此这里先调用
// Worker /status 获取真实状态，再通过 p.BroadcastExternalState() 推送。
//
// 守卫（参考 Navidrome play_tracker.go:365-373 out-of-order 守卫）：
// 当 Worker 返回自相矛盾的状态（currentTrack=nil 但 paused=true）时，不广播。
// 这种状态在 Worker 端理论上不会再出现（Step 1 已修复 getStatus 在暂停态保留
// currentTrack），但作为防御性编程，Go 端仍需检查，避免错误状态污染前端。
//
// 参考：Navidrome "Ignoring out-of-order stopped report for different track"
// 不删除当前会话，直接拒绝广播错误状态。
func (s *Service) broadcastWorkerState(botID string) {
	if !s.cfg.UseMusicBotWorker {
		return
	}
	wstatus, err := s.getWorkerStatus(botID)
	if err != nil {
		log.Printf("[bot] broadcastWorkerState getWorkerStatus FAILED botID=%s err=%v", botID, err)
		return
	}

	// 守卫：自相矛盾状态检测（参考 Navidrome out-of-order 守卫）
	// paused=true 但 currentTrack=nil 表示 Worker 返回了不一致的状态
	// （可能是 Worker bug 或并发竞态），不应广播给前端，否则会让 UI 错误清空
	if paused, _ := wstatus["paused"].(bool); paused {
		if track, _ := wstatus["currentTrack"].(map[string]interface{}); track == nil {
			log.Printf("[bot] broadcastWorkerState IGNORED contradictory state (paused=true but currentTrack=nil) botID=%s", botID)
			return
		}
	}

	s.playersMu.RLock()
	p, ok := s.players[botID]
	s.playersMu.RUnlock()
	if !ok {
		// 没有 BotPlayer 实例（理论上 startPlayback 会创建），无法广播
		log.Printf("[bot] broadcastWorkerState no BotPlayer for botID=%s", botID)
		return
	}
	p.BroadcastExternalState(wstatus)
	log.Printf("[bot] broadcastWorkerState OK botID=%s playing=%v paused=%v", botID, wstatus["playing"], wstatus["paused"])
}

// RemoveFromQueue removes a track from the queue and reorders positions
func (s *Service) RemoveFromQueue(queueID string) error {
	var item model.BotPlayQueue
	if err := s.db.Where("id = ?", queueID).First(&item).Error; err != nil {
		return err
	}
	if err := s.db.Delete(&model.BotPlayQueue{}, "id = ?", queueID).Error; err != nil {
		return err
	}
	// Reorder remaining items: decrement position for all items after the removed one
	if err := s.db.Model(&model.BotPlayQueue{}).Where("bot_id = ? AND position > ?", item.BotID, item.Position).UpdateColumn("position", gorm.Expr("position - 1")).Error; err != nil {
		return err
	}
	s.broadcastQueueForBot(item.BotID)
	return nil
}

// ClearQueue clears the queue for a bot and stops playback.
// 清空队列时同时停止播放器，避免"队列空但仍在播放"的状态不一致问题。
func (s *Service) ClearQueue(botID string) error {
	if err := s.db.Where("bot_id = ?", botID).Delete(&model.BotPlayQueue{}).Error; err != nil {
		return err
	}
	// 停止播放器并清除当前曲目状态
	s.playersMu.RLock()
	p, ok := s.players[botID]
	s.playersMu.RUnlock()
	if ok {
		p.Stop()
	} else if s.cfg.UseMusicBotWorker {
		// 阶段 2：Worker 模式下没有 BotPlayer，调用 Worker /stop
		if err := s.stopPlaybackWithWorker(botID, false); err != nil {
			log.Printf("[bot] ClearQueue stopPlaybackWithWorker FAILED botID=%s err=%v", botID, err)
		}
	}
	s.broadcastQueueForBot(botID)
	return nil
}

// ReorderQueue reorders the queue
func (s *Service) ReorderQueue(botID string, queueIDs []string) error {
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range queueIDs {
			if err := tx.Model(&model.BotPlayQueue{}).Where("id = ? AND bot_id = ?", id, botID).Update("position", i).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.broadcastQueueForBot(botID)
	return nil
}

// SynthesizeTTS validates input, creates a TTS job record, and starts
// MeloTTS synthesis in a background goroutine. It returns the job ID
// immediately so the HTTP handler does not block on the slow TTS call.
//
// TTS 任务状态推送（参考 Replicate Prediction lifecycle + BullMQ Job）：
// - 入口：SetStatus(pending) + WS 推送 tts_state_update{status:pending}
// - goroutine 开始合成：SetStatus(processing, progress:30)
// - 合成成功 → ffmpeg 后处理：SetStatus(processing, progress:70)
// - 推流成功：SetStatus(succeeded, progress:100)
// - 任何阶段失败：SetStatus(failed, progress:100, error) + WS 推送 tts_state_update{status:failed,error}
//
// WS 推送为主通道，前端可通过 GET /api/v1/bots/tts/:jobId 兜底查询（处理 WS 断线）。
// 内存 job 状态 TTL 5 分钟自动清理（见 tts_job_manager.go cleanupLoop）。
func (s *Service) SynthesizeTTS(userID, username, botID, text, voice string, speed, pitch, volume float64) (string, error) {
	bot, err := s.getBot(botID)
	if err != nil {
		return "", err
	}
	if err := s.requireBotAccess(userID, bot); err != nil {
		return "", err
	}

	// Check consent
	var consent model.TTSConsent
	err = s.db.Where("user_id = ?", userID).First(&consent).Error
	if err != nil || !consent.Consented {
		return "", errors.New(errors.BOT_TTS_CONSENT_REQUIRED, "TTS consent required")
	}

	// Input validation to prevent command argument injection
	if len(text) == 0 {
		return "", errors.ErrBadRequest.WithDetails("text cannot be empty")
	}
	if len(text) > 2000 {
		return "", errors.ErrBadRequest.WithDetails("text too long (max 2000 characters)")
	}
	if strings.Contains(text, "\x00") {
		return "", errors.ErrBadRequest.WithDetails("text contains invalid characters")
	}
	if strings.HasPrefix(text, "-") {
		return "", errors.ErrBadRequest.WithDetails("text cannot start with '-'")
	}
	// Reject control characters that could interfere with downstream processing
	if strings.ContainsAny(text, "\r\n") {
		return "", errors.ErrBadRequest.WithDetails("text contains invalid control characters")
	}
	if voice != "" {
		if len(voice) > 128 {
			return "", errors.ErrBadRequest.WithDetails("voice parameter too long")
		}
		if strings.HasPrefix(voice, "-") || strings.ContainsAny(voice, ";|&$`\"\x00") {
			return "", errors.ErrBadRequest.WithDetails("invalid voice parameter")
		}
	}

	// 解析 channelID（用于 WS 推送 tts_state_update 事件）
	channelID := ""
	if bot.OutputRoomID != nil {
		channelID = *bot.OutputRoomID
	}

	// N8：voice 参数真实化——由引擎运行时探测说话人数，多说话人模型把
	// voice（speaker id 字符串）映射到 sid；单说话人模型（当前 MeloTTS
	// zh_en）忽略 voice 并记录明确日志。pitch/volume 由下方 ffmpeg 后处理实现。
	numSpeakers := int(TTSSpeakerCount())
	sid := resolveVoiceToSid(voice, numSpeakers)
	// Generate output file
	outputID := idgen.GenerateID(idgen.PrefixFile)
	if numSpeakers <= 1 && voice != "" {
		logIgnoredVoice(voice, outputID)
	}
	// MeloTTS outputs WAV; we convert to MP3 via ffmpeg post-processing
	outputWav := filepath.Join(s.cfg.LocalDataPath, "audio", "tts", outputID+".wav")
	outputMp3 := filepath.Join(s.cfg.LocalDataPath, "audio", "tts", outputID+".mp3")
	if err := os.MkdirAll(filepath.Dir(outputMp3), 0755); err != nil {
		return "", errors.ErrInternal
	}

	// Store pending TTS message so the job is tracked even if the process restarts.
	voiceName := "Sherpa-ONNX"
	if numSpeakers > 1 {
		voiceName = fmt.Sprintf("Sherpa-ONNX sid=%d", sid)
	}
	tts := &model.BotTTSMessage{
		ID:                 outputID,
		UserID:             userID,
		BotID:              botID,
		Text:               text,
		VoiceName:          voiceName,
		Speed:              speed,
		Pitch:              pitch,
		Volume:             volume,
		VoiceDataPath:      outputMp3,
		VoiceDataSensitive: true,
		ExpiresAt:          time.Now().Add(7 * 24 * time.Hour),
	}
	if err := s.db.Create(tts).Error; err != nil {
		return "", errors.ErrInternal.WithDetails("failed to create tts job: " + err.Error())
	}

	// 入口：标记 pending 并 WS 推送（让前端立刻显示"合成中..."）
	pendingJob := globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusPending, 0, nil)
	globalTTSJobManager.broadcastTTSState(s.hub, pendingJob)

	// Run TTS asynchronously so the HTTP request returns immediately.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[tts] panic during synthesis for job %s: %v", outputID, r)
				// panic：推送 failed 状态（参考 BullMQ Job.on('failed')）
				failedJob := globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusFailed, 100, classifyTTSError("panic", fmt.Errorf("%v", r)))
				globalTTSJobManager.broadcastTTSState(s.hub, failedJob)
			}
		}()

		// 阶段 1：Sherpa-ONNX 合成（标记 processing, progress 30）
		processingJob := globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusProcessing, 30, nil)
		globalTTSJobManager.broadcastTTSState(s.hub, processingJob)

		// Synthesize text to WAV using Sherpa-ONNX (Go/CGO, no Python dependency).
		// sid 来自 voice 参数映射（单说话人模型恒为 0，见 resolveVoiceToSid）。
		sampleRate, err := SynthesizeText(text, outputWav, speed, sid)
		if err != nil {
			log.Printf("[tts] Sherpa-ONNX synthesis failed for job %s: %v", outputID, err)
			// 合成失败：推送 failed 状态 + error（前端可据此显示重试按钮）
			failedJob := globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusFailed, 100, classifyTTSError("synthesize", err))
			globalTTSJobManager.broadcastTTSState(s.hub, failedJob)
			return
		}
		_ = sampleRate

		// 阶段 2：ffmpeg 后处理（更新 progress 70）
		processingJob = globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusProcessing, 70, nil)
		globalTTSJobManager.broadcastTTSState(s.hub, processingJob)

		// Post-process: WAV → MP3 with optional pitch/volume filters
		ffmpegArgs := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", outputWav}
		var afFilters []string
		if pitch != 0 {
			afFilters = append(afFilters, fmt.Sprintf("rubberband=pitch=%.4f", 1.0+pitch/100.0))
		}
		if volume != 0 {
			afFilters = append(afFilters, fmt.Sprintf("volume=%.1fdB", volume))
		}
		if len(afFilters) > 0 {
			ffmpegArgs = append(ffmpegArgs, "-af", strings.Join(afFilters, ","))
		}
		ffmpegArgs = append(ffmpegArgs, "-codec:a", "libmp3lame", "-q:a", "2", outputMp3)
		if err := exec.Command(ffmpegPath(s.cfg), ffmpegArgs...).Run(); err != nil {
			log.Printf("[tts] ffmpeg post-process failed for job %s: %v", outputID, err)
			_ = os.Remove(outputWav)
			// ffmpeg 失败：推送 failed 状态
			failedJob := globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusFailed, 100, classifyTTSError("ffmpeg", err))
			globalTTSJobManager.broadcastTTSState(s.hub, failedJob)
			return
		}

		// Remove intermediate WAV
		_ = os.Remove(outputWav)

		now := time.Now()
		tts.PlayedAt = &now
		if err := s.db.Save(tts).Error; err != nil {
			log.Printf("[tts] failed to update played_at for job %s: %v", outputID, err)
		}

		// 阶段 3：LiveKit 推流（更新 progress 90）
		processingJob = globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusProcessing, 90, nil)
		globalTTSJobManager.broadcastTTSState(s.hub, processingJob)

		if err := s.PlayTTS(botID, outputMp3, username); err != nil {
			log.Printf("[tts] PlayTTS failed for job %s: %v", outputID, err)
			// 推流失败：推送 failed 状态
			failedJob := globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusFailed, 100, classifyTTSError("livekit", err))
			globalTTSJobManager.broadcastTTSState(s.hub, failedJob)
			return
		}

		// 推流成功：标记 succeeded（前端关闭 loading）
		succeededJob := globalTTSJobManager.SetStatus(outputID, channelID, userID, TTSStatusSucceeded, 100, nil)
		globalTTSJobManager.broadcastTTSState(s.hub, succeededJob)
	}()

	return outputID, nil
}

// GetTTSJobStatus 查询 TTS 任务状态（兜底查询用，供 handler 调用）
//
// 用于 WS 断线重连后前端补偿查询 pending TTS 任务的状态。
// 返回 nil 表示 job 不存在（已过期 5 分钟 TTL 或从未创建）。
func (s *Service) GetTTSJobStatus(jobID string) *TTSJob {
	return globalTTSJobManager.GetStatus(jobID)
}

// RecordTTSConsent records that a user has consented to TTS data processing.
func (s *Service) RecordTTSConsent(userID string) error {
	var consent model.TTSConsent
	err := s.db.Where("user_id = ?", userID).First(&consent).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			now := time.Now()
			consent = model.TTSConsent{
				ID:          idgen.GenerateID(idgen.PrefixBot),
				UserID:      userID,
				Consented:   true,
				ConsentedAt: &now,
			}
			return s.db.Create(&consent).Error
		}
		return err
	}
	consent.Consented = true
	now := time.Now()
	consent.ConsentedAt = &now
	return s.db.Save(&consent).Error
}

// GetTTSConsent 查询用户是否已同意 TTS 数据使用。
//
// 设计：以后端 TTSConsent 表为权威来源（参考 Mattermost Preferences 模式），
// 取代前端 localStorage 读取，避免共享设备的跨用户持久化问题。
// 查询失败默认返回 false（保守策略：未明确同意即视为未同意）。
func (s *Service) GetTTSConsent(userID string) (bool, error) {
	var consent model.TTSConsent
	err := s.db.Where("user_id = ?", userID).First(&consent).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	return consent.Consented, nil
}

// RevokeTTSConsent 撤回用户的 TTS 数据使用同意。
//
// 设计：与 RecordTTSConsent 对称，写入 consented=false 与 revoked_at 时间戳，
// 审计可追溯。记录不存在时为 no-op（无需创建一条"未同意"的记录）。
func (s *Service) RevokeTTSConsent(userID string) error {
	var consent model.TTSConsent
	err := s.db.Where("user_id = ?", userID).First(&consent).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil // 记录不存在视为已撤回，no-op
		}
		return err
	}
	consent.Consented = false
	now := time.Now()
	consent.RevokedAt = &now
	return s.db.Save(&consent).Error
}

// GetNeteaseSearch proxies search to NeteaseCloudMusicApi
// 搜索接口不返回封面 URL，需批量调用 /song/detail 补充 picUrl 字段
func (s *Service) GetNeteaseSearch(keyword string, limit int) (map[string]interface{}, error) {
	if keyword == "" {
		return map[string]interface{}{"songs": []interface{}{}}, nil
	}
	result, err := s.NeteaseProxy("/search?keywords=" + url.QueryEscape(keyword) + "&limit=" + fmt.Sprintf("%d", limit))
	if err != nil {
		return nil, err
	}

	// 批量获取封面：从搜索结果提取 song ID，调用 /song/detail 获取 picUrl
	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		return result, nil
	}
	songs, ok := resultMap["songs"].([]interface{})
	if !ok || len(songs) == 0 {
		return result, nil
	}

	// 收集所有 song ID
	ids := make([]string, 0, len(songs))
	for _, song := range songs {
		songMap, ok := song.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := songMap["id"]; ok {
			ids = append(ids, fmt.Sprintf("%v", id))
		}
	}

	if len(ids) == 0 {
		return result, nil
	}

	// 批量调用 /song/detail 获取封面
	idStr := strings.Join(ids, ",")
	detail, err := s.NeteaseProxy("/song/detail?ids=" + url.QueryEscape(idStr))
	if err != nil {
		log.Printf("[netease-search] song/detail failed: %v", err)
		return result, nil // 封面获取失败不影响搜索结果
	}

	// 构建 song ID → picUrl 映射
	coverMap := make(map[string]string)
	detailSongs, ok := detail["songs"].([]interface{})
	if !ok {
		log.Printf("[netease-search] song/detail response has no songs array, keys=%v", detail)
		return result, nil
	}
	for _, ds := range detailSongs {
		dm, ok := ds.(map[string]interface{})
		if !ok {
			continue
		}
		songID := fmt.Sprintf("%v", dm["id"])
		if al, ok := dm["al"].(map[string]interface{}); ok {
			if picUrl, ok := al["picUrl"].(string); ok && picUrl != "" {
				coverMap[songID] = picUrl
			}
		}
	}
	log.Printf("[netease-search] coverMap: %d covers for %d songs", len(coverMap), len(ids))

	// 将封面 URL 回填到搜索结果
	for _, song := range songs {
		songMap, ok := song.(map[string]interface{})
		if !ok {
			continue
		}
		songID := fmt.Sprintf("%v", songMap["id"])
		if cover, exists := coverMap[songID]; exists {
			// 在 album 对象中补充 picUrl
			if album, ok := songMap["album"].(map[string]interface{}); ok {
				album["picUrl"] = cover
			}
			if al, ok := songMap["al"].(map[string]interface{}); ok {
				al["picUrl"] = cover
			}
		}
	}

	return result, nil
}

// NeteaseHealthCheck verifies that the configured NeteaseCloudMusicApi endpoint is reachable.
// It returns a descriptive error for logging; it does not block other bot functionality.
func (s *Service) NeteaseHealthCheck() error {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint == "" {
		return fmt.Errorf("RRT_NETEASE_API_ENDPOINT not configured")
	}
	fullURL := endpoint + "/"
	resp, err := s.neteaseClient.Get(fullURL)
	if err != nil {
		return fmt.Errorf("netease api unreachable at %s: %w", fullURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("netease api returned status %d at %s", resp.StatusCode, fullURL)
	}
	return nil
}

// GetNeteaseURL gets song URL from Netease
func (s *Service) GetNeteaseURL(songID string) (string, error) {
	result, err := s.NeteaseProxy("/song/url?id=" + url.QueryEscape(songID))
	if err != nil {
		return "", err
	}
	data, ok := result["data"].([]interface{})
	if !ok || len(data) == 0 {
		return "", errors.New(errors.BOT_NOT_FOUND, "song URL not found")
	}
	first, ok := data[0].(map[string]interface{})
	if !ok {
		return "", errors.New(errors.BOT_NOT_FOUND, "song URL not found")
	}
	// NJ-32：未登录会话播放 VIP/版权曲目时，网易云返回 30s 试听片段
	// （freeTrialInfo = {start,end}）。照播会造成「歌刚响起 30 秒就从头
	// 重来」的体验事故（实测 2026-10-04）。显式拒绝，走播放失败→跳过路径，
	// 并提示扫码登录后可播放完整版。
	if first["freeTrialInfo"] != nil {
		return "", errors.New(errors.BOT_NETEASE_API_ERROR, "该歌曲受版权限制（仅提供试听片段），扫码登录网易云账号后可播放完整版")
	}
	urlStr, ok := first["url"].(string)
	if !ok || urlStr == "" {
		return "", errors.New(errors.BOT_NOT_FOUND, "song URL not available")
	}
	return urlStr, nil
}

// neteaseAPIEndpoint returns the configured NeteaseCloudMusicApi endpoint
func (s *Service) neteaseAPIEndpoint() string {
	if v := os.Getenv("RRT_NETEASE_API_ENDPOINT"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if s.cfg.NeteaseAPIEndpoint != "" {
		return strings.TrimRight(s.cfg.NeteaseAPIEndpoint, "/")
	}
	return ""
}

// neteaseEnvCookie returns the operator-provided login cookie from
// RRT_NETEASE_COOKIE (FIX-20261003-01 N17): either a bare MUSIC_U token value
// (wrapped into "MUSIC_U=<value>") or a full cookie string, passed through
// verbatim otherwise. Empty env → "" (nothing injected). This is the ops-side
// fallback for anonymous-request risk control (-462): a valid logged-in cookie
// makes the upstream challenge disappear; it coexists with the QR-login jar
// (DB-restored cookies) — prefer configuring only one of the two.
func neteaseEnvCookie() string {
	v := strings.TrimSpace(os.Getenv("RRT_NETEASE_COOKIE"))
	if v == "" {
		return ""
	}
	if !strings.Contains(v, "=") {
		return "MUSIC_U=" + v
	}
	return v
}

// NeteaseProxy performs a GET request to the Netease API and returns parsed JSON
func (s *Service) NeteaseProxy(path string) (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint == "" {
		return nil, errors.New(errors.BOT_NETEASE_NOT_CONFIGURED, "Netease Cloud Music API endpoint not configured")
	}
	fullURL := endpoint + path
	log.Printf("[netease-proxy] GET %s", fullURL)
	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to build netease request: " + err.Error())
	}
	// N17：运维注入的登录 cookie（RRT_NETEASE_COOKIE）。显式 Cookie 头与
	// neteaseClient.Jar 自动附加共存（Go 会把 jar cookie 追加到已有 Cookie 头）。
	if ck := neteaseEnvCookie(); ck != "" {
		req.Header.Set("Cookie", ck)
	}

	var lastErr error
	var resp *http.Response
	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		resp, err = s.neteaseClient.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[netease-proxy] request error (attempt %d): %v", attempt+1, err)
			continue
		}
		log.Printf("[netease-proxy] response status: %d (attempt %d)", resp.StatusCode, attempt+1)
		if resp.StatusCode >= 500 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("netease API returned status %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
			log.Printf("[netease-proxy] server error (attempt %d): %v", attempt+1, lastErr)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			log.Printf("[netease-proxy] upstream client error: status=%d", resp.StatusCode)
			return nil, neteaseUpstreamError(resp.StatusCode, body)
		}
		defer resp.Body.Close()
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			log.Printf("[netease-proxy] JSON decode error: %v", err)
			return nil, errors.ErrInternal.WithDetails("invalid netease api response: " + err.Error())
		}
		if err := neteaseResultError(result); err != nil {
			return nil, err
		}
		return result, nil
	}
	log.Printf("[netease-proxy] request failed after retries: %v", lastErr)
	if lastErr != nil {
		// Network-level failures (timeout, connection refused, ECONNRESET, etc.)
		if _, ok := lastErr.(net.Error); ok {
			return nil, errors.New(errors.BOT_NETEASE_UNREACHABLE, "music service is unreachable")
		}
		// 5xx errors are wrapped as fmt.Errorf by the loop above.
		if strings.Contains(lastErr.Error(), "netease API returned status") {
			return nil, errors.New(errors.BOT_NETEASE_API_ERROR, "music service temporarily unavailable")
		}
	}
	return nil, errors.New(errors.BOT_NETEASE_API_ERROR, "music service temporarily unavailable")
}

// NeteaseProxyPost sends a POST request to the Netease API with form data
func (s *Service) NeteaseProxyPost(endpoint string, params map[string]string) (map[string]interface{}, error) {
	apiEndpoint := s.neteaseAPIEndpoint()
	if apiEndpoint == "" {
		return nil, errors.New(errors.BOT_NETEASE_NOT_CONFIGURED, "Netease Cloud Music API endpoint not configured")
	}
	data := url.Values{}
	for k, v := range params {
		data.Set(k, v)
	}

	var lastErr error
	var resp *http.Response
	var err error
	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		// N17：与 NeteaseProxy 同口径注入运维登录 cookie（PostForm 无法预置头，
		// 这里改用手动构造请求以携带 Cookie 头）
		bodyStr := data.Encode()
		req, reqErr := http.NewRequest("POST", apiEndpoint+endpoint, strings.NewReader(bodyStr))
		if reqErr != nil {
			return nil, errors.ErrInternal.WithDetails("failed to build netease request: " + reqErr.Error())
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if ck := neteaseEnvCookie(); ck != "" {
			req.Header.Set("Cookie", ck)
		}
		resp, err = s.neteaseClient.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[netease-proxy-post] request error (attempt %d): %v", attempt+1, err)
			continue
		}
		if resp.StatusCode >= 500 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("netease API returned status %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
			log.Printf("[netease-proxy-post] server error (attempt %d): %v", attempt+1, lastErr)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			log.Printf("[netease-proxy-post] upstream client error: status=%d", resp.StatusCode)
			return nil, neteaseUpstreamError(resp.StatusCode, body)
		}
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, errors.ErrInternal.WithDetails("invalid netease api response")
		}
		if err := neteaseResultError(result); err != nil {
			return nil, err
		}
		return result, nil
	}
	log.Printf("[netease-proxy-post] request failed after retries: %v", lastErr)
	return nil, errors.New(errors.BOT_NETEASE_API_ERROR, "music service temporarily unavailable")
}

// GetNeteaseQRKey generates a unique key for QR login
func (s *Service) GetNeteaseQRKey() (string, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint != "" {
		result, err := s.NeteaseProxy("/login/qr/key")
		if err != nil {
			return "", err
		}
		if data, ok := result["data"].(map[string]interface{}); ok {
			if key, ok := data["unikey"].(string); ok && key != "" {
				return key, nil
			}
		}
	}
	// Fallback: generate a local mock key
	b := make([]byte, 16)
	cryptorand.Read(b)
	return hex.EncodeToString(b), nil
}

// GetNeteaseQRCreate creates a QR code for the given key
func (s *Service) GetNeteaseQRCreate(key string, qrimg bool) (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint != "" {
		result, err := s.NeteaseProxy("/login/qr/create?key=" + key + "&qrimg=true")
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	// Fallback: return a placeholder QR image with instructions
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="150" height="150"><rect width="150" height="150" fill="#f5f5f5"/><rect x="10" y="10" width="130" height="130" fill="none" stroke="#ccc" stroke-width="2"/><text x="75" y="70" font-size="11" text-anchor="middle" fill="#666">Configure Netease API</text><text x="75" y="90" font-size="9" text-anchor="middle" fill="#999">RRT_NETEASE_API_ENDPOINT</text></svg>`
	placeholderQR := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
	return map[string]interface{}{
		"qrurl": "https://music.163.com/login?code=" + key,
		"qrimg": placeholderQR,
	}, nil
}

// GetNeteaseQRCheck checks the QR scan status
func (s *Service) GetNeteaseQRCheck(key string) (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint != "" {
		// timestamp prevents CDN caching
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		result, err := s.NeteaseProxy("/login/qr/check?key=" + key + "&timestamp=" + ts)
		if err != nil {
			return nil, err
		}
		// 登录成功(code=803)时，持久化 cookie
		if code, ok := result["code"].(float64); ok && code == 803 {
			if endpoint := s.neteaseAPIEndpoint(); endpoint != "" {
				if u, err := url.Parse(endpoint); err == nil {
					cookies := s.neteaseClient.Jar.Cookies(u)
					cookieStr := ""
					for _, c := range cookies {
						cookieStr += c.Name + "=" + c.Value + "; "
					}
					s.saveNeteaseAuth(strings.TrimRight(cookieStr, "; "))
				}
			}
		}
		return result, nil
	}
	// Fallback: simulate expired QR code
	return map[string]interface{}{
		"code":    800,
		"message": "二维码已过期",
	}, nil
}

// GetNeteaseLoginStatus gets the current login status
func (s *Service) GetNeteaseLoginStatus() (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint != "" {
		result, err := s.NeteaseProxy("/login/status")
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	// Fallback: not logged in
	return map[string]interface{}{
		"profile": nil,
	}, nil
}

// GetNeteaseUserAccount gets user account information
func (s *Service) GetNeteaseUserAccount() (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint != "" {
		result, err := s.NeteaseProxy("/user/account")
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	// Fallback: not logged in
	return map[string]interface{}{
		"account": nil,
		"profile": nil,
	}, nil
}

// GetNeteaseRecommend gets daily recommended songs
func (s *Service) GetNeteaseRecommend() (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint != "" {
		result, err := s.NeteaseProxy("/recommend/songs")
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	// Fallback: API not configured
	return map[string]interface{}{
		"data": map[string]interface{}{
			"dailySongs": []interface{}{},
		},
	}, nil
}

// GetNeteasePlaylists gets user's playlists by UID
func (s *Service) GetNeteasePlaylists(uid string) (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint != "" {
		path := "/user/playlist"
		if uid != "" {
			path += "?uid=" + uid
		}
		result, err := s.NeteaseProxy(path)
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	// Fallback: API not configured
	return map[string]interface{}{
		"playlist": []interface{}{},
	}, nil
}

// GetBotQueue returns the play queue for a bot
func (s *Service) GetBotQueue(botID string) ([]map[string]interface{}, error) {
	queue, err := s.botPlayQueueRepo.ListByBotID(context.Background(), botID)
	if err != nil {
		return nil, errors.ErrInternal
	}
	tracks := make([]map[string]interface{}, 0, len(queue))
	for _, q := range queue {
		tracks = append(tracks, map[string]interface{}{
			"id":       q.ID,
			"trackId":  q.TrackID,
			"title":    q.Title,
			"artist":   q.Artist,
			"duration": q.Duration,
			"cover":    q.Cover,
			"album":    q.Album,
			"source":   q.Source,
		})
	}
	return tracks, nil
}

// allowedAudioTypes defines permitted audio MIME types for bot uploads
var allowedAudioTypes = map[string]bool{
	"audio/mpeg": true,
	"audio/mp3":  true,
	"audio/wav":  true,
	"audio/ogg":  true,
	"audio/opus": true,
	"audio/flac": true,
}

// isValidAudioMagic checks the file header against known audio magic numbers
func isValidAudioMagic(header []byte) bool {
	if len(header) < 4 {
		return false
	}
	// MP3: ID3 tag or MPEG sync word (0xFFE0)
	if string(header[:3]) == "ID3" || (header[0] == 0xFF && (header[1]&0xE0) == 0xE0) {
		return true
	}
	// WAV: RIFF....WAVE
	if string(header[:4]) == "RIFF" && len(header) >= 12 && string(header[8:12]) == "WAVE" {
		return true
	}
	// OGG / OPUS: OggS
	if string(header[:4]) == "OggS" {
		return true
	}
	// FLAC: fLaC
	if string(header[:4]) == "fLaC" {
		return true
	}
	return false
}

// audioDuration uses ffprobe to get the duration of an audio file in seconds.
// If ffprobe is unavailable or fails, it returns 0 without error so uploads
// still succeed and the UI can fall back to 0:00.
func audioDuration(filePath string) int {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[bots] ffprobe failed for %s: %v", filePath, err)
		return 0
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return int(secs)
}

// UploadAudio saves an uploaded audio file
func (s *Service) UploadAudio(userID, filename, mimeType string, fileSize int64, reader io.Reader) (*model.BotUploadAudio, error) {
	if fileSize > 50*1024*1024 {
		return nil, errors.New(errors.FILE_TOO_LARGE, "audio file too large (max 50MB)")
	}
	if !allowedAudioTypes[mimeType] {
		return nil, errors.New(errors.FILE_INVALID_TYPE, "invalid audio format")
	}

	// Verify magic number (first 512 bytes)
	header := make([]byte, 512)
	n, err := io.ReadFull(reader, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, errors.ErrInternal
	}
	if !isValidAudioMagic(header[:n]) {
		return nil, errors.New(errors.FILE_INVALID_TYPE, "invalid audio file content")
	}
	reader = io.MultiReader(bytes.NewReader(header[:n]), reader)

	uploadDir := filepath.Join(s.cfg.LocalDataPath, "uploads/audio")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, errors.ErrInternal
	}
	fileID := idgen.GenerateID(idgen.PrefixFile)
	filePath := filepath.Join(uploadDir, fileID+filepath.Ext(filename))
	filePath = filepath.Clean(filePath)
	// Ensure path stays within uploadDir
	absDir, _ := filepath.Abs(uploadDir)
	absPath, _ := filepath.Abs(filePath)
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
		return nil, errors.ErrForbidden
	}

	out, err := os.Create(filePath)
	if err != nil {
		return nil, errors.ErrInternal
	}
	defer out.Close()

	written, err := io.Copy(out, reader)
	if err != nil {
		os.Remove(filePath)
		return nil, errors.ErrInternal
	}

	// Read audio duration so the player can show total time and seek correctly.
	duration := audioDuration(filePath)

	// Store path relative to LocalDataPath so playback can resolve it
	// consistently regardless of LocalDataPath prefix.
	relPath, _ := filepath.Rel(s.cfg.LocalDataPath, filePath)
	if relPath == "" || strings.HasPrefix(relPath, "..") {
		relPath = filePath
	}
	upload := &model.BotUploadAudio{
		ID:        fileID,
		UserID:    userID,
		Title:     filename,
		FilePath:  relPath,
		FileSize:  written,
		MimeType:  mimeType,
		Duration:  duration,
		CreatedAt: time.Now(),
	}
	if err := s.botUploadAudioRepo.Create(context.Background(), upload); err != nil {
		os.Remove(filePath)
		return nil, errors.ErrInternal
	}
	s.enforceUploadQuota(upload.ID)
	return upload, nil
}

// UploadWithUploader 包含上传记录和上传者用户名，用于空间内共享列表展示。
type UploadWithUploader struct {
	model.BotUploadAudio
	UploaderUsername string `json:"uploaderUsername"`
}

// GetUploads returns all uploaded audio files in the space, with uploader username.
// 修改：从仅返回调用者自己的上传改为返回空间内全部上传，供所有用户共享音乐列表。
// userID 参数保留用于未来按需过滤，当前实现未使用。
func (s *Service) GetUploads(userID string) ([]UploadWithUploader, error) {
	uploads, err := s.botUploadAudioRepo.ListAll(context.Background())
	if err != nil {
		return nil, errors.ErrInternal
	}
	if len(uploads) == 0 {
		return []UploadWithUploader{}, nil
	}

	// 收集所有上传者 ID，批量查询用户名，避免 N+1 查询
	uidSet := make(map[string]struct{}, len(uploads))
	for _, u := range uploads {
		uidSet[u.UserID] = struct{}{}
	}
	uids := make([]string, 0, len(uidSet))
	for uid := range uidSet {
		uids = append(uids, uid)
	}

	var users []model.User
	if err := s.db.Select("id, display_name, username").Where("id IN ?", uids).Find(&users).Error; err != nil {
		// 用户名查询失败不应阻塞列表返回，降级为空用户名
		users = nil
	}
	uidToName := make(map[string]string, len(users))
	for _, u := range users {
		uidToName[u.ID] = u.DisplayName
		if uidToName[u.ID] == "" {
			uidToName[u.ID] = u.Username
		}
	}

	result := make([]UploadWithUploader, 0, len(uploads))
	for _, u := range uploads {
		if u.PendingDelete {
			continue
		}
		result = append(result, UploadWithUploader{
			BotUploadAudio:   u,
			UploaderUsername: uidToName[u.UserID],
		})
	}
	return result, nil
}

// DeleteUpload removes an uploaded audio file
//
// 共享列表语义：GetUploads 返回空间内全部上传（不区分 user_id），
// 因此 DeleteUpload 也只按 id 查询，允许空间内任何成员删除任意上传。
// userID 参数保留以兼容现有 handler 签名，当前未使用。
func (s *Service) DeleteUpload(userID, id string) error {
	var upload model.BotUploadAudio
	if err := s.db.Where("id = ? AND pending_delete = ?", id, false).First(&upload).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.ErrNotFound
		}
		return errors.ErrInternal
	}
	references, err := s.uploadQueueReferenceCount(upload.ID)
	if err != nil {
		return errors.ErrInternal
	}
	if references > 0 {
		if err := s.db.Model(&upload).Update("pending_delete", true).Error; err != nil {
			return errors.ErrInternal
		}
		return nil
	}
	return s.deleteUploadFileAndRecord(&upload)
}

// ===== Playback Control =====

// Skip skips to the next track in the queue
func (s *Service) Skip(botID string) error {
	// 阶段 4：Worker 模式下转发到 Worker /skip（Worker 自主管理队列与播放历史）
	if s.cfg.UseMusicBotWorker {
		if err := s.skipWithWorker(botID, true); err != nil {
			log.Printf("[bot] Skip Worker FAILED botID=%s err=%v, falling back to local playNext", botID, err)
			// 落到下面的 Go 端逻辑
		} else {
			log.Printf("[bot] Skip Worker OK botID=%s", botID)
			s.broadcastWorkerState(botID)
			return nil
		}
	}
	p := s.getOrCreatePlayer(botID)
	// 不在这里删除当前曲目，由 playNext 统一处理删除逻辑。
	// 原因：在 repeat-all 模式下如果队列只有 1 首歌，先删除会导致队列空，
	// playNext 找不到下一首但又不停止播放器，造成"队列空但仍在播放"的状态不一致。
	// isUserSkip=true：用户主动点击下一首，repeat-one 模式下也切换到下一首。
	return s.playNext(botID, p, true)
}

// PlayPrevious plays the previous track from history
func (s *Service) PlayPrevious(botID string) error {
	// 阶段 4：Worker 模式下转发到 Worker /previous（Worker 维护播放历史）
	if s.cfg.UseMusicBotWorker {
		if err := s.previousWithWorker(botID); err != nil {
			log.Printf("[bot] Previous Worker FAILED botID=%s err=%v, falling back to local player", botID, err)
		} else {
			log.Printf("[bot] Previous Worker OK botID=%s", botID)
			s.broadcastWorkerState(botID)
			return nil
		}
	}
	p := s.getOrCreatePlayer(botID)
	prev := p.PreviousTrack()
	if prev == nil {
		return nil
	}
	return s.startPlayback(botID, prev)
}

// Pause pauses playback
//
// 阶段 3：当 UseMusicBotWorker=true 时，优先调用 Worker /pause 接口；
// Worker 调用失败时回退到 Go 端 player_cgo 的 Pause（保证可用性）。
//
// Step 6 修复：根据 Worker 返回的 Noop 信号决定是否广播状态。
//   - Noop=true（重复暂停/未在播放/会话不存在）：跳过 broadcastWorkerState，避免前端
//     收到错误的"暂停态 currentTrack=null"导致 UI 清空（参考 Jellyfin PausedGroupState
//     prevState.Equals(Type) no-op 处理）
//   - Noop=false（成功暂停）：正常调用 broadcastWorkerState 推送状态变更
func (s *Service) Pause(botID string) error {
	if s.cfg.UseMusicBotWorker {
		result, err := s.pauseWithWorker(botID)
		if err != nil {
			log.Printf("[bot] Pause Worker FAILED botID=%s err=%v, falling back to local player", botID, err)
			// 落到下面的 Go 端逻辑
		} else if result != nil && result.Noop {
			// no-op：跳过广播，但仍视为成功（幂等调用）
			log.Printf("[bot] Pause Worker NO-OP botID=%s reason=%s (skip broadcast)", botID, result.Reason)
			return nil
		} else {
			log.Printf("[bot] Pause Worker OK botID=%s", botID)
			// 推送最新状态到前端（解决前端 botPlaying 不更新问题）
			s.broadcastWorkerState(botID)
			return nil
		}
	}
	s.playersMu.RLock()
	p, ok := s.players[botID]
	s.playersMu.RUnlock()
	if ok {
		p.Pause()
	}
	return nil
}

// Resume resumes playback
//
// 阶段 3：当 UseMusicBotWorker=true 时，优先调用 Worker /resume 接口；
// Worker 调用失败时回退到 Go 端 player_cgo 的 Resume。
func (s *Service) Resume(botID string) error {
	if s.cfg.UseMusicBotWorker {
		if err := s.resumeWithWorker(botID); err != nil {
			log.Printf("[bot] Resume Worker FAILED botID=%s err=%v, falling back to local player", botID, err)
		} else {
			log.Printf("[bot] Resume Worker OK botID=%s", botID)
			// 推送最新状态到前端（解决前端 botPlaying 不更新问题）
			s.broadcastWorkerState(botID)
			return nil
		}
	}
	s.playersMu.RLock()
	p, ok := s.players[botID]
	s.playersMu.RUnlock()
	if ok {
		p.Resume()
	}
	return nil
}

// Seek seeks to a specific time in the current track
//
// 阶段 3：当 UseMusicBotWorker=true 时，优先调用 Worker /seek 接口；
// Worker 调用失败时回退到 Go 端 player_cgo 的 Seek。
func (s *Service) Seek(botID string, seconds int) error {
	if s.cfg.UseMusicBotWorker {
		if err := s.seekWithWorker(botID, seconds); err != nil {
			log.Printf("[bot] Seek Worker FAILED botID=%s seconds=%d err=%v, falling back to local player", botID, seconds, err)
		} else {
			log.Printf("[bot] Seek Worker OK botID=%s seconds=%d", botID, seconds)
			// 推送最新状态到前端（解决前端 currentTime 不更新问题）
			s.broadcastWorkerState(botID)
			return nil
		}
	}
	p := s.getOrCreatePlayer(botID)
	return p.Seek(seconds)
}

// PlayTTS plays a TTS audio file through the bot.
// M18: username is used to synthesize a "[username] 说" prefix audio cue.
func (s *Service) PlayTTS(botID, audioPath, username string) error {
	p := s.getOrCreatePlayer(botID)
	return p.PlayTTS(audioPath, username)
}

// SetPlayMode sets the bot play mode
//
// 阶段 4：当 UseMusicBotWorker=true 时，优先调用 Worker /play-mode 接口。
// Worker 会根据模式变更重新生成 shuffle 顺序或清空 shuffle 状态。
func (s *Service) SetPlayMode(botID string, mode string) error {
	// 先持久化到 DB
	var state model.BotPlayerState
	s.db.Where("bot_id = ?", botID).FirstOrCreate(&state, model.BotPlayerState{BotID: botID})
	state.PlayMode = mode
	state.UpdatedAt = time.Now()
	s.db.Save(&state)

	if s.cfg.UseMusicBotWorker {
		if err := s.setPlayModeWithWorker(botID, mode); err != nil {
			log.Printf("[bot] SetPlayMode Worker FAILED botID=%s mode=%s err=%v, falling back to local player", botID, mode, err)
		} else {
			log.Printf("[bot] SetPlayMode Worker OK botID=%s mode=%s", botID, mode)
			s.broadcastWorkerState(botID)
			return nil
		}
	}

	p := s.getOrCreatePlayer(botID)
	p.SetPlayMode(mode)
	return nil
}

// SetVolume sets bot volume (0-100)
//
// 阶段 3：当 UseMusicBotWorker=true 时，优先调用 Worker /volume 接口；
// Worker 调用失败时回退到 Go 端 player_cgo 的 SetVolume。
//
// 注意：DB 中的 BotPlayerState.Volume 始终更新（无论是否使用 Worker），
// 保证 Worker 重启或回退到 Go 端时音量一致。
func (s *Service) SetVolume(botID string, volume int) error {
	// 先持久化到 DB（保证下次启动时音量一致）
	var state model.BotPlayerState
	s.db.Where("bot_id = ?", botID).FirstOrCreate(&state, model.BotPlayerState{BotID: botID})
	state.Volume = volume
	state.UpdatedAt = time.Now()
	s.db.Save(&state)

	if s.cfg.UseMusicBotWorker {
		if err := s.setVolumeWithWorker(botID, volume); err != nil {
			log.Printf("[bot] SetVolume Worker FAILED botID=%s volume=%d err=%v, falling back to local player", botID, volume, err)
			// 落到下面的 Go 端逻辑
		} else {
			log.Printf("[bot] SetVolume Worker OK botID=%s volume=%d", botID, volume)
			// 推送最新状态到前端（解决前端 volume 不更新问题）
			s.broadcastWorkerState(botID)
			return nil
		}
	}

	s.playersMu.RLock()
	p, ok := s.players[botID]
	s.playersMu.RUnlock()
	if ok {
		p.SetVolume(volume)
	}
	return nil
}

// PlayNow starts playing a specific track from the queue.
// If the track cannot be played (e.g. copyright restriction), it is removed
// from the queue and the next track is tried automatically.
func (s *Service) PlayNow(botID, queueID string) error {
	var track model.BotPlayQueue
	if err := s.db.Where("id = ? AND bot_id = ?", queueID, botID).First(&track).Error; err != nil {
		return errors.ErrNotFound
	}
	if err := s.startPlayback(botID, &track); err != nil {
		log.Printf("[bot] PlayNow startPlayback FAILED botID=%s queueID=%s trackID=%s title=%s source=%s err=%v", botID, queueID, track.TrackID, track.Title, track.Source, err)
		s.db.Where("id = ? AND bot_id = ?", track.ID, botID).Delete(&model.BotPlayQueue{})
		s.broadcastQueueForBot(botID)
		// Try the next track in the queue
		p := s.getOrCreatePlayer(botID)
		if err := s.playNext(botID, p, true); err != nil { // PlayNow 失败回退，应跳过失败的曲目
			return err
		}
		// 检查 playNext 后是否成功播放了某首曲目
		if !p.IsPlaying() && p.currentTrack == nil {
			return errors.New(errors.BOT_TRACK_NOT_FOUND, "无法播放该曲目，且队列中没有可用的备选曲目")
		}
		return nil
	}
	log.Printf("[bot] PlayNow startPlayback OK botID=%s queueID=%s title=%s", botID, queueID, track.Title)
	return nil
}

// startPlayback begins playing a track
//
// 阶段 2 起：当 cfg.UseMusicBotWorker=true 时，优先调用 Node.js Worker 进行播放。
// Worker 调用失败时回退到 Go 端 player_cgo.go 的旧播放逻辑（fallback），保证可用性。
func (s *Service) startPlayback(botID string, track *model.BotPlayQueue) error {
	p := s.getOrCreatePlayer(botID)
	p.StopIdleTimer() // Cancel any pending idle disconnect

	// 串行化：防止并发 startPlayback 的 Play()->Stop() 互相 kill FFmpeg 进程
	p.startMu.Lock()
	defer p.startMu.Unlock()

	var audioURL string
	if track.Source == "netease" {
		url, err := s.GetNeteaseURL(track.TrackID)
		if err != nil {
			log.Printf("[bot] startPlayback GetNeteaseURL FAILED trackID=%s err=%v", track.TrackID, err)
			return err
		}
		audioURL = url
		log.Printf("[bot] startPlayback GetNeteaseURL OK trackID=%s url=%s", track.TrackID, audioURL[:min(80, len(audioURL))])
	} else if track.Source == "upload" {
		var upload model.BotUploadAudio
		if err := s.db.Where("id = ?", track.TrackID).First(&upload).Error; err != nil {
			return errors.ErrNotFound
		}
		s.markUploadPlayed(upload.ID)
		audioURL = upload.FilePath
		// If the stored path is relative (legacy data), resolve to absolute
		if !filepath.IsAbs(audioURL) {
			audioURL = filepath.Join(s.cfg.LocalDataPath, audioURL)
		}
		log.Printf("[bot] startPlayback upload url=%s", audioURL)
	} else {
		return errors.New(errors.BOT_NOT_FOUND, "unsupported source type")
	}

	// 阶段 2：优先调用 Worker，失败回退到 Go 端 player_cgo
	if s.cfg.UseMusicBotWorker {
		if err := s.startPlaybackWithWorker(botID, track, audioURL); err != nil {
			log.Printf("[bot] startPlayback Worker FAILED botID=%s title=%s err=%v, falling back to local player", botID, track.Title, err)
			// 落到下面的 Go 端播放逻辑
		} else {
			log.Printf("[bot] startPlayback Worker OK botID=%s title=%s", botID, track.Title)
			// 同步 BotPlayer 的 currentTrack 状态（Worker 模式下未调用 p.Play()，
			// 必须在此处更新 currentTrack，否则 playNext 找不到当前曲目，
			// 会回退到 queue[0]，导致"点击下一首仍播放同一首歌"的 Bug）。
			p.SetCurrentTrackForWorker(track, audioURL)
			return nil
		}
	}

	if err := p.Play(track, audioURL); err != nil {
		log.Printf("[bot] startPlayback p.Play FAILED audioURL=%s err=%v", audioURL[:min(80, len(audioURL))], err)
		return err
	}
	log.Printf("[bot] startPlayback p.Play OK title=%s", track.Title)
	return nil
}

// playNext plays the next track based on play mode.
// isUserSkip=true 表示用户主动点击"下一首"（应切换到下一首曲目）；
// isUserSkip=false 表示自然播放结束（repeat-one 模式下应重复当前曲目）。
func (s *Service) playNext(botID string, p *BotPlayer, isUserSkip bool) error {
	if p == nil {
		p = s.getOrCreatePlayer(botID)
	}
	status := p.Status()
	playMode, _ := status["playMode"].(string)

	var queue []model.BotPlayQueue
	if err := s.db.Where("bot_id = ?", botID).Order("position ASC").Find(&queue).Error; err != nil {
		return errors.ErrInternal
	}
	if len(queue) == 0 {
		// Queue is empty — start 1-minute idle timer. If no new song is
		// added within that window the bot leaves the room and releases
		// its LiveKit connection + FFmpeg process (TSMusicBot idle pattern).
		p.StartIdleTimer(1 * time.Minute)
		return nil
	}

	currentTrack := p.currentTrack
	var nextTrack *model.BotPlayQueue

	switch playMode {
	case "random":
		// Use player's shuffle order if available
		nextTrack = p.NextShuffleTrack(queue)
		if nextTrack == nil {
			p.RegenerateShuffle(queue)
			nextTrack = p.NextShuffleTrack(queue)
		}
		if nextTrack == nil {
			nextTrack = &queue[rand.Intn(len(queue))]
		}
	case "repeat-one":
		// 单曲循环：自然播放结束（isUserSkip=false）时重复当前曲目；
		// 用户主动点击下一首（isUserSkip=true）时切换到队列中的下一首。
		if !isUserSkip && currentTrack != nil {
			for i := range queue {
				if queue[i].ID == currentTrack.ID {
					nextTrack = &queue[i]
					break
				}
			}
		} else if isUserSkip && currentTrack != nil {
			for i := range queue {
				if queue[i].ID == currentTrack.ID {
					if i+1 < len(queue) {
						nextTrack = &queue[i+1]
					} else {
						nextTrack = &queue[0] // 循环到队列开头
					}
					break
				}
			}
		}
		if nextTrack == nil && len(queue) > 0 {
			nextTrack = &queue[0]
		}
	default: // order, repeat-all
		if currentTrack != nil {
			for i := range queue {
				if queue[i].ID == currentTrack.ID {
					if i+1 < len(queue) {
						nextTrack = &queue[i+1]
					} else if playMode == "repeat-all" {
						nextTrack = &queue[0]
					}
					break
				}
			}
		}
		// NJ-32：order 模式下队列耗尽（自然播完/跳过末尾）不应回放 queue[0]
		// ——回放会把短片段（如版权试听）无限循环。仅在无当前曲目（全新
		// 自动播起步）时才从队头取曲；耗尽走下方 stop+idle 路径。
		if nextTrack == nil && currentTrack == nil && len(queue) > 0 {
			nextTrack = &queue[0]
		}
	}

	// Remove the finished track from queue (except repeat-one 自然结束).
	// 额外豁免：当 nextTrack 与 currentTrack 是同一首曲目时（repeat-all 模式下
	// 队列只有 1 首歌循环，或 order 模式下 fallback 到 queue[0]），不删除当前曲目，
	// 否则会导致"队列空但仍在播放同一首歌"的状态不一致。
	// 注意：repeat-one 模式下用户主动 Skip 时，应删除当前曲目（切换到下一首）。
	shouldRemoveCurrent := currentTrack != nil && !(playMode == "repeat-one" && !isUserSkip)
	if shouldRemoveCurrent {
		if nextTrack == nil || nextTrack.ID != currentTrack.ID {
			s.db.Where("id = ? AND bot_id = ?", currentTrack.ID, botID).Delete(&model.BotPlayQueue{})
		}
	}
	s.broadcastQueueForBot(botID)

	if nextTrack == nil {
		// 没有下一首可播，停止播放器并启动 idle timer 等待新曲目
		p.Stop()
		p.StartIdleTimer(1 * time.Minute)
		return nil
	}
	// Iterative playback loop (replaces recursive call to prevent stack overflow)
	const maxSkipAttempts = 50
	for attempt := 0; attempt < maxSkipAttempts; attempt++ {
		if nextTrack == nil {
			// 没有可播放的曲目，停止播放器并清除状态
			p.Stop()
			return nil
		}
		if err := s.startPlayback(botID, nextTrack); err != nil {
			// Track failed to play (e.g. song URL not available / copyright
			// restriction). Remove it from the queue and try the next one.
			log.Printf("[bot] playNext startPlayback FAILED (attempt %d) trackID=%s title=%s err=%v", attempt+1, nextTrack.TrackID, nextTrack.Title, err)
			s.db.Where("id = ? AND bot_id = ?", nextTrack.ID, botID).Delete(&model.BotPlayQueue{})
			s.broadcastQueueForBot(botID)
			// Get next track from queue
			var queue []model.BotPlayQueue
			s.db.Where("bot_id = ?", botID).Order("position ASC, created_at ASC").Find(&queue)
			if len(queue) == 0 {
				// 队列已空且所有曲目都播放失败，停止播放器并清除状态
				p.Stop()
				return nil
			}
			nextTrack = &queue[0]
			continue
		}
		break
	}
	return nil
}

// EnqueueUpload adds an uploaded audio file to the play queue
func (s *Service) EnqueueUpload(botID, uploadID, addedBy string) (*model.BotPlayQueue, error) {
	var upload model.BotUploadAudio
	if err := s.db.Where("id = ? AND pending_delete = ?", uploadID, false).First(&upload).Error; err != nil {
		return nil, errors.ErrNotFound
	}
	return s.AddToQueue(botID, upload.ID, upload.Title, "", upload.Duration, "", "", "upload", addedBy, nil)
}

// PriorityQueue moves a queue item to position 0 and starts playing it
func (s *Service) PriorityQueue(botID, queueID string) error {
	var item model.BotPlayQueue
	if err := s.db.Where("id = ? AND bot_id = ?", queueID, botID).First(&item).Error; err != nil {
		return errors.ErrNotFound
	}
	// Shift all existing items by 1
	if err := s.db.Model(&model.BotPlayQueue{}).Where("bot_id = ?", botID).UpdateColumn("position", gorm.Expr("position + 1")).Error; err != nil {
		return errors.ErrInternal
	}
	// Set target item to position 0
	item.Position = 0
	if err := s.db.Save(&item).Error; err != nil {
		return errors.ErrInternal
	}
	// Start playing immediately
	return s.startPlayback(botID, &item)
}

// NeteasePasswordLogin performs phone/password login via Netease API
func (s *Service) NeteasePasswordLogin(phone, password string) (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint == "" {
		return nil, errors.New(errors.BOT_NETEASE_NOT_CONFIGURED, "Netease API endpoint not configured")
	}
	result, err := s.NeteaseProxyPost("/login/cellphone", map[string]string{
		"phone":    phone,
		"password": password,
	})
	if err != nil {
		return nil, err
	}
	// Save cookie after successful login
	s.saveNeteaseAuth("")
	return result, nil
}

// NeteaseCookieLogin performs cookie-based login
func (s *Service) NeteaseCookieLogin(cookie string) (map[string]interface{}, error) {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint == "" {
		return nil, errors.New(errors.BOT_NETEASE_NOT_CONFIGURED, "Netease API endpoint not configured")
	}
	// Parse and set cookies in the jar
	u, _ := url.Parse(endpoint)
	if u != nil {
		parts := strings.Split(cookie, ";")
		var cookies []*http.Cookie
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 {
				cookies = append(cookies, &http.Cookie{
					Name:  kv[0],
					Value: kv[1],
				})
			}
		}
		s.neteaseClient.Jar.SetCookies(u, cookies)
	}
	// Verify login status
	result, err := s.NeteaseProxy("/login/status")
	if err != nil {
		return nil, err
	}
	s.saveNeteaseAuth(cookie)
	return result, nil
}

// NeteaseLogout clears Netease authentication
func (s *Service) NeteaseLogout() error {
	endpoint := s.neteaseAPIEndpoint()
	if endpoint != "" {
		s.NeteaseProxy("/logout")
	}
	// Clear cookies in jar by setting expired cookies
	u, _ := url.Parse(endpoint)
	if u != nil {
		existingCookies := s.neteaseClient.Jar.Cookies(u)
		expiredCookies := make([]*http.Cookie, 0, len(existingCookies))
		for _, c := range existingCookies {
			expiredCookies = append(expiredCookies, &http.Cookie{
				Name:    c.Name,
				Value:   "",
				Path:    "/",
				Domain:  c.Domain,
				Expires: time.Unix(0, 0),
				MaxAge:  -1,
			})
		}
		s.neteaseClient.Jar.SetCookies(u, expiredCookies)
	}
	// Clear persisted auth
	s.db.Where("1 = 1").Delete(&model.NeteaseAuth{})
	return nil
}

// GetSharedFolderAudio lists audio files in shared folders
func (s *Service) GetSharedFolderAudio() (map[string]interface{}, error) {
	// Query shared folder entries with audio MIME types
	var entries []model.SharedFileEntry
	if err := s.db.Where("mime_type LIKE ?", "audio/%").Find(&entries).Error; err != nil {
		return nil, errors.ErrInternal
	}
	items := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		items = append(items, map[string]interface{}{
			"id":       e.ID,
			"name":     e.FileName,
			"path":     e.FilePath,
			"size":     e.FileSize,
			"mimeType": e.MimeType,
		})
	}
	return map[string]interface{}{"items": items}, nil
}

// ImportSharedFolderAudio imports an audio file from shared folder to uploads
func (s *Service) ImportSharedFolderAudio(userID, filePath string) (*model.BotUploadAudio, error) {
	// Validate file exists and is within shared folder
	cleanPath := filepath.Clean(filePath)
	// Verify the path is within the shared folder root
	sharedRoot := filepath.Clean(s.cfg.LocalDataPath + "/shared")
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return nil, errors.ErrForbidden.WithDetails("invalid file path")
	}
	absRoot, err := filepath.Abs(sharedRoot)
	if err != nil {
		return nil, errors.ErrInternal
	}
	if !strings.HasPrefix(absPath, absRoot+string(os.PathSeparator)) && absPath != absRoot {
		return nil, errors.ErrForbidden.WithDetails("path traversal detected")
	}
	info, err := os.Stat(cleanPath)
	if err != nil {
		return nil, errors.ErrNotFound
	}
	if info.IsDir() {
		return nil, errors.ErrBadRequest.WithDetails("path is a directory")
	}

	// Copy to upload directory
	uploadDir := filepath.Join(s.cfg.LocalDataPath, "uploads/audio")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, errors.ErrInternal
	}
	fileID := idgen.GenerateID(idgen.PrefixFile)
	dstPath := filepath.Join(uploadDir, fileID+filepath.Ext(cleanPath))
	dstPath = filepath.Clean(dstPath)
	absDir, _ := filepath.Abs(uploadDir)
	absDst, _ := filepath.Abs(dstPath)
	if !strings.HasPrefix(absDst, absDir+string(filepath.Separator)) {
		return nil, errors.ErrForbidden
	}

	src, err := os.Open(cleanPath)
	if err != nil {
		return nil, errors.ErrInternal
	}
	defer src.Close()
	dst, err := os.Create(dstPath)
	if err != nil {
		return nil, errors.ErrInternal
	}
	defer dst.Close()
	written, err := io.Copy(dst, src)
	if err != nil {
		os.Remove(dstPath)
		return nil, errors.ErrInternal
	}

	upload := &model.BotUploadAudio{
		ID:        fileID,
		UserID:    userID,
		Title:     filepath.Base(cleanPath),
		FilePath:  dstPath,
		FileSize:  written,
		MimeType:  "audio/mpeg", // default; could be improved by detection
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(upload).Error; err != nil {
		os.Remove(dstPath)
		return nil, errors.ErrInternal
	}
	s.enforceUploadQuota(upload.ID)
	return upload, nil
}

// ===== M22: Bot Token Management =====
//
// These methods provide minimal token management for BotToken-based authorization.
// Only OWNER/ADMIN can create/revoke tokens; any authenticated user can list
// tokens for a space (read-only visibility for transparency).

// CreateBotToken generates a new bot token for a space.
// Returns the created Bot record (including the plaintext token — only time it is exposed).
func (s *Service) CreateBotToken(spaceID string, outputRoomID *string, name, createdBy string) (*model.Bot, error) {
	if err := s.requireSpaceMembership(createdBy, spaceID); err != nil {
		return nil, err
	}

	// Generate a cryptographically random token (32 bytes = 64 hex chars)
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return nil, errors.ErrInternal
	}
	token := hex.EncodeToString(b)

	bot := &model.Bot{
		ID:           idgen.GenerateID(idgen.PrefixBot),
		SpaceID:      spaceID,
		OutputRoomID: outputRoomID,
		Name:         name,
		Token:        token,
		CreatedBy:    createdBy,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := s.botRepo.Create(context.Background(), bot); err != nil {
		return nil, errors.ErrInternal
	}
	return bot, nil
}

// ListBotTokens returns all bot tokens for a space.
// The Token field is masked in the response (only last 4 chars visible).
func (s *Service) ListBotTokens(spaceID string) ([]map[string]interface{}, error) {
	bots, err := s.botRepo.ListBySpaceID(context.Background(), spaceID)
	if err != nil {
		return nil, errors.ErrInternal
	}
	items := make([]map[string]interface{}, 0, len(bots))
	for _, b := range bots {
		masked := ""
		if len(b.Token) > 4 {
			masked = "****" + b.Token[len(b.Token)-4:]
		} else {
			masked = "****"
		}
		items = append(items, map[string]interface{}{
			"id":           b.ID,
			"spaceId":      b.SpaceID,
			"outputRoomId": b.OutputRoomID,
			"name":         b.Name,
			"token":        masked,
			"createdBy":    b.CreatedBy,
			"createdAt":    b.CreatedAt,
		})
	}
	return items, nil
}

// DeleteBotToken revokes (soft-deletes) a bot token by ID.
func (s *Service) DeleteBotToken(id string) error {
	result := s.db.Delete(&model.Bot{}, "id = ?", id)
	if result.Error != nil {
		return errors.ErrInternal
	}
	if result.RowsAffected == 0 {
		return errors.ErrNotFound
	}
	return nil
}
