// Package bots: player_worker.go
// 阶段 3：Go 服务端通过 HTTP 调用 Node.js Agent Worker 的播放控制接口
//
// 设计：
// - callWorker：通用 HTTP POST/GET 客户端，封装鉴权与错误处理
// - startPlaybackWithWorker：构造 PlayRequest 并调用 Worker /play 接口
// - stopPlaybackWithWorker：调用 Worker /stop 接口
// - getWorkerStatus：查询 Worker /status 接口
// - pauseWithWorker / resumeWithWorker / seekWithWorker / setVolumeWithWorker：阶段 3 新增
//
// 与 Worker 的接口契约见 music-bot-worker/src/types/botSession.ts

package bots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
)

// workerPlayTrack 是 PlayRequest.track 字段的 Go 镜像
// 字段名使用 json tag 与 Worker TypeScript 接口对齐
type workerPlayTrack struct {
	ID      string `json:"id"`
	TrackID string `json:"trackId"`
	Title   string `json:"title"`
	Artist  string `json:"artist"`
	Duration int   `json:"duration"`
	Cover   string `json:"cover"`
	Album   string `json:"album"`
	Source  string `json:"source"`
}

// workerPlayRequest 对应 Worker PlayRequest
type workerPlayRequest struct {
	Track     workerPlayTrack `json:"track"`
	AudioURL  string          `json:"audioUrl"`
	RoomName  string          `json:"roomName"`
	ChannelID string          `json:"channelId"`
	Volume    int             `json:"volume,omitempty"`
	PlayMode  string          `json:"playMode,omitempty"`
}

// workerStopRequest 对应 Worker StopRequest
type workerStopRequest struct {
	Disconnect bool `json:"disconnect,omitempty"`
}

// workerSeekRequest 对应 Worker SeekRequest
type workerSeekRequest struct {
	Position int `json:"position"`
}

// workerSetVolumeRequest 对应 Worker SetVolumeRequest
type workerSetVolumeRequest struct {
	Volume int `json:"volume"`
}

// workerStatusResponse 对应 Worker StatusResponse
type workerStatusResponse struct {
	Playing      bool             `json:"playing"`
	Paused       bool             `json:"paused"`
	CurrentTime  int              `json:"currentTime"`
	Volume       int              `json:"volume"`
	PlayMode     string           `json:"playMode"`
	CurrentTrack *workerPlayTrack `json:"currentTrack"`
}

// workerPauseResult 对应 Worker PauseResult
//
// Worker /pause 接口返回的 no-op 信号（参考 Jellyfin SyncPlay prevState.Equals(Type) 模式）：
// - Noop=true：no-op（重复暂停/未在播放/会话不存在），Go 端应跳过 broadcastWorkerState
// - Noop=false：成功暂停，Go 端应调用 broadcastWorkerState 推送给前端
// - CurrentState：Worker 当前状态快照，可据此校正本地缓存（参考 Navidrome out-of-order 守卫备份模式）
type workerPauseResult struct {
	Noop         bool                  `json:"noop"`
	Reason       string                `json:"reason"`
	CurrentState *workerStatusResponse `json:"currentState"`
}

// 共享 HTTP 客户端（连接池复用）
var workerHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		// Worker 仅本机访问，keep-alive 安全
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	},
}

// workerBaseURL 构造 Worker API 基地址
func workerBaseURL(cfg *config.Config) string {
	return fmt.Sprintf("http://127.0.0.1:%d/internal/bots", cfg.MusicBotWorkerPort)
}

// callWorkerPOST 调用 Worker 的 POST 接口
func callWorkerPOST(ctx context.Context, cfg *config.Config, botID, action string, body interface{}) error {
	url := fmt.Sprintf("%s/%s/%s", workerBaseURL(cfg), botID, action)

	reqBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.MusicBotWorkerToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.MusicBotWorkerToken)
	}

	resp, err := workerHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("worker %s request failed: %w", action, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker %s returned %d: %s", action, resp.StatusCode, string(respBody))
	}

	return nil
}

// callWorkerPOSTWithResponse 调用 Worker 的 POST 接口并解析 JSON 响应到 out
//
// 用于需要读取 Worker 返回 body 的接口（如 /pause 返回的 PauseResult{noop, reason, currentState}）。
// 大多数 POST 接口不关心响应 body，仍使用 callWorkerPOST 即可。
//
// 与 callWorkerGET 的区别：GET 用于读取资源状态，POST 用于触发动作；这里
// 让 POST 也能读取动作执行后的"结果快照"（参考 Jellyfin SyncPlay CurrentSession 回送模式）。
func callWorkerPOSTWithResponse(ctx context.Context, cfg *config.Config, botID, action string, body interface{}, out interface{}) error {
	url := fmt.Sprintf("%s/%s/%s", workerBaseURL(cfg), botID, action)

	reqBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.MusicBotWorkerToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.MusicBotWorkerToken)
	}

	resp, err := workerHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("worker %s request failed: %w", action, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker %s returned %d: %s", action, resp.StatusCode, string(respBody))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode worker response: %w", err)
	}
	return nil
}

