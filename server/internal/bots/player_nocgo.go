//go:build !cgo

package bots

import (
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
)

var errNoCGO = errors.New("music bot requires CGO (opus audio codec), please build with CGO_ENABLED=1 and a C compiler installed")

// BotPlayer is a stub when CGO is not available.
type BotPlayer struct {
	db           *gorm.DB
	cfg          *config.Config
	hub          *realtime.Hub
	botID        string
	currentTrack *model.BotPlayQueue
	onComplete   func()

	mu          sync.RWMutex
	playing     bool
	paused      bool
	currentTime int
	volume      int
	playMode    string

	// startMu 串行化 startPlayback 调用（与 cgo 版本保持字段一致）
	startMu sync.Mutex
}

// NewBotPlayer creates a stub bot player.
func NewBotPlayer(db *gorm.DB, cfg *config.Config, botID string, hub *realtime.Hub) *BotPlayer {
	return &BotPlayer{
		db:       db,
		cfg:      cfg,
		hub:      hub,
		botID:    botID,
		volume:   80,
		playMode: "order",
	}
}

// SetOnComplete sets the callback for when a track finishes (no-op).
func (p *BotPlayer) SetOnComplete(cb func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onComplete = cb
}

// SetOnDisconnect is a no-op in the stub.
func (p *BotPlayer) SetOnDisconnect(cb func(botID string)) {}

// IsPlaying returns whether the player is currently playing.
func (p *BotPlayer) IsPlaying() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.playing
}

// IsPaused returns whether the player is paused.
func (p *BotPlayer) IsPaused() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.paused
}

// IsConnected returns false in the stub (no LiveKit connection).
func (p *BotPlayer) IsConnected() bool { return false }

// StartIdleTimer is a no-op in the stub.
func (p *BotPlayer) StartIdleTimer(d time.Duration) {}

// StopIdleTimer is a no-op in the stub.
func (p *BotPlayer) StopIdleTimer() {}

// Connect returns an error indicating CGO is required.
func (p *BotPlayer) Connect() error {
	return errNoCGO
}

// Disconnect is a no-op in the stub.
func (p *BotPlayer) Disconnect() {}

// Play returns an error indicating CGO is required.
func (p *BotPlayer) Play(track *model.BotPlayQueue, audioURL string) error {
	return errNoCGO
}

// Stop is a no-op in the stub.
func (p *BotPlayer) Stop() {}

// Pause is a no-op in the stub.
func (p *BotPlayer) Pause() {}

// Resume is a no-op in the stub.
func (p *BotPlayer) Resume() {}

// Seek returns an error indicating CGO is required.
func (p *BotPlayer) Seek(seconds int) error {
	return errNoCGO
}

// SetVolume is a no-op in the stub.
func (p *BotPlayer) SetVolume(v int) {
	if v < 0 {
		v = 0
	}
	if v > 200 {
		v = 200
	}
	p.mu.Lock()
	p.volume = v
	p.mu.Unlock()
}

// SetPlayMode is a no-op in the stub.
func (p *BotPlayer) SetPlayMode(mode string) {
	p.mu.Lock()
	p.playMode = mode
	p.mu.Unlock()
}

// Status returns an empty playing state.
func (p *BotPlayer) Status() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var track map[string]interface{}
	if p.currentTrack != nil {
		track = map[string]interface{}{
			"id":       p.currentTrack.ID,
			"title":    p.currentTrack.Title,
			"artist":   p.currentTrack.Artist,
			"duration": p.currentTrack.Duration,
			"cover":    p.currentTrack.Cover,
			"album":    p.currentTrack.Album,
			"source":   p.currentTrack.Source,
		}
	}

	return map[string]interface{}{
		"playing":      p.playing,
		"paused":       p.paused,
		"currentTime":  p.currentTime,
		"volume":       p.volume,
		"playMode":     p.playMode,
		"currentTrack": track,
	}
}

// PlayTTS returns an error indicating CGO is required.
// M18: signature updated to accept username parameter for "[username] 说" prefix.
func (p *BotPlayer) PlayTTS(audioPath, username string) error {
	return errNoCGO
}

// PreviousTrack returns nil in the stub.
func (p *BotPlayer) PreviousTrack() *model.BotPlayQueue {
	return nil
}

// NextShuffleTrack returns nil in the stub.
func (p *BotPlayer) NextShuffleTrack(queue []model.BotPlayQueue) *model.BotPlayQueue {
	return nil
}

// RegenerateShuffle is a no-op in the stub.
func (p *BotPlayer) RegenerateShuffle(queue []model.BotPlayQueue) {}

// broadcastQueue is a no-op in the stub.
func (p *BotPlayer) broadcastQueue(items []model.BotPlayQueue) {}

// BroadcastExternalState is a no-op in the stub (no hub broadcast without CGO).
// Defined to keep the Service API consistent across cgo/nocgo builds.
func (p *BotPlayer) BroadcastExternalState(status map[string]interface{}) {}

// SetCurrentTrackForWorker 在 Worker 模式下同步 BotPlayer 的 currentTrack 状态。
//
// nocgo 构建下 BotPlayer 结构体字段较少（无 playHistory/currentURL 等），
// 仅更新 currentTrack / playing / paused / currentTime 让 playNext / Status
// 读取到正确的当前曲目。不进行 saveState / broadcastState（nocgo 下 hub/DB 不可用）。
func (p *BotPlayer) SetCurrentTrackForWorker(track *model.BotPlayQueue, audioURL string) {
	p.mu.Lock()
	p.currentTrack = track
	p.playing = true
	p.paused = false
	p.currentTime = 0
	p.mu.Unlock()
}
