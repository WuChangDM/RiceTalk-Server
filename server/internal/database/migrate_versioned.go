package database

import (
	"os"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // register postgres driver
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"ridgericetalk/internal/logger"
)

// MigrationsDir is the default location of SQL migration files, relative to
// the server working directory. It points to the project-level migrations/
// directory (one level above server/).
const MigrationsDir = "../migrations"

// resolveMigrationsPath returns the file:// source URL for golang-migrate.
// If migrationsPath is empty, MigrationsDir is used.
func resolveMigrationsPath(migrationsPath string) string {
	if migrationsPath == "" {
		migrationsPath = MigrationsDir
	}
	if !strings.HasPrefix(migrationsPath, "file://") {
		migrationsPath = "file://" + migrationsPath
	}
	return migrationsPath
}

// newMigrate creates a golang-migrate instance for the given database URL and
// driver. Currently only PostgreSQL is supported for versioned migrations;
// SQLite falls back to GORM AutoMigrate (see RunMigrationsUp).
func newMigrate(dbURL, dbDriver, migrationsPath string) (*migrate.Migrate, error) {
	sourceURL := resolveMigrationsPath(migrationsPath)

	switch strings.ToLower(dbDriver) {
	case "postgres", "postgresql":
		// golang-migrate accepts the standard postgres connection URL.
		m, err := migrate.New(sourceURL, dbURL)
		if err != nil {
			return nil, fmt.Errorf("create migrate instance: %w", err)
		}
		return m, nil
	case "sqlite", "sqlite3":
		// golang-migrate's sqlite3 driver requires CGO and mattn/go-sqlite3,
		// which conflicts with our pure-Go glebarez/sqlite. Fall back to
		// AutoMigrate for SQLite environments (development/test only).
		return nil, fmt.Errorf("versioned migrations are not supported for SQLite; use AutoMigrate (development) or PostgreSQL (production)")
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", dbDriver)
	}
}

// RunMigrationsUp applies all pending up migrations from the migrations directory.
// For SQLite, it falls back to GORM AutoMigrate.
func RunMigrationsUp(dbURL, dbDriver, migrationsPath string, log *logger.Logger) error {
	if strings.ToLower(dbDriver) == "sqlite" || strings.ToLower(dbDriver) == "sqlite3" {
		log.Info("SQLite detected; falling back to GORM AutoMigrate (versioned migrations are PostgreSQL-only)")
		db, err := New(dbURL, log)
		if err != nil {
			return fmt.Errorf("connect database for AutoMigrate: %w", err)
		}
		// NJ-35：migrate 工具本身就是生产 SQLite 环境的迁移入口，其 AutoMigrate
		// 兜底必须真正执行。此前直接调 Migrate() 会撞上 H36 的生产门禁被静默
		// 跳过——migrate up 报成功但零建表，全新部署首启即 "no such table: users"
		// 崩溃（实测 2026-10-04）。借用 H36 预留的 RRT_FORCE_AUTOMIGRATE 逃生门。
		os.Setenv("RRT_FORCE_AUTOMIGRATE", "1")
		defer os.Unsetenv("RRT_FORCE_AUTOMIGRATE")
		return Migrate(db, log)
	}

	m, err := newMigrate(dbURL, dbDriver, migrationsPath)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}

	version, dirty, _ := m.Version()
	log.Info("migrations applied", "version", version, "dirty", dirty)
	return nil
}

// RunMigrationsDown rolls back the last N migrations. If steps <= 0, rolls
// back exactly one migration.
func RunMigrationsDown(dbURL, dbDriver, migrationsPath string, steps int, log *logger.Logger) error {
	if steps <= 0 {
		steps = 1
	}

	if strings.ToLower(dbDriver) == "sqlite" || strings.ToLower(dbDriver) == "sqlite3" {
		return fmt.Errorf("migrate down is not supported for SQLite; AutoMigrate does not support rollback")
	}

	m, err := newMigrate(dbURL, dbDriver, migrationsPath)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Steps(-steps); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate down %d steps: %w", steps, err)
	}

	version, dirty, _ := m.Version()
	log.Info("migrations rolled back", "version", version, "dirty", dirty, "steps", steps)
	return nil
}

// ForceMigrationVersion sets the migration version to a specific value,
// marking it as clean. Useful for baselining existing databases.
func ForceMigrationVersion(dbURL, dbDriver, migrationsPath string, version int, log *logger.Logger) error {
	if strings.ToLower(dbDriver) == "sqlite" || strings.ToLower(dbDriver) == "sqlite3" {
		return fmt.Errorf("force version is not supported for SQLite")
	}

	m, err := newMigrate(dbURL, dbDriver, migrationsPath)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Force(version); err != nil {
		return fmt.Errorf("force version %d: %w", version, err)
	}

	log.Info("migration version forced", "version", version)
	return nil
}

// GetMigrationVersion returns the current migration version and dirty state.
// Returns (0, false, nil) if no migrations have been applied yet.
func GetMigrationVersion(dbURL, dbDriver, migrationsPath string) (uint, bool, error) {
	if strings.ToLower(dbDriver) == "sqlite" || strings.ToLower(dbDriver) == "sqlite3" {
		return 0, false, fmt.Errorf("version query is not supported for SQLite; use PostgreSQL")
	}

	m, err := newMigrate(dbURL, dbDriver, migrationsPath)
	if err != nil {
		return 0, false, err
	}
	defer m.Close()

	version, dirty, err := m.Version()
	if err != nil {
		if err == migrate.ErrNilVersion {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get version: %w", err)
	}
	return version, dirty, nil
}

// MarkMigrationDirty marks the current migration version as dirty without
// changing the version number. Useful for recovering from failed migrations.
func MarkMigrationDirty(dbURL, dbDriver, migrationsPath string, log *logger.Logger) error {
	if strings.ToLower(dbDriver) == "sqlite" || strings.ToLower(dbDriver) == "sqlite3" {
		return fmt.Errorf("mark dirty is not supported for SQLite")
	}

	m, err := newMigrate(dbURL, dbDriver, migrationsPath)
	if err != nil {
		return err
	}
	defer m.Close()

	version, _, err := m.Version()
	if err != nil {
		return fmt.Errorf("get current version before marking dirty: %w", err)
	}

	if err := m.Force(int(version)); err != nil {
		return fmt.Errorf("mark version %d dirty: %w", version, err)
	}

	log.Warn("migration version marked dirty", "version", version)
	return nil
}

// SanitizeDBURLForLog redacts the password in a database URL for safe logging.
func SanitizeDBURLForLog(dbURL string) string {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "[unparseable url]"
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "REDACTED")
	}
	return u.String()
}
