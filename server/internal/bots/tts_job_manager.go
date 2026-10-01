// Package bots: tts_job_manager.go
//
// TTS 异步任务状态管理（参考 Replicate Prediction lifecycle + BullMQ Job 状态机）
//
// 设计目标：
// - 让前端实时感知 TTS 任务进度（pending → processing → succeeded/failed）
// - WS 断线时前端可通过 GET /api/v1/bots/tts/:jobId 兜底查询
// - 内存存储 + TTL 5min 自动清理，避免泄漏（参考 Replicate data retention）
//
// 状态枚举（精简版 4 态，对齐 Replicate 但去掉本项目不需要的 canceled/aborted/starting）：
//   pending    — 已入队，未开始合成
//   processing — 正在 Sherpa-ONNX 合成或 ffmpeg 后处理
//   succeeded  — 推流完成，用户已听到声音
//   failed     — 合成失败 / 推流失败 / 模型未配置
//
// 与现有 BotTTSMessage 表的关系：
// - BotTTSMessage 是持久化记录（DB），存储 TTS 元数据 + 文件路径，TTL 7 天
// - TTSJob 是内存临时状态（sync.Map），存储任务进度 + error，TTL 5 分钟
// 两者通过 outputID（jobId）关联：DB 记录用 jobID 主键，内存状态用同样的 jobID
//
// 参考：
// - Replicate Prediction lifecycle: https://replicate.com/docs/topics/predictions/lifecycle
// - BullMQ Job: https://docs.bullmq.io/
package bots

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"ridgericetalk/internal/realtime"
)

// TTSJobStatus 表示 TTS 任务的状态枚举
type TTSJobStatus string

const (
	TTSStatusPending    TTSJobStatus = "pending"
	TTSStatusProcessing TTSJobStatus = "processing"
	TTSStatusSucceeded  TTSJobStatus = "succeeded"
	TTSStatusFailed     TTSJobStatus = "failed"
)

// TTSErrorCode 表示 TTS 失败的错误码（前端据此决定是否显示重试按钮）
type TTSErrorCode string

const (
	// TTSErrNotConfigured: Sherpa-ONNX 模型未部署（retryable=false）
	TTSErrNotConfigured TTSErrorCode = "BOT_TTS_NOT_CONFIGURED"
	// TTSErrSynthesisFailed: Sherpa-ONNX 合成失败（retryable=true，可能是临时故障）
	TTSErrSynthesisFailed TTSErrorCode = "BOT_TTS_SYNTHESIS_FAILED"
	// TTSErrFFmpegFailed: ffmpeg 后处理失败（retryable=true）
	TTSErrFFmpegFailed TTSErrorCode = "BOT_TTS_FFMPEG_FAILED"
	// TTSErrLiveKitFailed: LiveKit 推流失败（retryable=true，可能是网络抖动）
	TTSErrLiveKitFailed TTSErrorCode = "BOT_TTS_LIVEKIT_FAILED"
	// TTSErrPanic: goroutine panic（retryable=true）
	TTSErrPanic TTSErrorCode = "BOT_TTS_PANIC"
)

// TTSError 表示 TTS 失败的错误信息（参考 Replicate error 对象结构）
type TTSError struct {
	Code      TTSErrorCode `json:"code"`
	Message   string       `json:"message"`
	Retryable bool         `json:"retryable"`
}

// TTSJob 表示一个 TTS 任务的状态快照
//
// 参考 Replicate Prediction 对象：jobId / status / input / output / error / progress / timestamps
type TTSJob struct {
	JobID      string        `json:"jobId"`
	ChannelID  string        `json:"channelId"`
	UserID     string        `json:"userId"`
	Status     TTSJobStatus  `json:"status"`
	Progress   int           `json:"progress"`
	Error      *TTSError     `json:"error,omitempty"`
	CreatedAt  time.Time     `json:"createdAt"`
	UpdatedAt  time.Time     `json:"updatedAt"`
	ExpiresAt  time.Time     `json:"expiresAt"`
}

// ttsJobManager 管理 TTS 任务的内存状态
//
// 设计要点：
// - 使用 sync.Map 并发安全（多个 goroutine 同时读写）
// - TTL 5 分钟自动清理：避免内存泄漏，参考 Replicate data retention
// - 启动一个后台 goroutine 定期清理过期 job
type ttsJobManager struct {
	jobs    sync.Map // map[string]*TTSJob（jobID → job）
	stopCh  chan struct{}
	stopped bool
	mu      sync.Mutex // 保护 stopped 标志
}

// globalTTSJobManager 是全局单例（与 BotManager 设计一致）
//
// 为什么用全局单例而非 Service 字段：
// - Service 在测试中可能被多次创建，导致状态分散
// - TTS job 跨多个 Service 实例共享（理论上不会发生，但防御性设计）
// - 与 BotManager 的单例模式保持一致
var globalTTSJobManager = newTTSJobManager()

// newTTSJobManager 创建一个新的 TTS job manager 并启动清理 goroutine
func newTTSJobManager() *ttsJobManager {
	m := &ttsJobManager{
		stopCh: make(chan struct{}),
	}
	go m.cleanupLoop()
	return m
}

// cleanupLoop 定期清理过期的 TTS job（每 1 分钟检查一次）
//
// TTL 5 分钟：TTS 任务最长 15 秒，5 分钟足够前端兜底查询。
// 超过 5 分钟仍未清理的 job 一定是异常状态（如 goroutine 卡死），可安全清理。
func (m *ttsJobManager) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			m.jobs.Range(func(key, value interface{}) bool {
				job, ok := value.(*TTSJob)
				if !ok || now.After(job.ExpiresAt) {
					m.jobs.Delete(key)
				}
				return true
			})
		}
	}
}

