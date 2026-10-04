// Package main: cmd/tts-worker
//
// TTS 引擎独立 worker 进程（known_issues N16 修复）。
//
// 背景：sherpa-onnx 的 cgo 调用在模型损坏（如 Git LFS 指针占位文件）时会抛
// C++ Ort::Exception，cgo 执行中 SIGABRT 不可 recover，旧实现（引擎直接驻留
// 主进程）导致整个 Go 服务端崩溃循环，systemd 3 次快速重启后放弃、服务整体
// 不可用。修复后所有 sherpa-onnx / cgo 调用只存在于本进程：
//
//   - 本进程崩溃 → 主进程管理器（internal/ttsworker.Manager）检测并带退避
//     自动重启，主进程绝不 abort；
//   - 模型加载失败 → 本进程打印 stderr 日志并退出非 0；
//   - stdin EOF（主进程关停）→ 优雅退出 0。
//
// 协议：stdin/stdout 行分隔 JSON（见 internal/ttsworker/protocol.go）。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"ridgericetalk/internal/ttsworker"
)

func main() {
	modelDir := flag.String("model-dir", "", "TTS 模型目录（含 model.onnx / tokens.txt，必需）")
	flag.Parse()

	logger := log.New(os.Stderr, "[tts-worker] ", log.LstdFlags)

	if *modelDir == "" {
		fmt.Fprintln(os.Stderr, "[tts-worker] --model-dir is required")
		os.Exit(2)
	}

	// 加载引擎。失败（模型缺失/无效/损坏）→ 退出非 0，由主进程管理器按
	// 「TTS 不可用」降级，绝不影响主进程。
	eng, err := ttsworker.NewEngine(*modelDir)
	if err != nil {
		logger.Printf("engine init failed: %v", err)
		os.Exit(1)
	}
	defer eng.Close()

	logger.Printf("engine loaded, serving stdin protocol (modelDir=%s)", *modelDir)

	// 主循环：stdin EOF → nil → 优雅退出 0。
	if err := ttsworker.ServeEngine(eng, os.Stdin, os.Stdout, logger); err != nil {
		logger.Printf("serve loop error: %v", err)
		os.Exit(1)
	}
	logger.Printf("stdin closed, exiting gracefully")
}
