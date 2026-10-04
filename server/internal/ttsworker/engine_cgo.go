//go:build cgo

// Package ttsworker: engine_cgo.go
//
// sherpa-onnx TTS 引擎实现（仅存在于 worker 子进程，cgo 构建）。
//
// known_issues N16：sherpa-onnx 的 cgo 调用在模型损坏时可能抛 C++
// Ort::Exception → SIGABRT 不可 recover。本文件是把 cgo 隔离到独立进程的
// 唯一落点之一（另一个是 cmd/tts-worker/main.go），主进程（internal/bots）
// 禁止 import sherpa。
//
// 模型配置构建逻辑自原 internal/bots/ttsengine_cgo.go 的 buildVITSTTSConfig
// 原样下沉，行为保持不变。
package ttsworker

import (
	"fmt"
	"path/filepath"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// sherpaEngine 是 Engine 的 sherpa-onnx 实现。
type sherpaEngine struct {
	tts *sherpa.OfflineTts
}

// NewEngine 加载 VITS 模型构建引擎。
// 加载前先做内容级预检（LFS 指针/过小文件直接拒绝），这是 spawn 前主进程
// 预检之后的第二道防线；两层都拦不住的损坏（如 onnx 结构坏但体积正常）
// 才会走到 sherpa 内部——即便如此也只崩 worker 进程，不影响主进程。
func NewEngine(modelDir string) (Engine, error) {
	if err := ValidateTTSModel(modelDir, DefaultMinModelBytes); err != nil {
		return nil, err
	}
	cfg := buildVITSTTSConfig(modelDir)
	tts := sherpa.NewOfflineTts(&cfg)
	if tts == nil {
		return nil, fmt.Errorf("sherpa-onnx: NewOfflineTts returned nil (modelDir=%s)", modelDir)
	}
	return &sherpaEngine{tts: tts}, nil
}

// Info 返回引擎元数据（模型元信息，加载成功后恒可用）。
func (e *sherpaEngine) Info() (int32, int32, error) {
	if e.tts == nil {
		return 0, 0, fmt.Errorf("sherpa-onnx: engine not initialized")
	}
	return int32(e.tts.SampleRate()), int32(e.tts.NumSpeakers()), nil
}

// Synthesize 合成文本并保存为 WAV。
func (e *sherpaEngine) Synthesize(text, wavPath string, speed float64, sid int32) (int32, error) {
	if e.tts == nil {
		return 0, fmt.Errorf("sherpa-onnx: engine not initialized")
	}
	cfg := &sherpa.GenerationConfig{
		Speed: float32(speed),
		Sid:   int(sid),
	}
	audio := e.tts.GenerateWithConfig(text, cfg, nil)
	if audio == nil {
		return 0, fmt.Errorf("sherpa-onnx: GenerateWithConfig returned nil")
	}
	if !audio.Save(wavPath) {
		return 0, fmt.Errorf("sherpa-onnx: Save(%s) failed", wavPath)
	}
	return int32(audio.SampleRate), nil
}

// Close 释放 sherpa 引擎资源。
func (e *sherpaEngine) Close() {
	if e.tts != nil {
		sherpa.DeleteOfflineTts(e.tts)
		e.tts = nil
	}
}

// buildVITSTTSConfig builds a VITS model configuration from a model directory.
// Expected directory layout:
//
//	modelDir/
//	├── model.onnx        (VITS ONNX model, required)
//	├── tokens.txt        (token list, required)
//	└── lexicons/         (lexicon files, optional)
func buildVITSTTSConfig(modelDir string) sherpa.OfflineTtsConfig {
	return sherpa.OfflineTtsConfig{
		Model: sherpa.OfflineTtsModelConfig{
			Vits: sherpa.OfflineTtsVitsModelConfig{
				Model:       filepath.Join(modelDir, "model.onnx"),
				Tokens:      filepath.Join(modelDir, "tokens.txt"),
				Lexicon:     filepath.Join(modelDir, "lexicon.txt"),
				NoiseScale:  0.667,
				NoiseScaleW: 0.8,
				LengthScale: 1.0,
			},
			NumThreads: 1,
			Debug:      0,
			Provider:   "cpu",
		},
		MaxNumSentences: 1,
		SilenceScale:    0.2,
	}
}