// Stop 停止清理 goroutine（仅在服务关闭时调用）
func (m *ttsJobManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.stopped = true
	close(m.stopCh)
}

// SetStatus 更新 TTS job 状态（不存在则创建）
//
// 调用时机：
// - SynthesizeTTS 入口：SetStatus(pending)
// - goroutine 开始合成：SetStatus(processing)
// - 合成/推流成功：SetStatus(succeeded)
// - 任何阶段失败：SetStatus(failed) + error
func (m *ttsJobManager) SetStatus(jobID, channelID, userID string, status TTSJobStatus, progress int, ttsErr *TTSError) *TTSJob {
	now := time.Now()
	job := &TTSJob{
		JobID:     jobID,
		ChannelID: channelID,
		UserID:    userID,
		Status:    status,
		Progress:  progress,
		Error:     ttsErr,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(5 * time.Minute),
	}
	// 保留原 CreatedAt（若已存在）
	if existing, ok := m.jobs.Load(jobID); ok {
		if existingJob, ok := existing.(*TTSJob); ok {
			job.CreatedAt = existingJob.CreatedAt
		}
	}
	m.jobs.Store(jobID, job)
	return job
}

// GetStatus 查询 TTS job 状态（兜底查询用）
//
// 返回 nil 表示 job 不存在（已过期或从未创建）
func (m *ttsJobManager) GetStatus(jobID string) *TTSJob {
	if v, ok := m.jobs.Load(jobID); ok {
		if job, ok := v.(*TTSJob); ok {
			return job
		}
	}
	return nil
}

// broadcastTTSState 通过 WS 推送 TTS 状态变更给频道内所有客户端
//
// 事件命名：tts_state_update（与 bot_state_update 命名风格一致）
//
// 为什么不扩展 bot_state_update 为通用 task_state_update：
// - TTS 与音乐播放器是不同业务域，混用一个事件会让前端 handler 难以维护
// - 参考 BullMQ 也是按队列分事件流
//
// payload 结构（参考修复方案 §3.4）：
//
//	{
//	  "type": "tts_state_update",
//	  "payload": {
//	    "jobId": "uuid-v4",
//	    "channelId": "ch_xxx",
//	    "userId": "u_xxx",
//	    "status": "failed",
//	    "error": { "code": "BOT_TTS_NOT_CONFIGURED", "message": "...", "retryable": false },
//	    "progress": 100,
//	    "timestamp": "2026-07-29T10:00:02Z"
//	  }
//	}
func (m *ttsJobManager) broadcastTTSState(hub *realtime.Hub, job *TTSJob) {
	if hub == nil || job == nil || job.ChannelID == "" {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type": "tts_state_update",
		"payload": map[string]interface{}{
			"jobId":     job.JobID,
			"channelId": job.ChannelID,
			"userId":    job.UserID,
			"status":    string(job.Status),
			"error":     job.Error,
			"progress":  job.Progress,
			"timestamp": job.UpdatedAt.UTC().Format(time.RFC3339),
		},
	})
	hub.BroadcastToChannel(job.ChannelID, data)
}

// classifyTTSError 根据 SynthesizeText / ffmpeg / PlayTTS 错误分类错误码
//
// 分类规则（参考修复方案 §3.7）：
// - "model not found" / "engine not initialized" → BOT_TTS_NOT_CONFIGURED (retryable=false)
// - Sherpa-ONNX 合成错误 → BOT_TTS_SYNTHESIS_FAILED (retryable=true)
// - ffmpeg 错误 → BOT_TTS_FFMPEG_FAILED (retryable=true)
// - LiveKit 推流错误 → BOT_TTS_LIVEKIT_FAILED (retryable=true)
// - panic → BOT_TTS_PANIC (retryable=true)
// - 默认 → BOT_TTS_SYNTHESIS_FAILED (retryable=true)
func classifyTTSError(stage string, err error) *TTSError {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch stage {
	case "synthesize":
		// 检测模型未配置类错误
		if contains(msg, "model not found", "engine not initialized", "no model loaded", "initTTS") {
			return &TTSError{
				Code:      TTSErrNotConfigured,
				Message:   "TTS 引擎未配置（Sherpa-ONNX 模型未部署），请联系管理员",
				Retryable: false,
			}
		}
		return &TTSError{
			Code:      TTSErrSynthesisFailed,
			Message:   "TTS 语音合成失败：" + msg,
			Retryable: true,
		}
	case "ffmpeg":
		return &TTSError{
			Code:      TTSErrFFmpegFailed,
			Message:   "TTS 音频后处理失败：" + msg,
			Retryable: true,
		}
	case "livekit":
		return &TTSError{
			Code:      TTSErrLiveKitFailed,
			Message:   "TTS 推流失败：" + msg,
			Retryable: true,
		}
	case "panic":
		return &TTSError{
			Code:      TTSErrPanic,
			Message:   "TTS 内部错误（panic）：" + msg,
			Retryable: true,
		}
	default:
		return &TTSError{
			Code:      TTSErrSynthesisFailed,
			Message:   "TTS 处理失败：" + msg,
			Retryable: true,
		}
	}
}

// contains 检查 s 是否包含任意 substring（不区分大小写）
func contains(s string, substrs ...string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
