package main

import (
	"fmt"
	"os"

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

	// Create and wire the application. The logger's Fatal path uses the same
	// console-aware exit function as before.
	app, err := server.NewApp(cfg, exitWithPause)
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
