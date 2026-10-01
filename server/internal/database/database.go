package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/sirupsen/logrus"
	applogger "ridgericetalk/internal/logger"
	"ridgericetalk/internal/model"
)

// poolEnvInt reads an int environment variable for connection pool tuning.
// If the variable is unset or invalid, it returns the provided default.
func poolEnvInt(key string, defaultValue int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return defaultValue
}

// poolMaxOpen returns the configured MaxOpenConns for the driver.
func poolMaxOpen(driver string, defaultValue int) int {
	switch driver {
	case "postgres":
		return poolEnvInt("RRT_DB_MAX_OPEN_CONNS", defaultValue)
	case "sqlite":
		// SQLite writes are serialized; only override if explicitly requested.
		return poolEnvInt("RRT_DB_MAX_OPEN_CONNS", defaultValue)
	default:
		return defaultValue
	}
}

// poolMaxIdle returns the configured MaxIdleConns for the driver.
func poolMaxIdle(driver string, defaultValue int) int {
	return poolEnvInt("RRT_DB_MAX_IDLE_CONNS", defaultValue)
}

// poolConnMaxLifetime returns the configured connection max lifetime.
func poolConnMaxLifetime(defaultValue time.Duration) time.Duration {
	if v, err := strconv.Atoi(os.Getenv("RRT_DB_CONN_MAX_LIFETIME")); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	return defaultValue
}

// DB wraps gorm.DB for the application
type DB struct {
	*gorm.DB
}

// New creates a new database connection
func New(databaseURL string, log *applogger.Logger) (*DB, error) {
	driver := "sqlite"
	if os.Getenv("RRT_DB_DRIVER") != "" {
		driver = os.Getenv("RRT_DB_DRIVER")
	}

	var dialector gorm.Dialector

	switch driver {
	case "sqlite":
		// Ensure directory exists with secure permissions (0700)
		dir := filepath.Dir(databaseURL)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
		}
		sqliteKey := os.Getenv("RRT_SQLITE_KEY")
		d, err := openSQLite(databaseURL, sqliteKey)
		if err != nil {
			return nil, err
		}
		dialector = d
	case "postgres":
		d, err := openPostgres(databaseURL)
		if err != nil {
			return nil, err
		}
		dialector = d
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", driver)
	}

	gormLogLevel := logger.Silent
	if log.GetLevel() == logrus.DebugLevel {
		gormLogLevel = logger.Info
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.New(
			log, // reuse application logger interface
			logger.Config{
				SlowThreshold:             500 * time.Millisecond,
				LogLevel:                  gormLogLevel,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		),
		// NowFunc 强制 GORM 使用 UTC 时间，避免跨时区部署时数据时间字段漂移
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Get underlying sql.DB for connection pool config
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}

	switch driver {
	case "sqlite":
		// SQLite serialized writer: keep the pool small to reduce lock contention.
		sqlDB.SetMaxOpenConns(poolMaxOpen(driver, 4))
		sqlDB.SetMaxIdleConns(poolMaxIdle(driver, 2))
		sqlDB.SetConnMaxLifetime(poolConnMaxLifetime(30 * time.Minute))
	case "postgres":
		sqlDB.SetMaxOpenConns(poolMaxOpen(driver, 25))
		sqlDB.SetMaxIdleConns(poolMaxIdle(driver, 5))
		sqlDB.SetConnMaxLifetime(poolConnMaxLifetime(30 * time.Minute))
	}

	return &DB{db}, nil
}

