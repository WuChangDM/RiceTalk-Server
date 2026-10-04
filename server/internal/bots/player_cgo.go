//go:build cgo

package bots

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/hraban/opus"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"gorm.io/gorm"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
)

const (
	opusSampleRate    = 48000
	opusFrameDuration = 20 * time.Millisecond
	opusChannels      = 2
	opusBitrate       = "128k"
	broadcastInterval = 500 * time.Millisecond // broadcast state every 2 seconds during playback

	// M18 ducking constants
	duckMultiplierDucked = 0.3 // music volume multiplier during TTS playback
	duckRecoverySteps    = 10  // gradual recovery steps
	duckRecoveryStepMs   = 20  // milliseconds per recovery step (200ms total)

	// maxOpusPacketSize is the maximum encoded opus packet size we handle.
	maxOpusPacketSize = 4000
	// pcmFrameSize is the number of float32 samples per 20ms frame at 48kHz stereo.
	pcmFrameSize = opusSampleRate / 1000 * int(opusFrameDuration/time.Millisecond) * opusChannels
)

// oggPacketInfo records the file offset and size of an opus packet
type oggPacketInfo struct {
	offset int64
	size   int
}

// oggTrackReader holds an open OGG file and pre-scanned packet offsets
type oggTrackReader struct {
	file    *os.File
	packets []oggPacketInfo
}

func (r *oggTrackReader) ReadPacket(index int) ([]byte, error) {
	if index < 0 || index >= len(r.packets) {
		return nil, io.EOF
	}
	pkt := r.packets[index]
	buf := make([]byte, pkt.size)
	if _, err := r.file.ReadAt(buf, pkt.offset); err != nil {
		return nil, err
	}
	return buf, nil
}

func (r *oggTrackReader) Close() error {
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}

func (r *oggTrackReader) Len() int {
	return len(r.packets)
}

// BotPlayer manages music playback for a bot via LiveKit
type BotPlayer struct {
	db    *gorm.DB
	cfg   *config.Config
	hub   *realtime.Hub
	botID string

	room   *lksdk.Room
	track  *lksdk.LocalTrack
	logger protoLogger.Logger

	mu           sync.RWMutex
	playing      bool
	paused       bool
	currentTime  int // seconds
	volume       int
	playMode     string
	currentTrack *model.BotPlayQueue
	currentURL   string

	// Playback state
	oggReader   *oggTrackReader
	packetIndex int
	stopCh      chan struct{}
	wg          sync.WaitGroup

	// FFmpeg process reference (for kill)
	ffmpegCmd *exec.Cmd

	// OGG file path (kept during playback for seeking)
	oggFilePath string

	// playLoopGen tracks which playLoop instance is current.
	// Used by the old playLoop to detect it has been replaced.
	playLoopGen int

	// seekOffset is added to idx/50 in playLoop to compute currentTime.
	// Set to 0 by Play() (file starts at t=0) and to seconds by Seek()
	// (file is re-encoded from seek position).
	seekOffset int

	// Play history for "previous track" support
	playHistory  []*model.BotPlayQueue
	historyIndex int // -1 means not navigating history

	// Shuffle order for random mode (stores queue item IDs)
	shuffleOrder []string
	shuffleIndex int

	// Callback when track completes
	onComplete func()
	// Callback when player disconnects (for service to remove from map)
	onDisconnect func(botID string)

	// Idle timer: after last track finishes and queue is empty,
	// disconnect after 1 minute to release resources (TSMusicBot pattern).
	idleTimer *time.Timer

	// --- M18 parallel TTS ducking (multi-track architecture) ---

	// ttsTrack is a second LiveKit track dedicated to TTS audio.
	// Music continues on `track` while TTS plays on `ttsTrack`.
	ttsTrack *lksdk.LocalTrack

	// TTS playback state (independent from music playback).
	ttsMu      sync.RWMutex
	ttsPlaying bool
	ttsStopCh  chan struct{}
	ttsWg      sync.WaitGroup

	// opusDecoder/opusEncoder are used for real-time volume scaling
	// (decode opus → scale PCM → re-encode opus) in playLoop.
	opusDecoder *opus.Decoder
	opusEncoder *opus.Encoder

	// duckMultiplier is applied to the music volume during TTS playback.
	// 1.0 = normal, 0.3 = ducked. Gradually restored after TTS finishes.
	// duckGen is incremented on each ducking state change so stale
	// recovery goroutines can detect they've been superseded.
	duckMultiplier float32
	duckGen        int

	// startMu 串行化 startPlayback 调用，防止并发 Play() 的 Stop() 互相 kill FFmpeg 进程
	startMu sync.Mutex
}

// NewBotPlayer creates a new bot player for a bot
func NewBotPlayer(db *gorm.DB, cfg *config.Config, botID string, hub *realtime.Hub) *BotPlayer {
	return &BotPlayer{
		db:             db,
		cfg:            cfg,
		hub:            hub,
		botID:          botID,
		logger:         protoLogger.GetLogger(),
		volume:         80,
		playMode:       "order",
		stopCh:         make(chan struct{}),
		ttsStopCh:      make(chan struct{}),
		duckMultiplier: 1.0,
	}
}

// SetOnComplete sets the callback for when a track finishes
func (p *BotPlayer) SetOnComplete(cb func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onComplete = cb
}

// SetOnDisconnect sets the callback for when the player disconnects
func (p *BotPlayer) SetOnDisconnect(cb func(botID string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onDisconnect = cb
}

// IsPlaying returns whether the player is actively playing audio
func (p *BotPlayer) IsPlaying() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.playing
}

