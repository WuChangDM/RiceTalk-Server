package main

import (
	"fmt"
	"os"
	"strconv"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/database"
	"ridgericetalk/internal/logger"
)

func usage() {
	fmt.Println(`RidgeRiceTalk Database Migration Tool

Usage:
  go run ./cmd/migrate <command> [args]

Commands:
  up              Apply all pending migrations
  down [N]        Roll back N migrations (default N=1)
  force <V>       Force-set migration version to V (mark clean; for baselining)
  version         Print current migration version and dirty state
  dirty           Mark current migration version as dirty (recovery)

Environment:
  RRT_DATABASE_URL  Database connection string
  RRT_DB_DRIVER     "postgres" (versioned) or "sqlite" (AutoMigrate fallback)
  RRT_LOG_LEVEL     Log level (default: info)

Notes:
  - Versioned migrations require PostgreSQL and the migrations/ directory.
  - SQLite falls back to GORM AutoMigrate (up only; no rollback).
  - To baseline an existing database, run: migrate force 1`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}

	migrationsPath := database.MigrationsDir
	if envPath := os.Getenv("RRT_MIGRATIONS_PATH"); envPath != "" {
		migrationsPath = envPath
	}

	switch cmd {
	case "up":
		if err := database.RunMigrationsUp(cfg.DatabaseURL, cfg.DBDriver, migrationsPath, log); err != nil {
			fmt.Fprintf(os.Stderr, "migration up failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Migrations applied successfully")

	case "down":
		steps := 1
		if len(os.Args) >= 3 {
			if n, err := strconv.Atoi(os.Args[2]); err == nil && n > 0 {
				steps = n
			}
		}
		if err := database.RunMigrationsDown(cfg.DatabaseURL, cfg.DBDriver, migrationsPath, steps, log); err != nil {
			fmt.Fprintf(os.Stderr, "migration down failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Rolled back %d migration(s) successfully\n", steps)

	case "force":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "force requires a version number: migrate force <V>")
			os.Exit(1)
		}
		version, err := strconv.Atoi(os.Args[2])
		if err != nil || version < 0 {
			fmt.Fprintf(os.Stderr, "invalid version: %s\n", os.Args[2])
			os.Exit(1)
		}
		if err := database.ForceMigrationVersion(cfg.DatabaseURL, cfg.DBDriver, migrationsPath, version, log); err != nil {
			fmt.Fprintf(os.Stderr, "force version failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Forced migration version to %d\n", version)

	case "version":
		version, dirty, err := database.GetMigrationVersion(cfg.DatabaseURL, cfg.DBDriver, migrationsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "get version failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Migration version: %d (dirty: %v)\n", version, dirty)

	case "dirty":
		if err := database.MarkMigrationDirty(cfg.DatabaseURL, cfg.DBDriver, migrationsPath, log); err != nil {
			fmt.Fprintf(os.Stderr, "mark dirty failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Current migration version marked as dirty")

	case "help", "-h", "--help":
		usage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		usage()
		os.Exit(1)
	}
}
