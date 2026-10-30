package testutil

import (
	"fmt"
	"sync/atomic"

	"ridgericetalk/internal/database/automigrate"
	"ridgericetalk/internal/logger"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var testDBCounter int64

// SetupTestDB creates an isolated in-memory SQLite database with all tables migrated.
func SetupTestDB() (*gorm.DB, error) {
	id := atomic.AddInt64(&testDBCounter, 1)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:memdb%d?mode=memory", id)), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open test db: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	// 复用生产清单（唯一真源），避免测试库与 SQLite 生产建表漂移（task-69）。
	if err := db.AutoMigrate(automigrate.AllModels()...); err != nil {
		return nil, fmt.Errorf("migrate test db: %w", err)
	}

	return db, nil
}

// MustSetupTestDB panics if SetupTestDB fails.
func MustSetupTestDB() *gorm.DB {
	db, err := SetupTestDB()
	if err != nil {
		panic(err)
	}
	return db
}

// TestLogger returns a silent logger suitable for tests.
func TestLogger() *logger.Logger {
	log, _ := logger.New("fatal")
	return log
}