// IsPaused returns whether the player is paused
func (p *BotPlayer) IsPaused() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.paused
}

// IsConnected returns whether the LiveKit room connection is established
func (p *BotPlayer) IsConnected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.room != nil
}

// StartIdleTimer starts a timer that disconnects the player after d.
// If a new track is added before the timer fires, StopIdleTimer should
// be called to cancel it.
func (p *BotPlayer) StartIdleTimer(d time.Duration) {
	p.StopIdleTimer()
	p.mu.Lock()
	p.idleTimer = time.AfterFunc(d, func() {
		p.Disconnect()
		// After disconnect, callback to let the service know to
		// remove this player from the map.
		p.mu.RLock()
		cb := p.onDisconnect
		botID := p.botID
		p.mu.RUnlock()
		if cb != nil {
			cb(botID)
		}
	})
	p.mu.Unlock()
}

// StopIdleTimer cancels any pending idle timer
func (p *BotPlayer) StopIdleTimer() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
}

// Connect joins the LiveKit room as a bot participant.
// The target room is the bot's configured OutputRoomID.
func (p *BotPlayer) Connect() error {
	var bot model.Bot
	if err := p.db.Where("id = ?", p.botID).First(&bot).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("bot not found")
		}
		return fmt.Errorf("failed to load bot: %w", err)
	}
	if bot.OutputRoomID == nil || *bot.OutputRoomID == "" {
		return fmt.Errorf("bot has no output room configured")
	}
	roomID := *bot.OutputRoomID
	roomName := "rrt-room-" + roomID

	// Generate bot token with publish-only permissions.
	// Note: hidden is set to false because hidden participants do not receive
	// ParticipantJoined updates on other clients, which causes LiveKit SDK to
	// reject their published tracks with "Tried to add a track for a participant,
	// that's not present" error. Bot is filtered from UI via channelParticipants
	// (API-sourced), not via LiveKit participants list.
	at := auth.NewAccessToken(p.cfg.LiveKitAPIKey, p.cfg.LiveKitAPISecret)
	canPublish := true
	canSubscribe := false
	hidden := false
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanPublish:   &canPublish,
		CanSubscribe: &canSubscribe,
		Hidden:       hidden,
	}
	at.SetVideoGrant(grant).
		SetIdentity("bot-music-" + p.botID).
		SetName("音乐机器人")

	token, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("failed to generate bot token: %w", err)
	}

	room, err := lksdk.ConnectToRoomWithToken(p.cfg.LiveKitURL, token, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to LiveKit room: %w", err)
	}

	p.room = room

	// Create Opus audio track
	track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: opusSampleRate,
		Channels:  opusChannels,
	})
	if err != nil {
		room.Disconnect()
		return fmt.Errorf("failed to create local track: %w", err)
	}

	if _, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "bot-music",
		Source: livekit.TrackSource_MICROPHONE,
		Stereo: true,
	}); err != nil {
		track.Close()
		room.Disconnect()
		return fmt.Errorf("failed to publish track: %w", err)
	}

	// M18: Publish a second track for TTS audio so music and TTS can
	// play in parallel on separate tracks.
	ttsTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: opusSampleRate,
		Channels:  opusChannels,
	})
	if err != nil {
		track.Close()
		room.Disconnect()
		return fmt.Errorf("failed to create tts track: %w", err)
	}
	if _, err := room.LocalParticipant.PublishTrack(ttsTrack, &lksdk.TrackPublicationOptions{
		Name:   "bot-tts",
		Source: livekit.TrackSource_MICROPHONE,
		Stereo: true,
	}); err != nil {
		track.Close()
		ttsTrack.Close()
		room.Disconnect()
		return fmt.Errorf("failed to publish tts track: %w", err)
	}

	// M18: Create opus decoder/encoder for real-time volume scaling.
	// Used by playLoop to apply volume and ducking to music packets.
	decoder, err := opus.NewDecoder(opusSampleRate, opusChannels)
	if err != nil {
		track.Close()
		ttsTrack.Close()
		room.Disconnect()
		return fmt.Errorf("failed to create opus decoder: %w", err)
	}
	encoder, err := opus.NewEncoder(opusSampleRate, opusChannels, opus.AppAudio)
	if err != nil {
		track.Close()
		ttsTrack.Close()
		room.Disconnect()
		return fmt.Errorf("failed to create opus encoder: %w", err)
	}
	// Match the FFmpeg encoding bitrate (128k).
	if err := encoder.SetBitrate(128000); err != nil {
		p.logger.Warnw("failed to set opus encoder bitrate", err)
	}

	p.track = track
	p.ttsTrack = ttsTrack
	p.opusDecoder = decoder
	p.opusEncoder = encoder
	return nil
}

// Disconnect leaves the LiveKit room and stops playback
func (p *BotPlayer) Disconnect() {
	p.StopIdleTimer()
	p.Stop()
	p.stopTTS()
	p.broadcastState() // notify frontend that playback stopped
	if p.ttsTrack != nil {
		p.ttsTrack.Close()
		p.ttsTrack = nil
	}
	if p.track != nil {
		p.track.Close()
		p.track = nil
	}
	if p.room != nil {
		p.room.Disconnect()
		p.room = nil
	}
}

