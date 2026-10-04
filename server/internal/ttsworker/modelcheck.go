// Package ttsworker: modelcheck.go
//
// TTS 模型预检（known_issues N16 修复）。
//
// 缺陷背景：仓库中 model.onnx 是 Git LFS 管理的大文件，bundle 克隆（不含 LFS
// 对象）落地的是 134 字节 LFS 指针文本；旧实现（ttsengine_cgo.go）预检只做
// os.IsNotExist，指针文件「存在」放行 → sherpa-onnx 解析失败抛 C++ 异常 →
// cgo SIGABRT 全进程崩溃。本文件在 spawn worker 之前（以及 worker 加载之前
// 双保险）做内容级校验：
//   - model.onnx 不存在           → 「TTS model not found; TTS disabled」（与
//     原实现逐字同族，走既有优雅降级路径）
//   - 尺寸 < 阈值（默认 1MB）      → 拒绝（占位/截断/损坏）
//   - 内容以 LFS 指针特征串开头    → 拒绝
//
// 错误消息契约：所有拒绝类错误文本必须包含 "model not found" 子串（与
// tts_job_manager.classifyTTSError 的字符串归类保持兼容，归类为
// BOT_TTS_NOT_CONFIGURED / retryable=false），同时附带具体原因便于运维排查。
package ttsworker

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultMinModelBytes 是 model.onnx 的最小合法体积（1MB）。
// 真实 VITS 模型约 160MB；134B 的 LFS 指针与任何截断/占位文件都远小于该阈值。
const DefaultMinModelBytes int64 = 1 << 20

// lfsPointerPrefix 是 Git LFS 指针文件第一行的固定特征前缀。
// 标准指针第一行形如 "version https://git-lfs.github.com/spec/v1"。
const lfsPointerPrefix = "version https://git-lfs"

// ValidateTTSModel 校验 modelDir 下的 TTS 模型文件是否可信可加载。
// minBytes 为最小合法体积阈值（传 <=0 时使用 DefaultMinModelBytes，测试可注入
// 小阈值以避免生成大文件）。
//
// 返回 nil 表示通过；返回 *NotReadyError 表示模型缺失/无效（TTS 应禁用，
// 不应启动 worker）。
func ValidateTTSModel(modelDir string, minBytes int64) error {
	if minBytes <= 0 {
		minBytes = DefaultMinModelBytes
	}
	modelPath := filepath.Join(modelDir, "model.onnx")
	st, err := os.Stat(modelPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 与原 ttsengine_cgo.go 的降级文案逐字同族（"TTS model not found; TTS disabled"）
			return &NotReadyError{Reason: fmt.Sprintf(
				"TTS model not found; TTS disabled (modelDir=%s)", modelDir)}
		}
		return &NotReadyError{Reason: fmt.Sprintf(
			"TTS model not found or unreadable (%v); TTS disabled (modelDir=%s)", err, modelDir)}
	}
	if st.IsDir() {
		return &NotReadyError{Reason: fmt.Sprintf(
			"TTS model not found or invalid (model.onnx is a directory); TTS disabled (modelDir=%s)", modelDir)}
	}
	if st.Size() < minBytes {
		return &NotReadyError{Reason: fmt.Sprintf(
			"TTS model not found or invalid (size %d bytes < minimum %d bytes, suspected LFS pointer placeholder or corrupted file); TTS disabled (modelDir=%s)",
			st.Size(), minBytes, modelDir)}
	}
	ok, err := isLFSPointerFile(modelPath)
	if err != nil {
		return &NotReadyError{Reason: fmt.Sprintf(
			"TTS model not found or unreadable (%v); TTS disabled (modelDir=%s)", err, modelDir)}
	}
	if ok {
		return &NotReadyError{Reason: fmt.Sprintf(
			"TTS model not found or invalid (model.onnx is a Git LFS pointer placeholder, not the real binary); TTS disabled (modelDir=%s); run 'git lfs pull' or download the model before deploying",
			modelDir)}
	}
	return nil
}

// isLFSPointerFile 判断文件内容是否以 Git LFS 指针特征串开头。
// 只读文件头几十字节，不做全量扫描。
func isLFSPointerFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	head := make([]byte, len(lfsPointerPrefix)+8)
	n, err := br.Read(head)
	if err != nil && n == 0 {
		// 空文件或读失败：空文件由体积阈值拦截，这里不视为指针
		return false, nil
	}
	return strings.HasPrefix(string(head[:n]), lfsPointerPrefix), nil
}
