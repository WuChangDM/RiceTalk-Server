//go:build !cgo

// Package ttsworker: engine_nocgo.go
//
// 无 cgo 构建下的 TTS 引擎 stub（known_issues N16 修复的协议双轨要求）：
// worker 二进制在 CGO_ENABLED=0（本地/CI 无 cgo 工具链）下同样可编译、
// 可运行、可测协议层——它正常起 serve 循环，但对任何请求返回
// ok:false "TTS engine requires CGO"。主进程管理器收到握手失败后按既有
// 「TTS 不可用」路径优雅降级，行为与生产语义一致。
package ttsworker

import "errors"

// errTTSNoCGO 与原 internal/bots/ttsengine_nocgo.go 的错误文本保持一致。
var errTTSNoCGO = errors.New("TTS engine requires CGO (sherpa-onnx), please build with CGO_ENABLED=1 and a C compiler installed")

// stubEngine 是无 cgo 环境的占位引擎。
type stubEngine struct{}

// NewEngine 返回 stub 引擎（不报错：worker 仍需起服务以支撑协议层验证）。
func NewEngine(modelDir string) (Engine, error) {
	return &stubEngine{}, nil
}

// Info 恒返回 errTTSNoCGO。
func (e *stubEngine) Info() (int32, int32, error) { return 0, 0, errTTSNoCGO }

// Synthesize 恒返回 errTTSNoCGO。
func (e *stubEngine) Synthesize(text, wavPath string, speed float64, sid int32) (int32, error) {
	return 0, errTTSNoCGO
}

// Close 无资源需要释放。
func (e *stubEngine) Close() {}