// Play starts playing an audio URL
func (p *BotPlayer) Play(track *model.BotPlayQueue, audioURL string) error {
	p.Stop()

	// Ensure connected to LiveKit
	if p.room == nil || p.track == nil {
		if err := p.Connect(); err != nil {
			return fmt.Errorf("failed to connect bot player: %w", err)
		}
	}

	// 立即更新 currentTrack 并广播状态，让前端 UI 即时切换到新曲目。
	// 转码（convertToOpus）可能耗时数秒，若等转码完成才更新 currentTrack，
	// 会出现"点击下一首后 UI 仍显示旧曲目、队列看似未变化"的体验问题。
	// 转码失败时由 rollbackPlayerState 回滚状态。
	p.mu.Lock()
	// Record play history: truncate forward history if navigating back, then append
	if p.historyIndex >= 0 && len(p.playHistory) > 0 {
		p.playHistory = p.playHistory[:p.historyIndex+1]
	}
	if p.currentTrack != nil {
		p.playHistory = append(p.playHistory, p.currentTrack)
		if len(p.playHistory) > 50 {
			p.playHistory = p.playHistory[len(p.playHistory)-50:]
		}
	}
	p.historyIndex = -1
	p.currentTrack = track
	p.currentURL = audioURL
	p.playing = true
	p.paused = false
	p.currentTime = 0
	p.mu.Unlock()

	p.saveState()
	p.broadcastState() // 即时推送：UI 立刻切换到新曲目

	// Convert audio to OGG Opus
	tmpDir := filepath.Join(p.cfg.LocalDataPath, "audio", "bot-tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		p.rollbackPlayerState(track)
		return fmt.Errorf("failed to create tmp dir: %w", err)
	}
	tmpFile := filepath.Join(tmpDir, track.ID+".ogg")

	if err := p.convertToOpus(audioURL, tmpFile, 0); err != nil {
		p.rollbackPlayerState(track)
		return fmt.Errorf("failed to convert audio: %w", err)
	}

	// Scan OGG file offsets (do not load all packets into memory)
	reader, err := scanOggPackets(tmpFile)
	if err != nil {
		os.Remove(tmpFile)
		p.rollbackPlayerState(track)
		return fmt.Errorf("failed to scan OGG packets: %w", err)
	}

	p.mu.Lock()
	p.seekOffset = 0
	p.oggReader = reader
	p.packetIndex = 0
	p.playLoopGen++
	p.oggFilePath = tmpFile
	p.stopCh = make(chan struct{})
	p.mu.Unlock()

	p.broadcastState() // 转码完成，即将开始实际播放

	p.wg.Add(1)
	go p.playLoop()

	return nil
}

// rollbackPlayerState 撤销 Play() 中提前设置的 currentTrack 状态。
// 当转码失败时调用，避免 UI 显示"正在播放"但实际无音频输出。
// 仅当 currentTrack 仍是失败曲目时才清除，防止并发 Play() 互相覆盖。
func (p *BotPlayer) rollbackPlayerState(failedTrack *model.BotPlayQueue) {
	p.mu.Lock()
	if p.currentTrack != nil && p.currentTrack.ID == failedTrack.ID {
		p.currentTrack = nil
		p.currentURL = ""
		p.playing = false
		p.paused = false
		p.currentTime = 0
	}
	p.mu.Unlock()
	p.saveState()
	p.broadcastState()
}

// SetCurrentTrackForWorker 在 Worker 模式下同步 BotPlayer 的 currentTrack 状态。
//
// 背景：Worker 模式下 startPlaybackWithWorker 成功后直接返回，未调用 p.Play()，
// 导致 p.currentTrack 一直为 nil。playNext 读取 p.currentTrack 找不到当前曲目，
// 回退到 queue[0]，造成"点击下一首仍播放同一首歌"的 Bug。
//
// 此方法复制 Play() 中的状态更新逻辑（不含转码/连接 LiveKit）：
//   - 更新 currentTrack / currentURL / playing / paused / currentTime
//   - 记录 playHistory（支持 PreviousTrack 回退）
//   - 持久化 saveState + 广播 broadcastState
//
// 调用方：service.go#startPlaybackWithWorker 成功分支。
func (p *BotPlayer) SetCurrentTrackForWorker(track *model.BotPlayQueue, audioURL string) {
	p.mu.Lock()
	// Record play history: truncate forward history if navigating back, then append
	if p.historyIndex >= 0 && len(p.playHistory) > 0 {
		p.playHistory = p.playHistory[:p.historyIndex+1]
	}
	if p.currentTrack != nil {
		p.playHistory = append(p.playHistory, p.currentTrack)
		if len(p.playHistory) > 50 {
			p.playHistory = p.playHistory[len(p.playHistory)-50:]
		}
	}
	p.historyIndex = -1
	p.currentTrack = track
	p.currentURL = audioURL
	p.playing = true
	p.paused = false
	p.currentTime = 0
	p.mu.Unlock()

	p.saveState()
	p.broadcastState()
}

// Stop stops current playback and waits for playLoop to exit.
func (p *BotPlayer) Stop() {
	p.signalStop()
	p.mu.Lock()
	if p.oggReader != nil {
		p.oggReader.Close()
		p.oggReader = nil
	}
	if p.oggFilePath != "" {
		os.Remove(p.oggFilePath)
		p.oggFilePath = ""
	}
	p.playing = false
	p.paused = false
	p.packetIndex = 0
	p.mu.Unlock()

	p.wg.Wait()
}

// signalStop closes stopCh and kills FFmpeg without waiting for
// playLoop exit. Safe for use in Seek() where we start a new
// playLoop immediately without blocking.
func (p *BotPlayer) signalStop() {
	p.mu.Lock()
	select {
	case <-p.stopCh:
		// already closed
	default:
		close(p.stopCh)
	}
	if p.ffmpegCmd != nil && p.ffmpegCmd.Process != nil {
		p.ffmpegCmd.Process.Kill()
	}
	p.mu.Unlock()
}

