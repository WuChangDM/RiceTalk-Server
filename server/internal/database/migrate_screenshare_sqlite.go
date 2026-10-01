package database

import (
	"fmt"

	"gorm.io/gorm"
)

// migrateScreenShareSQLiteCompat handles the SQLite-specific migration that
// 000008_screenshare_v2.up.sql cannot perform (that file uses PostgreSQL-only
// syntax: DO $$, information_schema.columns, ALTER COLUMN SET NOT NULL).
//
// Background: migration 000007 created `bind_channel_id` (nullable) on
// `screen_share_sessions`. Migration 000008 was supposed to rename it to
// `channel_id` (NOT NULL) and add V2 fields, but it only runs on PostgreSQL.
// On SQLite the table is stuck at the 000007 state, so GORM AutoMigrate
// tries to `ADD COLUMN channel_id TEXT NOT NULL` and fails with
// "Cannot add a NOT NULL column with default value NULL".
//
// This function runs BEFORE AutoMigrate and performs the rename using
// SQLite 3.25.0+ RENAME COLUMN syntax. After the rename, AutoMigrate can
// safely add the V2 columns (all nullable or with defaults) and apply
// NOT NULL constraints via table rebuild.
//
// Reference: 设计文档 §12.6 SQLite 兼容迁移
func migrateScreenShareSQLiteCompat(db *gorm.DB) error {
	// Only run on SQLite
	if db.Dialector.Name() != "sqlite" {
		return nil
	}

	// Check if screen_share_sessions table exists
	var tableExists int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='screen_share_sessions'").Scan(&tableExists).Error; err != nil {
		return fmt.Errorf("check screen_share_sessions existence: %w", err)
	}
	if tableExists == 0 {
		// Table will be created by AutoMigrate from scratch
		return nil
	}

	// Check if bind_channel_id exists and channel_id does not
	var bindChannelCount int64
	if err := db.Raw("SELECT count(*) FROM pragma_table_info('screen_share_sessions') WHERE name='bind_channel_id'").Scan(&bindChannelCount).Error; err != nil {
		return fmt.Errorf("check bind_channel_id column: %w", err)
	}

	var channelIDCount int64
	if err := db.Raw("SELECT count(*) FROM pragma_table_info('screen_share_sessions') WHERE name='channel_id'").Scan(&channelIDCount).Error; err != nil {
		return fmt.Errorf("check channel_id column: %w", err)
	}

	// Rename bind_channel_id -> channel_id (SQLite 3.25.0+)
	if bindChannelCount > 0 && channelIDCount == 0 {
		if err := db.Exec("ALTER TABLE screen_share_sessions RENAME COLUMN bind_channel_id TO channel_id").Error; err != nil {
			return fmt.Errorf("rename bind_channel_id to channel_id: %w", err)
		}
		// Backfill NULL values with empty string so AutoMigrate can apply
		// NOT NULL constraint without violating existing rows.
		if err := db.Exec("UPDATE screen_share_sessions SET channel_id = '' WHERE channel_id IS NULL").Error; err != nil {
			return fmt.Errorf("backfill channel_id nulls: %w", err)
		}
	}

	// Rebuild indexes: drop old bind_channel_id index, create channel_id index
	// (safe to run regardless of rename — IF EXISTS / IF NOT EXISTS guards)
	if err := db.Exec("DROP INDEX IF EXISTS idx_screen_share_sessions_bind_channel_id").Error; err != nil {
		return fmt.Errorf("drop old bind_channel_id index: %w", err)
	}
	if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_screen_share_sessions_channel_id ON screen_share_sessions(channel_id)").Error; err != nil {
		return fmt.Errorf("create channel_id index: %w", err)
	}

	return nil
}