// callWorkerGET 调用 Worker 的 GET 接口并解析响应
func callWorkerGET(ctx context.Context, cfg *config.Config, botID, action string, out interface{}) error {
	url := fmt.Sprintf("%s/%s/%s", workerBaseURL(cfg), botID, action)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if cfg.MusicBotWorkerToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.MusicBotWorkerToken)
	}

	resp, err := workerHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("worker %s request failed: %w", action, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker %s returned %d: %s", action, resp.StatusCode, string(respBody))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode worker response: %w", err)
	}
	return nil
}

// startPlaybackWithWorker 调用 Worker 的 /play 接口开始播放
//
// 调用前需已解析 audioURL（netease 调用 GetNeteaseURL，upload 从 DB 读取 FilePath）。
// 此函数不阻塞 — Worker 收到请求后立即返回，playLoop 在 Worker 内部后台运行。
func (s *Service) startPlaybackWithWorker(botID string, track *model.BotPlayQueue, audioURL string) error {
	cfg := s.cfg

	// 查询 bot 获取 channelID（bot.OutputRoomID 实为 channel ID，见 player_cgo.broadcastChannelID）
	var bot model.Bot
	if err := s.db.Where("id = ?", botID).First(&bot).Error; err != nil {
		return fmt.Errorf("load bot for worker play: %w", err)
	}
	if bot.OutputRoomID == nil || *bot.OutputRoomID == "" {
		return errors.New("bot has no output room configured")
	}
	channelID := *bot.OutputRoomID
	roomName := "rrt-room-" + channelID

	// 读取当前音量与播放模式（与 Go 端 startPlayback 行为一致）
	volume := 80
	playMode := "order"
	var state model.BotPlayerState
	if err := s.db.Where("bot_id = ?", botID).First(&state).Error; err == nil {
		if state.Volume > 0 {
			volume = state.Volume
		}
		if state.PlayMode != "" {
			playMode = state.PlayMode
		}
	}

	req := workerPlayRequest{
		Track: workerPlayTrack{
			ID:       track.ID,
			TrackID:  track.TrackID,
			Title:    track.Title,
			Artist:   track.Artist,
			Duration: track.Duration,
			Cover:    track.Cover,
			Album:    track.Album,
			Source:   track.Source,
		},
		AudioURL:  audioURL,
		RoomName:  roomName,
		ChannelID: channelID,
		Volume:    volume,
		PlayMode:  playMode,
	}

	// 用较长超时（30s）调用 play，因为 Worker 内部需要：
	// 1. 生成 token 2. 连接 LiveKit 3. 发布 track
	// 但 play 不会等待播放完成，只等连接建立
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return callWorkerPOST(ctx, cfg, botID, "play", req)
}

// stopPlaybackWithWorker 调用 Worker 的 /stop 接口停止播放
//
// disconnect=true 时同时断开 Worker 的 LiveKit 连接（用于 bot 离开频道）。
// 此函数不等待 playLoop 完全退出（Worker 内部最多 2s 退出），立即返回。
func (s *Service) stopPlaybackWithWorker(botID string, disconnect bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return callWorkerPOST(ctx, s.cfg, botID, "stop", workerStopRequest{Disconnect: disconnect})
}

// getWorkerStatus 查询 Worker 的播放状态
//
// 用于 GetStatus 接口：当 UseMusicBotWorker=true 时，从 Worker 获取实时状态。
// 返回的 map 字段与 BotPlayer.Status() 对齐（handler.go 依赖此契约）。
func (s *Service) getWorkerStatus(botID string) (map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var resp workerStatusResponse
	if err := callWorkerGET(ctx, s.cfg, botID, "status", &resp); err != nil {
		return nil, err
	}

	// 转换为与 BotPlayer.Status() 一致的 map 结构
	var track map[string]interface{}
	if resp.CurrentTrack != nil {
		track = map[string]interface{}{
			"id":       resp.CurrentTrack.ID,
			"trackId":  resp.CurrentTrack.TrackID,
			"title":    resp.CurrentTrack.Title,
			"artist":   resp.CurrentTrack.Artist,
			"duration": resp.CurrentTrack.Duration,
			"cover":    resp.CurrentTrack.Cover,
			"album":    resp.CurrentTrack.Album,
			"source":   resp.CurrentTrack.Source,
		}
	}

	return map[string]interface{}{
		"playing":      resp.Playing,
		"paused":       resp.Paused,
		"currentTime":  resp.CurrentTime,
		"volume":       resp.Volume,
		"playMode":     resp.PlayMode,
		"currentTrack": track,
	}, nil
}