// Pause pauses playback
func (p *BotPlayer) Pause() {
	p.mu.Lock()
	p.playing = false
	p.paused = true
	p.mu.Unlock()
	p.saveState()
	p.broadcastState()
}

// Resume resumes playback
func (p *BotPlayer) Resume() {
	p.mu.Lock()
	p.playing = true
	p.paused = false
	p.mu.Unlock()
	p.saveState()
	p.broadcastState()
}

// Seek seeks to a specific time (in seconds).

func (p *BotPlayer) Seek(seconds int) error {
	// Snapshot state under lock
	p.mu.RLock()
	wasPlaying := p.playing
	ct := p.currentTrack
	cu := p.currentURL
	p.mu.RUnlock()

	if !wasPlaying || ct == nil || cu == "" {
		return nil
	}

	// Signal old playLoop to stop and wait for it to exit.
	// We must not close the OGG file while the old playLoop is
	// reading from it (causes "file already closed").
	p.signalStop()
	p.wg.Wait()

	// Now safe to clean up
	p.mu.Lock()
	if p.oggReader != nil {
		p.oggReader.Close()
		p.oggReader = nil
	}
	if p.oggFilePath != "" {
		os.Remove(p.oggFilePath)
		p.oggFilePath = ""
	}
	p.mu.Unlock()

	// Re-encode from the seek position (no lock needed for I/O)
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("bot_seek_%s_*.ogg", p.botID))
	if err != nil {
		return fmt.Errorf("seek: temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	if err := p.convertToOpus(cu, tmpPath, seconds); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("seek: re-encode: %w", err)
	}

	reader, err := scanOggPackets(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("seek: scan: %w", err)
	}

	// Set up new playLoop state under lock
	p.mu.Lock()
	p.playing = true
	p.paused = false
	p.oggReader = reader
	p.oggFilePath = tmpPath
	p.playLoopGen++
	p.packetIndex = 0
	p.seekOffset = seconds
	p.currentTime = seconds
	p.stopCh = make(chan struct{})
	p.mu.Unlock()

	p.wg.Add(1)
	go p.playLoop()

	p.saveState()
	p.broadcastState()
	return nil
}

// SetVolume sets playback volume (0-100) - stored for frontend reference
func (p *BotPlayer) SetVolume(v int) {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	p.mu.Lock()
	p.volume = v
	p.mu.Unlock()
	p.saveState()
	p.broadcastState()
	p.broadcastVolumeChanged()
}

// SetPlayMode sets play mode
func (p *BotPlayer) SetPlayMode(mode string) {
	p.mu.Lock()
	oldMode := p.playMode
	p.playMode = mode
	if mode == "random" && oldMode != "random" {
		p.shuffleIndex = 0
		p.shuffleOrder = nil
	}
	p.mu.Unlock()
	p.saveState()
	p.broadcastState()
}

// PreviousTrack returns the previous track from history, or nil if none
func (p *BotPlayer) PreviousTrack() *model.BotPlayQueue {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.playHistory) == 0 {
		return nil
	}
	idx := len(p.playHistory) - 1
	if p.historyIndex >= 0 && len(p.playHistory) > 0 {
		idx = p.historyIndex - 1
	}
	if idx < 0 {
		return nil
	}
	p.historyIndex = idx
	return p.playHistory[idx]
}

// RegenerateShuffle creates a new Fisher-Yates shuffle of queue item IDs
func (p *BotPlayer) RegenerateShuffle(queue []model.BotPlayQueue) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.shuffleOrder = make([]string, len(queue))
	for i, q := range queue {
		p.shuffleOrder[i] = q.ID
	}
	// Fisher-Yates shuffle
	for i := len(p.shuffleOrder) - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		p.shuffleOrder[i], p.shuffleOrder[j] = p.shuffleOrder[j], p.shuffleOrder[i]
	}
	p.shuffleIndex = 0
}

// NextShuffleTrack returns the next track according to shuffle order.
// If shuffle order is empty or exhausted, it regenerates and returns nil (caller falls back to random).
func (p *BotPlayer) NextShuffleTrack(queue []model.BotPlayQueue) *model.BotPlayQueue {
	p.mu.Lock()
	defer p.mu.Unlock()

	// If queue changed significantly, regenerate
	if len(p.shuffleOrder) != len(queue) {
		p.shuffleOrder = nil
	}

	if len(p.shuffleOrder) == 0 || p.shuffleIndex >= len(p.shuffleOrder) {
		return nil // signal caller to regenerate and fallback
	}

	targetID := p.shuffleOrder[p.shuffleIndex]
	p.shuffleIndex++

	for i := range queue {
		if queue[i].ID == targetID {
			return &queue[i]
		}
	}
	return nil
}

// Status returns current playback status
func (p *BotPlayer) Status() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var track map[string]interface{}
	// 仅在播放中或暂停时返回 currentTrack。
	// 完全停止状态（!playing && !paused）下不返回，避免 ClearQueue/Stop
	// 后 Status() 仍显示残留的 track 信息。
	if p.currentTrack != nil && (p.playing || p.paused) {
		track = map[string]interface{}{
			"id":       p.currentTrack.ID,
			"trackId":  p.currentTrack.TrackID,
			"title":    p.currentTrack.Title,
			"artist":   p.currentTrack.Artist,
			"duration": p.currentTrack.Duration,
			"cover":    p.currentTrack.Cover,
			"album":    p.currentTrack.Album,
			"source":   p.currentTrack.Source,
		}
	}

	// Only reset currentTime to 0 when there is no current track
	ct := p.currentTime
	if p.currentTrack == nil || (!p.playing && !p.paused) {
		ct = 0
	}
	return map[string]interface{}{
		"playing":      p.playing,
		"paused":       p.paused,
		"currentTime":  ct,
		"volume":       p.volume,
		"playMode":     p.playMode,
		"currentTrack": track,
	}
}

