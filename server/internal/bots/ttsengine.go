// Package bots: ttsengine.go
//
// TTS 引擎门面（known_issues N16 修复）：sherpa-onnx / cgo 调用全部隔离到
// 独立 worker 子进程（cmd/tts-worker），本文件只是主进程侧的薄封装。
//
// 缺陷背景：旧实现（ttsengine_cgo.go）把 sherpa 引擎直接驻留主进程，
// model.onnx 为 134B Git LFS 指针文件时预检（仅 os.IsNotExist）放行，
// sherpa 解析失败抛 C++ Ort::Exception → cgo SIGABRT 不可 recover →
// 整个服务端崩溃循环（systemd 3 次重启后放弃，服务整体不可用）。
//
// 现在的架构：
//
//	NewServiceWithRepos ── goroutine ──→ InitTTS(modelDir)
//	                                        │ 模型预检（缺失/LFS指针/过小 → TTS 禁用，
//	                                        │   错误文本保持 "model not found" 同族）
//	                                        ▼
//	                              ttsworker.Manager（internal/ttsworker）
//	                                        │ spawn / 握手 / 退避重启 / 请求超时
//	                                        ▼
//	                             cmd/tts-worker 子进程（cgo + sherpa 全在这里）
//
// 对外 API（InitTTS / SynthesizeText / TTSSpeakerCount / TTSStatus / ResetTTS）
// 签名与语义与旧实现一致：HTTP/WS API 面、tts_voices 音色发现、
// tts_job_manager.classifyTTSError 的字符串归类（"model not found" /
// "engine not initialized" → BOT_TTS_NOT_CONFIGURED）全部保持兼容。
package bots

import (
	"errors"

	coreerrors "ridgericetalk/core/errors"
	"ridgericetalk/internal/ttsworker"
)

// globalTTSWorker 是全局 worker 管理器单例（与旧 globalTTS 单例一致：
// Service 在测试中可能多次创建，引擎状态必须全局唯一）。
var globalTTSWorker = ttsworker.NewManager(ttsworker.Config{})

// InitTTS 初始化 TTS：校验模型 + 同步拉起 worker（spawn + 握手）。
// 幂等；模型缺失/无效时返回错误并把引擎置为不可用（优雅降级，
// 不启动 worker、绝不崩溃）。错误文本与旧实现同族（含 "model not found"）。
func InitTTS(modelDir string) error {
	return globalTTSWorker.Initialize(modelDir)
}

// SynthesizeText 合成文本到 outputPath（WAV），返回采样率。
// worker 不可用（模型缺失/worker 启动失败/重启中）时返回
// BOT_TTS_NOT_CONFIGURED 错误（与旧实现 "engine not initialized" 同语义）；
// worker 内合成失败透传错误（归类 BOT_TTS_SYNTHESIS_FAILED，可重试）。
func SynthesizeText(text, outputPath string, speed float64, sid int32) (int32, error) {
	sampleRate, err := globalTTSWorker.Synthesize(text, outputPath, speed, sid)
	if err != nil {
		var nre *ttsworker.NotReadyError
		if errors.As(err, &nre) {
			// 保持旧实现行为：不可用类错误以 BOT_TTS_NOT_CONFIGURED 错误码返回
			//（HTTP 503；classifyTTSError 的消息文本同族，双保险）。
			return 0, coreerrors.New(coreerrors.BOT_TTS_NOT_CONFIGURED, err.Error())
		}
		return 0, err
	}
	return sampleRate, nil
}

// TTSSpeakerCount 返回就绪引擎的说话人数（音色能力发现的唯一数据源，
// 无硬编码）。引擎不可用/未初始化返回 0（与旧实现一致）。
func TTSSpeakerCount() int32 {
	return globalTTSWorker.SpeakerCount()
}

// TTSStatus 返回 (ready, modelDir, errText)。
func TTSStatus() (ready bool, model string, err string) {
	return globalTTSWorker.Status()
}

// ResetTTS 销毁当前 worker 并用 modelDir 重新初始化（管理后台重试初始化）。
// 同步返回首个错误（模型无效 / spawn 失败）。
func ResetTTS(modelDir string) error {
	return globalTTSWorker.Reset(modelDir)
}

// CloseTTS 优雅关停 worker 子进程（服务停机时调用，防孤儿进程）。
// 幂等；关停后如再收到 TTS 请求按不可用降级。
func CloseTTS() {
	globalTTSWorker.Close()
}
