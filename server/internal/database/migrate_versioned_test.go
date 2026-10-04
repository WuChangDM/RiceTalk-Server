package database

import (
	"os"
	"path/filepath"
	"testing"

	"ridgericetalk/core/idgen"
	"ridgericetalk/tests/testutil"
)

// NJ-35 回归：生产（RRT_ENV=production）+ SQLite 下，migrate up 必须真正
// 建表（此前被 Migrate() 的 H36 生产门禁静默跳过，报成功但零表，全新部署
// 首启即 "no such table: users" 崩溃）。
func TestRunMigrationsUpSQLiteCreatesTablesInProduction(t *testing.T) {
	idgen.Init(1, 1)
	t.Setenv("RRT_ENV", "production")
	t.Setenv("RRT_FORCE_AUTOMIGRATE", "")

	// 自管目录：RunMigrationsUp 内部连接不关闭（产品级小泄漏，另行记录），
	// t.TempDir 的强制清理会在 Windows 上因句柄占用而判失败。
	dir := filepath.Join(os.TempDir(), "nj35-"+idgen.NextString())
	_ = os.MkdirAll(dir, 0o700)
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) // 尽力清理，句柄占用时忽略
	dbURL := filepath.Join(dir, "fresh.db")
	log := testutil.TestLogger()

	if err := RunMigrationsUp(dbURL, "sqlite", "../../migrations", log); err != nil {
		t.Fatalf("migrate up on fresh production sqlite: %v", err)
	}

	// 表必须真实存在（此前 bug：报成功但 0 表）
	db, err := New(dbURL, log)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	var got int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='users'").Scan(&got).Error; err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if sqlDB, err := db.DB.DB(); err == nil {
		_ = sqlDB.Close() // 释放文件句柄，避免 Windows 下 TempDir 清理失败
	}
	if got != 1 {
		t.Fatal("users 表未被创建——生产 SQLite migrate up 仍是空操作")
	}
}
