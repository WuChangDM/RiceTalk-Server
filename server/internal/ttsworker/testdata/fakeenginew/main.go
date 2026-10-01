// Package main: internal/ttsworker/testdata/fakeenginew
//
// 测试用假 TTS worker（仅供 manager_test.go 通过 go build 编译为子进程，
// testdata 目录不参与正常包构建）。
//
// 通过环境变量 FAKE_WORKER_MODE 切换行为：
//   - echo（默认）       ：正常引擎。info 回 (16000, 1)；synth 把 text 写入
//     wavPath 后回 ok——用于验证请求-响应、并发 id 配对。
//   - exit-immediately   ：启动即退出 9——模拟「模型加载失败/损坏 → worker
//     崩溃退出非 0」，用于验证 spawn 失败与退避。
//   - crash-on-2         ：第 2 个合成请求不响应直接退出 3——模拟 sherpa cgo
//     SIGABRT，用于验证崩溃检测、透明自愈（同步拉起重试）。
//   - crash-always       ：每个合成请求都不响应直接退出 3——模拟持续崩溃，
//     用于验证 pending 请求失败（错误文本族）、自愈重试也有界、退避生效。
//   - sleep              ：合成请求睡 60s——模拟引擎卡死，用于验证请求超时
//     降级与超时杀进程自愈。
package main

import (
	"log"
	"os"
	"time"

	"ridgericetalk/internal/ttsworker"
)

func main() {
	mode := os.Getenv("FAKE_WORKER_MODE")
	if mode == "exit-immediately" {
		// 模拟模型加载阶段崩溃：主进程管理器应看到 spawn/握手失败
		os.Exit(9)
	}
	logger := log.New(os.Stderr, "[fake-tts-worker] ", 0)
	eng := &fakeEngine{mode: mode}
	defer eng.Close()
	if err := ttsworker.ServeEngine(eng, os.Stdin, os.Stdout, logger); err != nil {
		logger.Printf("serve error: %v", err)
		os.Exit(1)
	}
}

// fakeEngine 是 ttsworker.Engine 的测试桩。
type fakeEngine struct {
	mode   string
	synths int
}

// Info 实现 Engine.Info（echo/crash-on-2/sleep 均报告就绪）。
func (e *fakeEngine) Info() (int32, int32, error) {
	return 16000, 1, nil
}

// Synthesize 实现 Engine.Synthesize（按模式注入异常行为）。
func (e *fakeEngine) Synthesize(text, wavPath string, speed float64, sid int32) (int32, error) {
	e.synths++
	switch e.mode {
	case "crash-on-2":
		if e.synths >= 2 {
			// 模拟 cgo SIGABRT：不写响应，进程直接死亡
			os.Exit(3)
		}
	case "crash-always":
		os.Exit(3)
	case "sleep":
		time.Sleep(60 * time.Second)
	}
	if err := os.WriteFile(wavPath, []byte(text), 0o644); err != nil {
		return 0, err
	}
	return 16000, nil
}

// Close 实现 Engine.Close（测试桩无资源）。
func (e *fakeEngine) Close() {}