// PlayTTS plays a TTS audio file in parallel with music using a separate
// LiveKit track (bot-tts). Music continues playing on the bot-music track
// with the volume ducked to 30%. When TTS finishes, the music volume is
// gradually restored over 200ms.
//
// M18: If username is non-empty, synthesizes a "[username] 说" prefix audio
// cue and concatenates it with the TTS audio so listeners hear who is
// speaking before the message content.
//
// This is the Stage 2 multi-track implementation: TTS no longer calls Play()
// (which would stop music). Instead it plays on p.ttsTrack independently.
func (p *BotPlayer) PlayTTS(audioPath, username string) error {
	// Ensure connected to LiveKit (creates both bot-music and bot-tts tracks)
	if p.room == nil || p.ttsTrack == nil {
		if err := p.Connect(); err != nil {
			return fmt.Errorf("failed to connect bot player: %w", err)
		}
	}

	// Stop any existing TTS playback before starting a new one.
	p.stopTTS()

	// M18: Synthesize "[username] 说" prefix and concatenate with TTS audio.
	// If synthesis fails (e.g. edge-tts unavailable), fall back to the
	// original audioPath so TTS still works without the prefix.
	ttsPath := audioPath
	var combinedPath string
	if username != "" {
		if combined, err := p.synthesizeAndConcatPrefix(username, audioPath); err == nil {
			ttsPath = combined
			combinedPath = combined
		}
	}

	// Convert TTS audio to OGG Opus (same format as music)
	tmpDir := filepath.Join(p.cfg.LocalDataPath, "audio", "bot-tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		if combinedPath != "" {
			os.Remove(combinedPath)
		}
		return fmt.Errorf("failed to create tmp dir: %w", err)
	}
	ttsOggFile := filepath.Join(tmpDir, "tts-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".ogg")
	if err := p.convertToOpus(ttsPath, ttsOggFile, 0); err != nil {
		os.Remove(ttsOggFile)
		if combinedPath != "" {
			os.Remove(combinedPath)
		}
		return fmt.Errorf("failed to convert TTS audio: %w", err)
	}

	reader, err := scanOggPackets(ttsOggFile)
	if err != nil {
		os.Remove(ttsOggFile)
		if combinedPath != "" {
			os.Remove(combinedPath)
		}
		return fmt.Errorf("failed to scan TTS OGG packets: %w", err)
	}

	// Duck music: lower the volume multiplier to 30% and bump duckGen
	// so any stale recovery goroutine aborts.
	p.mu.Lock()
	p.duckGen++
	duckGen := p.duckGen
	p.duckMultiplier = duckMultiplierDucked
	p.mu.Unlock()

	// Start TTS playback on the dedicated ttsTrack.
	p.ttsMu.Lock()
	p.ttsPlaying = true
	p.ttsStopCh = make(chan struct{})
	ttsStopCh := p.ttsStopCh
	p.ttsMu.Unlock()

	p.ttsWg.Add(1)
	go p.playTTSLoop(reader, ttsOggFile, combinedPath, ttsStopCh, duckGen)

	return nil
}

// stopTTS signals the current TTS playback to stop and waits for it to exit.
func (p *BotPlayer) stopTTS() {
	p.ttsMu.Lock()
	select {
	case <-p.ttsStopCh:
		// already closed
	default:
		close(p.ttsStopCh)
	}
	p.ttsPlaying = false
	p.ttsMu.Unlock()
	p.ttsWg.Wait()
}