// ===== 阶段 3：Pause / Resume / Seek / SetVolume =====

// pauseWithWorker 调用 Worker 的 /pause 接口暂停播放
//
// 返回 workerPauseResult 让调用方根据 Noop 字段决定是否广播：
// - Noop=true：Worker 返回 no-op 信号（重复暂停/未在播放/会话不存在），调用方不应广播
//   避免前端收到"暂停态 currentTrack=null"导致 UI 错误清空（参考 Jellyfin PausedGroupState
//   "Client got lost" 注释，no-op 时不广播，但 Worker 仍回送 currentState 供校正）
// - Noop=false：成功暂停，调用方应通过 broadcastWorkerState 推送状态变更给前端
//
// 注意：返回 nil error 仅表示 HTTP 调用成功；Noop 字段表示业务上是否实际变更。
func (s *Service) pauseWithWorker(botID string) (*workerPauseResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Worker 的 pause 是幂等的（botManager.pause 已处理重复调用并返回 no-op 信号）
	var result workerPauseResult
	if err := callWorkerPOSTWithResponse(ctx, s.cfg, botID, "pause", struct{}{}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// resumeWithWorker 调用 Worker 的 /resume 接口恢复播放
func (s *Service) resumeWithWorker(botID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return callWorkerPOST(ctx, s.cfg, botID, "resume", struct{}{})
}

// seekWithWorker 调用 Worker 的 /seek 接口跳转到指定位置
// seconds为目标位置（秒）
func (s *Service) seekWithWorker(botID string, seconds int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// seek 内部需要重启 FFmpeg（最多 2s 等待旧 playLoop 退出），给较长超时
	return callWorkerPOST(ctx, s.cfg, botID, "seek", workerSeekRequest{Position: seconds})
}

// setVolumeWithWorker 调用 Worker 的 /volume 接口设置音量
func (s *Service) setVolumeWithWorker(botID string, volume int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return callWorkerPOST(ctx, s.cfg, botID, "volume", workerSetVolumeRequest{Volume: volume})
}

// ===== 阶段 4：Skip / Previous / SetPlayMode / UpdateQueue / Disconnect =====

// workerSkipRequest 对应 Worker SkipRequest
type workerSkipRequest struct {
	IsUserSkip bool `json:"isUserSkip"`
}

// workerSetPlayModeRequest 对应 Worker SetPlayModeRequest
type workerSetPlayModeRequest struct {
	Mode      string `json:"mode"`
	ChannelID string `json:"channelId,omitempty"`
	RoomName  string `json:"roomName,omitempty"`
}

// skipWithWorker 调用 Worker 的 /skip 接口切换到下一首
//
// isUserSkip 区分用户主动点击（true）与自然播放结束（false）：
// - 用户主动点击：repeat-one 模式下也切换到下一首
// - 自然播放结束：repeat-one 模式下重复当前曲目
//
// 注意：Worker 模式下 playNext 由 Worker 自主处理，Go 端仅转发请求。
// Worker 内部维护队列与播放历史，无需 Go 端提供上下文。
func (s *Service) skipWithWorker(botID string, isUserSkip bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// skip 内部可能触发 play（连接 LiveKit + 解码），给较长超时
	return callWorkerPOST(ctx, s.cfg, botID, "skip", workerSkipRequest{IsUserSkip: isUserSkip})
}

// previousWithWorker 调用 Worker 的 /previous 接口回溯播放历史
func (s *Service) previousWithWorker(botID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return callWorkerPOST(ctx, s.cfg, botID, "previous", struct{}{})
}

// setPlayModeWithWorker 调用 Worker 的 /play-mode 接口设置播放模式
//
// Worker 接收到模式变更后会：
// - 切换到 random 模式时重新生成 shuffle 顺序
// - 离开 random 模式时清空 shuffle 状态
//
// 附带 channelId/roomName：Worker 在 session 不存在时会创建轻量 session，
// 确保 setPlayMode 在 play 之前生效。
func (s *Service) setPlayModeWithWorker(botID, mode string) error {
	// 查询 bot 获取 channelId（用于 Worker 创建 session）
	channelID, roomName := s.getBotChannelInfo(botID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return callWorkerPOST(ctx, s.cfg, botID, "play-mode", workerSetPlayModeRequest{
		Mode:      mode,
		ChannelID: channelID,
		RoomName:  roomName,
	})
}

// updateQueueWithWorker 调用 Worker 的 /queue 接口同步队列
//
// 在以下场景调用：
// - EnqueueUpload / EnqueueNetease：新增曲目到队列
// - RemoveFromQueue：从队列删除曲目
// - ClearQueue：清空队列
// - PlayNow 失败删除曲目
//
// Go 端负责解析每首曲目的 audioUrl 并附带在请求中：
// - upload 类型：从 DB 读取 FilePath
// - netease 类型：留空，由 Worker 在播放时回调 Go 解析（阶段 5 实现）
//
// 附带 channelId/roomName：Worker 在 session 不存在时会创建轻量 session，
// 确保队列能被 Worker 缓存，后续 playNext/skip 能读取到正确的队列。
func (s *Service) updateQueueWithWorker(botID string) error {
	// 从 DB 读取最新队列
	var queue []model.BotPlayQueue
	if err := s.db.Where("bot_id = ?", botID).Order("position ASC, created_at ASC").Find(&queue).Error; err != nil {
		return fmt.Errorf("load queue for worker sync: %w", err)
	}

	// 转换为 Worker 请求格式，并解析每首曲目的 audioUrl
	workerQueue := make([]workerPlayTrackWithURL, 0, len(queue))
	for i := range queue {
		track := &queue[i]
		workerQueue = append(workerQueue, workerPlayTrackWithURL{
			ID:       track.ID,
			TrackID:  track.TrackID,
			Title:    track.Title,
			Artist:   track.Artist,
			Duration: track.Duration,
			Cover:    track.Cover,
			Album:    track.Album,
			Source:   track.Source,
			AudioURL: s.resolveAudioURLForTrack(track),
		})
	}

	// 查询 bot 获取 channelId（用于 Worker 创建 session）
	channelID, roomName := s.getBotChannelInfo(botID)

	reqBody := struct {
		Queue     []workerPlayTrackWithURL `json:"queue"`
		ChannelID string                   `json:"channelId,omitempty"`
		RoomName  string                   `json:"roomName,omitempty"`
	}{
		Queue:     workerQueue,
		ChannelID: channelID,
		RoomName:  roomName,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return callWorkerPOST(ctx, s.cfg, botID, "queue", reqBody)
}

// getBotChannelInfo 查询 bot 的 channelId 和 roomName
//
// 用于 Worker 接口请求中附带频道信息，让 Worker 在 session 不存在时
// 能创建轻量 session（保存队列和播放模式状态）。
//
// 返回值：
// - channelID：bot 绑定的语音频道 ID（bot.OutputRoomID）
// - roomName：LiveKit 房间名（rrt-room-<channelID>）
//
// 若查询失败或 bot 未绑定频道，返回空字符串（Worker 会跳过 session 创建）。
func (s *Service) getBotChannelInfo(botID string) (channelID, roomName string) {
	var bot model.Bot
	if err := s.db.Where("id = ?", botID).First(&bot).Error; err != nil {
		return "", ""
	}
	if bot.OutputRoomID == nil || *bot.OutputRoomID == "" {
		return "", ""
	}
	channelID = *bot.OutputRoomID
	roomName = "rrt-room-" + channelID
	return channelID, roomName
}

// workerPlayTrackWithURL 在 workerPlayTrack 基础上新增 audioUrl 字段
// 用于队列同步时附带每首曲目的音频 URL
type workerPlayTrackWithURL struct {
	ID       string `json:"id"`
	TrackID  string `json:"trackId"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Duration int    `json:"duration"`
	Cover    string `json:"cover"`
	Album    string `json:"album"`
	Source   string `json:"source"`
	AudioURL string `json:"audioUrl,omitempty"`
}

// resolveAudioURLForTrack 解析曲目的音频 URL
//
// - upload 类型：从 DB 读取 FilePath，拼接 LocalDataPath 得到绝对路径
//   Worker 运行在独立进程，必须提供绝对路径才能找到音频文件
// - netease 类型：留空（Worker 播放时回调 Go 解析，阶段 5 实现）
// - tts 类型：留空（TTS 通常通过专用接口播放，不走队列）
func (s *Service) resolveAudioURLForTrack(track *model.BotPlayQueue) string {
	switch track.Source {
	case "upload":
		// 从 DB 读取上传记录的 FilePath（相对路径，如 uploads/audio/xxx.mp3）
		var upload model.BotUploadAudio
		if err := s.db.Where("id = ?", track.TrackID).First(&upload).Error; err == nil {
			// 拼接 LocalDataPath 得到绝对路径
			// Worker 运行在独立进程，工作目录可能不同，必须提供绝对路径
			return filepath.Join(s.cfg.LocalDataPath, upload.FilePath)
		}
		return ""
	default:
		// netease / tts 类型：Worker 无法自主解析，留空
		return ""
	}
}

// disconnectWithWorker 调用 Worker 的 /disconnect 接口断开 LiveKit 连接
func (s *Service) disconnectWithWorker(botID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return callWorkerPOST(ctx, s.cfg, botID, "disconnect", struct{}{})
}