// Migrate runs all database migrations using GORM AutoMigrate.
//
// H36 (batch 9n): Production environments now ALWAYS skip AutoMigrate.
// Versioned migrations must be applied via 'go run ./cmd/migrate up' before
// starting the server. The RRT_ENABLE_AUTOMIGRATE escape hatch is no longer
// honored in production — it is ignored silently for backward compatibility.
//
// In non-production environments AutoMigrate runs by default but can be
// disabled with RRT_SKIP_AUTOMIGRATE=1.
//
// C7: After AutoMigrate, addForeignKeyConstraints is called to create
// physical foreign key constraints in PostgreSQL environments.
func Migrate(db *DB, log *applogger.Logger) error {
	env := os.Getenv("RRT_ENV")
	skip := os.Getenv("RRT_SKIP_AUTOMIGRATE") == "1"

	if env == "production" && os.Getenv("RRT_FORCE_AUTOMIGRATE") != "1" {
		// H36: AutoMigrate is fully disabled in production. Operators must use
		// 'go run ./cmd/migrate up' to apply versioned migrations.
		// RRT_FORCE_AUTOMIGRATE=1 is an intentional escape hatch for test/lab
		// deployments where running the migration tool is not practical.
		fmt.Fprintf(os.Stderr, "INFO: AutoMigrate disabled in production. Use 'migrate up' for versioned migrations.\n")
		// C7: 即使 AutoMigrate 跳过，也尝试添加外键约束（PostgreSQL 环境）
		// 外键约束通过 ALTER TABLE ADD CONSTRAINT 添加，不依赖 AutoMigrate
		if err := addForeignKeyConstraints(db.DB, log); err != nil {
			log.Warn("failed to add foreign key constraints", "error", err)
		}
		return nil
	}

	if skip {
		fmt.Fprintf(os.Stderr, "INFO: AutoMigrate skipped because RRT_SKIP_AUTOMIGRATE=1.\n")
		// C7: 即使 AutoMigrate 跳过，也尝试添加外键约束（PostgreSQL 环境）
		if err := addForeignKeyConstraints(db.DB, log); err != nil {
			log.Warn("failed to add foreign key constraints", "error", err)
		}
		return nil
	}

	// SQLite 兼容迁移：修复 000008_screenshare_v2.up.sql 在 SQLite 上无法执行的问题
	// 必须在 AutoMigrate 之前执行，将 bind_channel_id 重命名为 channel_id
	// 详见设计文档 §12.6 SQLite 兼容迁移
	if err := migrateScreenShareSQLiteCompat(db.DB); err != nil {
		log.Warn("failed to run SQLite compat migration for screen_share_sessions", "error", err)
		// 不返回错误，让 AutoMigrate 尝试继续（可能表已是正确状态）
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.Space{},
		&model.Membership{},
		&model.Channel{},
		&model.DMChannel{},
		&model.Message{},
		&model.Reaction{},
		&model.UserSession{},
		&model.AdminBootstrapToken{},
		&model.UserPresence{},
		&model.UserChannelRead{},
		&model.ChannelRolePermission{},
		&model.OGCache{},
		&model.VoiceRoom{},
		&model.VoiceParticipant{},
		&model.BotPlayQueue{},
		&model.BotPlayerState{},
		&model.BotUploadAudio{},
		&model.BotTTSMessage{},
		&model.TTSConsent{},
		&model.Whiteboard{},
		&model.WhiteboardStroke{},
		&model.FileMetadata{},
		&model.SharedFolder{},
		&model.SharedFileEntry{},
		&model.SharedDocument{},
		&model.SharedDocumentVersion{}, // M25: 文档版本历史
		&model.SharedDocumentComment{}, // Phase 2: 文档评论
		&model.ScheduleEvent{},
		&model.ScheduleEventInvite{}, // A8-S1: 日程事件邀请 RSVP（SQLite 开发环境建表；生产走 000039 迁移）
		&model.MinigameSession{},
		&model.MinigameHistory{},     // §15.4: 游戏历史记录
		&model.MinigameLeaderboard{}, // §15.7.4: 单机游戏排行榜（此前漏迁移，导致提交/查询报 no such table）
		&model.VirtualNetSession{},
		&model.VirtualNetNode{},
		&model.AdminConfig{},
		&model.AuditLog{},
		&model.ModuleRuntimeStatus{},
		&model.ScreenShareSession{},
		&model.MessageAck{},
		&model.SecurityAuditLog{},
		&model.PasswordHistory{},
		&model.SecurityQuestion{},
		&model.PasswordResetToken{},
		&model.VoiceRecording{},
		&model.E2EEKey{},
		&model.NeteaseAuth{},
		&model.Bot{},
		&model.RemoteAssistSession{}, // T49: 远程协助会话
		&model.UserSound{},          // 出入频道音效库
		&model.UserSoundSettings{},  // 个人入场/出场音效绑定
		&model.UserSoundFavorite{},  // 音效收藏
		&model.CloudFileShare{},     // DES-2026-0912-05: 云文件分享链接（SQLite 开发环境建表；生产走 000035 迁移）
		&model.UserChannelMute{},    // T4/S-1: 频道级静音（SQLite 开发环境建表；生产走 000036 迁移）
		&model.AdminAlert{},         // DES-20261001-01 §12: 管理后台监控告警（SQLite 开发环境建表；生产走 000040 迁移）
	); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// DES-2026-0912-02 §2.5: SQLite has no versioned migrations, so re-apply the
	// CloudFS partial unique indexes here (AutoMigrate will not rewrite an
	// existing index). Must run after AutoMigrate so the tables exist.
	if err := migrateCloudFSPartialUniqueIndexesSQLite(db.DB); err != nil {
		log.Warn("failed to reconcile CloudFS partial unique indexes (SQLite)", "error", err)
	}

	// C7: 在 PostgreSQL 环境添加外键约束
	if err := addForeignKeyConstraints(db.DB, log); err != nil {
		log.Warn("failed to add foreign key constraints", "error", err)
	}

	// Migrate legacy whiteboard strokes from channel-scoped to independent whiteboards.
	if err := migrateWhiteboardIndependence(db.DB, log); err != nil {
		log.Warn("failed to migrate whiteboard independence", "error", err)
	}

	return nil
}

// Transaction wraps a function in a database transaction
func (db *DB) Transaction(fn func(*gorm.DB) error) error {
	return db.DB.Transaction(fn)
}
