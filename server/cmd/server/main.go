package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/server"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		exitWithPause(1)
	}

	// 启动阶段的内嵌依赖下载（LiveKit/FFmpeg）需要一个可取消的根上下文：
	// Ctrl+C/终止信号到达时立即中止下载，而不是留下无上限的挂起请求。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Create and wire the application. The logger's Fatal path uses the same
	// console-aware exit function as before.
	app, err := server.NewApp(ctx, cfg, exitWithPause)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create app: %v\n", err)
		exitWithPause(1)
	}

	// If double-click launched, print startup info banner after server is ready.
	app.PrintStartupInfo(ownConsole)

	// Run blocks until a shutdown signal is received.
	if err := app.Run(); err != nil {
		app.LogError("application exited with error", "error", err)
		exitWithPause(1)
	}
}