// playTTSLoop writes TTS opus packets to the bot-tts LiveKit track.
// It runs independently from playLoop (music), allowing true parallel
// playback. When the loop exits (TTS finished or stopped), it triggers
// ducking recovery to gradually restore the music volume.
func (p *BotPlayer) playTTSLoop(reader *oggTrackReader, oggPath, combinedPath string, stopCh chan struct{}, duckGen int) {
	defer p.ttsWg.Done()
	defer reader.Close()
	defer os.Remove(oggPath)
	if combinedPath != "" {
		defer os.Remove(combinedPath)
	}

	// Always trigger ducking recovery on exit.
	defer p.restoreDucking(duckGen)

	p.ttsMu.RLock()
	track := p.ttsTrack
	p.ttsMu.RUnlock()

	if track == nil || reader.Len() == 0 {
		return
	}

	startTime := time.Now()

	for i := 0; i < reader.Len(); i++ {
		select {
		case <-stopCh:
			return
		default:
		}

		// Calculate when this packet should be sent (real-time pacing)
		targetTime := startTime.Add(time.Duration(i) * opusFrameDuration)
		now := time.Now()
		if now.Before(targetTime) {
			timer := time.NewTimer(targetTime.Sub(now))
			select {
			case <-stopCh:
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		pkt, err := reader.ReadPacket(i)
		if err != nil {
			p.logger.Errorw("failed to read TTS opus packet", err)
			return
		}

		sample := media.Sample{
			Data:     pkt,
			Duration: opusFrameDuration,
		}
		if err := track.WriteSample(sample, nil); err != nil {
			p.logger.Errorw("failed to write TTS sample", err)
			return
		}
	}
}

// restoreDucking gradually restores the music volume multiplier from
// duckMultiplierDucked (0.3) back to 1.0 over ~200ms (10 steps × 20ms).
// The duckGen parameter is compared against p.duckGen to detect if a newer
// ducking event (new TTS started) has superseded this recovery — if so,
// the recovery aborts immediately.
func (p *BotPlayer) restoreDucking(duckGen int) {
	p.ttsMu.Lock()
	p.ttsPlaying = false
	p.ttsMu.Unlock()

	go func() {
		for i := 1; i <= duckRecoverySteps; i++ {
			time.Sleep(time.Duration(duckRecoveryStepMs) * time.Millisecond)
			p.mu.Lock()
			if p.duckGen != duckGen {
				// A newer TTS has started; abort recovery.
				p.mu.Unlock()
				return
			}
			p.duckMultiplier = duckMultiplierDucked + (1.0-duckMultiplierDucked)*float32(i)/float32(duckRecoverySteps)
			p.mu.Unlock()
		}
		p.mu.Lock()
		if p.duckGen == duckGen {
			p.duckMultiplier = 1.0
		}
		p.mu.Unlock()
	}()
}

// scaleOpusVolume decodes an opus packet to float32 PCM, scales every sample
// by coeff, and re-encodes to opus. If coeff is ~1.0 the original packet is
// returned unchanged (no decode/encode overhead). If the decoder/encoder are
// unavailable or scaling fails, the original packet is returned as a fallback.
func (p *BotPlayer) scaleOpusVolume(pkt []byte, coeff float32) ([]byte, error) {
	if coeff > 0.999 && coeff < 1.001 {
		return pkt, nil
	}
	if p.opusDecoder == nil || p.opusEncoder == nil {
		return pkt, nil
	}

	pcm := make([]float32, pcmFrameSize)
	n, err := p.opusDecoder.DecodeFloat32(pkt, pcm)
	if err != nil {
		return nil, fmt.Errorf("opus decode: %w", err)
	}
	// n is samples per channel; total float32 values = n * channels
	total := n * opusChannels
	for i := 0; i < total; i++ {
		pcm[i] *= coeff
	}

	outBuf := make([]byte, maxOpusPacketSize)
	m, err := p.opusEncoder.EncodeFloat32(pcm[:total], outBuf)
	if err != nil {
		return nil, fmt.Errorf("opus encode: %w", err)
	}
	return outBuf[:m], nil
}

// synthesizeAndConcatPrefix synthesizes a "[username] 说" prefix audio cue
// using edge-tts and concatenates it with the TTS audio file using ffmpeg.
// Returns the path to the combined audio file. Caller is responsible for
// removing the returned file when done.
func (p *BotPlayer) synthesizeAndConcatPrefix(username, ttsAudioPath string) (string, error) {
	tmpDir := filepath.Join(p.cfg.LocalDataPath, "audio", "bot-tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create tmp dir: %w", err)
	}

	prefixID := "tts-prefix-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	prefixPath := filepath.Join(tmpDir, prefixID+".mp3")
	combinedPath := filepath.Join(tmpDir, prefixID+"-combined"+filepath.Ext(ttsAudioPath))

	// Synthesize "[username] 说" using edge-tts
	prefixText := fmt.Sprintf("%s 说", username)
	args := []string{
		"--voice", "zh-CN-XiaoxiaoNeural",
		"--text", prefixText,
		"--write-media", prefixPath,
	}
	if err := exec.Command("edge-tts", args...).Run(); err != nil {
		os.Remove(prefixPath)
		return "", fmt.Errorf("edge-tts prefix synthesis failed: %w", err)
	}
	defer os.Remove(prefixPath)

	// Concatenate prefix + TTS audio using ffmpeg concat demuxer
	listPath := filepath.Join(tmpDir, prefixID+"-list.txt")
	listContent := fmt.Sprintf("file '%s'\nfile '%s'\n", prefixPath, ttsAudioPath)
	if err := os.WriteFile(listPath, []byte(listContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write concat list: %w", err)
	}
	defer os.Remove(listPath)

	concatArgs := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-c", "copy",
		combinedPath,
	}
	if err := exec.Command("ffmpeg", concatArgs...).Run(); err != nil {
		os.Remove(combinedPath)
		return "", fmt.Errorf("ffmpeg concat failed: %w", err)
	}

	return combinedPath, nil
}

// broadcastChannelID returns the bot's configured output channel ID, if any.
// The value comes from bot.OutputRoomID (database column), which semantically
// is a channel ID used by the frontend's "subscribe" WS message to register
// in the hub's channels map. The field name "OutputRoomID" is historical.
func (p *BotPlayer) broadcastChannelID() string {
	var bot model.Bot
	if err := p.db.Where("id = ?", p.botID).First(&bot).Error; err != nil {
		return ""
	}
	if bot.OutputRoomID != nil {
		return *bot.OutputRoomID
	}
	return ""
}

// broadcastState pushes current state to WebSocket subscribers.
// payload.channelId is the bot's output channel ID (consumed by both web and
// win clients to filter events). payload.status carries the player state.
// Uses BroadcastToChannel (not BroadcastToRoom) because the frontend subscribes
// to bot events via the "subscribe" WS message, which registers in the channel
// map (c.channels), not the room map (c.rooms).
func (p *BotPlayer) broadcastState() {
	if p.hub == nil {
		return
	}
	channelID := p.broadcastChannelID()
	if channelID == "" {
		return
	}
	status := p.Status()
	data, _ := json.Marshal(map[string]interface{}{
		"type": "bot_state_update",
		"payload": map[string]interface{}{
			"botId":     p.botID,
			"channelId": channelID,
			"status":    status,
		},
	})
	p.hub.BroadcastToChannel(channelID, data)
}

// BroadcastExternalState pushes an externally-provided status map (e.g., from
// the Node.js music bot worker) to WebSocket subscribers. Used in Worker mode
// where the BotPlayer itself has no real playback state to report.
func (p *BotPlayer) BroadcastExternalState(status map[string]interface{}) {
	if p.hub == nil {
		return
	}
	channelID := p.broadcastChannelID()
	if channelID == "" {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type": "bot_state_update",
		"payload": map[string]interface{}{
			"botId":     p.botID,
			"channelId": channelID,
			"status":    status,
		},
	})
	p.hub.BroadcastToChannel(channelID, data)
}

// broadcastQueue pushes queue update to WebSocket subscribers.
// payload.channelId is the bot's output channel ID; payload.items is the queue.
// Uses BroadcastToChannel to match the frontend's "subscribe" registration.
func (p *BotPlayer) broadcastQueue(items []model.BotPlayQueue) {
	if p.hub == nil {
		return
	}
	channelID := p.broadcastChannelID()
	if channelID == "" {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type": "bot_queue_update",
		"payload": map[string]interface{}{
			"botId":     p.botID,
			"channelId": channelID,
			"items":     items,
		},
	})
	p.hub.BroadcastToChannel(channelID, data)
}

// broadcastVolumeChanged pushes volume change event to WebSocket subscribers.
// Uses BroadcastToChannel to match the frontend's "subscribe" registration.
func (p *BotPlayer) broadcastVolumeChanged() {
	if p.hub == nil {
		return
	}
	channelID := p.broadcastChannelID()
	if channelID == "" {
		return
	}
	p.mu.RLock()
	vol := p.volume
	p.mu.RUnlock()
	data, _ := json.Marshal(map[string]interface{}{
		"type": "bot_volume_changed",
		"payload": map[string]interface{}{
			"botId":     p.botID,
			"channelId": channelID,
			"volume":    vol,
		},
	})
	p.hub.BroadcastToChannel(channelID, data)
}

// convertToOpus converts audio to OGG Opus using FFmpeg
func (p *BotPlayer) convertToOpus(inputURL, outputPath string, seekSeconds int) error {
	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
	}
	if seekSeconds > 0 {
		args = append(args, "-ss", strconv.Itoa(seekSeconds))
	}
	args = append(args,
		"-i", inputURL,
		"-vn", // 跳过视频流（封面图），只保留音频流，避免 OGG 文件包含 theora 流导致 scanOggPackets 找不到 OpusHead
		"-c:a", "libopus",
		"-b:a", opusBitrate,
		"-ar", strconv.Itoa(opusSampleRate),
		"-ac", strconv.Itoa(opusChannels),
		"-page_duration", "20000", // 20ms per page = 1 opus packet per page
		"-f", "ogg",
		outputPath,
	)

	cmd := exec.Command(ffmpegPath(p.cfg), args...)
	p.mu.Lock()
	p.ffmpegCmd = cmd
	p.mu.Unlock()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w, output: %s", err, string(output))
	}
	return nil
}

// scanOggPackets scans an OGG Opus file and returns packet offsets without loading data
func scanOggPackets(path string) (*oggTrackReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	// Helper: read an OGG page, optionally returning its payload
	readPage := func(needPayload bool) ([]byte, error) {
		header := make([]byte, 27)
		if _, err := io.ReadFull(f, header); err != nil {
			return nil, err
		}
		if string(header[:4]) != "OggS" {
			return nil, fmt.Errorf("invalid OGG page signature")
		}
		segCount := int(header[26])
		sizeBuf := make([]byte, segCount)
		if _, err := io.ReadFull(f, sizeBuf); err != nil {
			return nil, err
		}
		payloadSize := 0
		for _, s := range sizeBuf {
			payloadSize += int(s)
		}
		if !needPayload {
			if _, err := f.Seek(int64(payloadSize), io.SeekCurrent); err != nil {
				return nil, err
			}
			return nil, nil
		}
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(f, payload); err != nil {
			return nil, err
		}
		return payload, nil
	}

	// Skip OpusHead page
	payload, err := readPage(true)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to read OpusHead: %w", err)
	}
	if !isOpusHeader(payload) {
		f.Close()
		return nil, fmt.Errorf("missing OpusHead")
	}

	// Skip OpusTags page
	payload, err = readPage(true)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to read OpusTags: %w", err)
	}
	if !isOpusTags(payload) {
		f.Close()
		return nil, fmt.Errorf("missing OpusTags")
	}

	// Scan remaining audio pages, recording payload offsets
	var packets []oggPacketInfo
	for {
		pageStart, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			break
		}
		header := make([]byte, 27)
		if _, err := io.ReadFull(f, header); err != nil {
			if err == io.EOF {
				break
			}
			continue
		}
		if string(header[:4]) != "OggS" {
			break
		}
		segCount := int(header[26])
		sizeBuf := make([]byte, segCount)
		if _, err := io.ReadFull(f, sizeBuf); err != nil {
			break
		}
		payloadSize := 0
		for _, s := range sizeBuf {
			payloadSize += int(s)
		}
		if payloadSize > 0 {
			payloadOffset := pageStart + 27 + int64(segCount)
			packets = append(packets, oggPacketInfo{offset: payloadOffset, size: payloadSize})
		}
		// Skip payload bytes
		if _, err := f.Seek(int64(payloadSize), io.SeekCurrent); err != nil {
			break
		}
	}

	return &oggTrackReader{file: f, packets: packets}, nil
}

func isOpusHeader(payload []byte) bool {
	return len(payload) >= 8 && string(payload[:8]) == "OpusHead"
}

func isOpusTags(payload []byte) bool {
	return len(payload) >= 8 && string(payload[:8]) == "OpusTags"
}

// playLoop writes opus packets to LiveKit track
func (p *BotPlayer) playLoop() {
	defer p.wg.Done()

	// Capture generation so we exit if a newer playLoop starts
	p.mu.RLock()
	track := p.track
	reader := p.oggReader
	gen := p.playLoopGen
	p.mu.RUnlock()

	if track == nil || reader == nil || reader.Len() == 0 {
		return
	}

	startTime := time.Now()
	lastBroadcast := startTime

	for {
		select {
		case <-p.stopCh:
			return
		default:
		}

		// Exit if a newer playLoop (from Seek/Play) takes over
		if gen != p.playLoopGen {
			return
		}

		p.mu.RLock()
		paused := p.paused
		idx := p.packetIndex
		offset := p.seekOffset
		p.mu.RUnlock()

		currentSec := offset + idx/50
		p.mu.Lock()
		if p.currentTime != currentSec {
			p.currentTime = currentSec
		}
		p.mu.Unlock()

		if paused {
			// Wait until resumed
			for {
				select {
				case <-p.stopCh:
					return
				default:
				}
				time.Sleep(50 * time.Millisecond)
				p.mu.RLock()
				stillPaused := p.paused
				p.mu.RUnlock()
				if !stillPaused {
					break
				}
			}
			// Recalculate startTime
			startTime = time.Now().Add(-time.Duration(idx) * opusFrameDuration)
			p.broadcastState()
			continue
		}

		if idx >= reader.Len() {
			// Track ended — 清理播放状态，但保留 currentTrack 供 playNext 识别
			// （playNext 需要通过 currentTrack 判断已播完的曲目并从队列删除）
			p.mu.Lock()
			p.playing = false
			p.paused = false
			p.currentTime = 0
			// 注意：不清理 currentTrack，否则 playNext 无法识别当前曲目
			p.currentURL = ""
			if p.oggReader != nil {
				p.oggReader.Close()
				p.oggReader = nil
			}
			if p.oggFilePath != "" {
				os.Remove(p.oggFilePath)
				p.oggFilePath = ""
			}
			// 关闭 stopCh，避免后续 Stop() 的竞态（旧 playLoop 已退出，
			// 但 stopCh 仍 open，会让 Stop()->signalStop() 误以为有 goroutine 在监听）
			select {
			case <-p.stopCh:
				// already closed
			default:
				close(p.stopCh)
			}
			p.mu.Unlock()
			p.saveState()
			p.broadcastState()

			if p.onComplete != nil {
				// 异步调用：同步调用会死锁，因为 onComplete -> playNext ->
				// Play() -> Stop() -> wg.Wait() 会等待当前 playLoop 退出，
				// 而当前 playLoop 卡在 onComplete 调用上无法退出。
				go p.onComplete()
			}
			return
		}

		// Calculate when this packet should be sent
		targetTime := startTime.Add(time.Duration(idx) * opusFrameDuration)
		now := time.Now()
		if now.Before(targetTime) {
			sleepDuration := targetTime.Sub(now)
			timer := time.NewTimer(sleepDuration)
			select {
			case <-p.stopCh:
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		// Read packet on demand from file
		pkt, err := reader.ReadPacket(idx)
		if err != nil {
			p.logger.Errorw("failed to read opus packet", err)
			return
		}

		// M18: Apply real volume scaling (decode → scale PCM → re-encode).
		// The effective coefficient combines the user-set volume and the
		// ducking multiplier (lowered to 30% during TTS playback).
		p.mu.RLock()
		coeff := float32(p.volume) * p.duckMultiplier / 100.0
		p.mu.RUnlock()
		if scaled, err := p.scaleOpusVolume(pkt, coeff); err == nil {
			pkt = scaled
		} else {
			p.logger.Warnw("opus volume scaling failed, using original packet", err)
		}

		sample := media.Sample{
			Data:     pkt,
			Duration: opusFrameDuration,
		}

		if err := track.WriteSample(sample, nil); err != nil {
			p.logger.Errorw("failed to write sample", err)
			return
		}

		p.mu.Lock()
		p.packetIndex = idx + 1
		p.mu.Unlock()

		// Periodic state broadcast
		if time.Since(lastBroadcast) > broadcastInterval {
			p.broadcastState()
			lastBroadcast = time.Now()
		}
	}
}

// saveState persists player state to database
func (p *BotPlayer) saveState() {
	p.mu.RLock()
	state := model.BotPlayerState{
		BotID:          p.botID,
		CurrentTrackID: "",
		Playing:        p.playing,
		Paused:         p.paused,
		CurrentTime:    p.currentTime,
		Volume:         p.volume,
		PlayMode:       p.playMode,
		UpdatedAt:      time.Now(),
	}
	if p.currentTrack != nil {
		state.CurrentTrackID = p.currentTrack.ID
	}
	p.mu.RUnlock()

	if err := p.db.Save(&state).Error; err != nil {
		p.logger.Errorw("failed to save player state", err)
	}
}
