package database

import (
	"testing"

	"ridgericetalk/tests/testutil"
)

// TestForeignKeysListComplete 验证外键约束列表覆盖所有核心表关系（C7）
func TestForeignKeysListComplete(t *testing.T) {
	if len(foreignKeys) == 0 {
		t.Fatal("foreign key list should not be empty")
	}

	// 验证每个外键定义的必需字段
	for _, fk := range foreignKeys {
		if fk.Name == "" {
			t.Error("foreign key name should not be empty")
		}
		if fk.Table == "" {
			t.Errorf("foreign key %s: table should not be empty", fk.Name)
		}
		if fk.Column == "" {
			t.Errorf("foreign key %s: column should not be empty", fk.Name)
		}
		if fk.RefTable == "" {
			t.Errorf("foreign key %s: ref table should not be empty", fk.Name)
		}
		if fk.RefColumn == "" {
			t.Errorf("foreign key %s: ref column should not be empty", fk.Name)
		}
		if fk.OnDelete != "CASCADE" && fk.OnDelete != "RESTRICT" && fk.OnDelete != "SET NULL" {
			t.Errorf("foreign key %s: invalid OnDelete %q, must be CASCADE/RESTRICT/SET NULL", fk.Name, fk.OnDelete)
		}
	}

	// 验证约束名称唯一
	seen := make(map[string]bool)
	for _, fk := range foreignKeys {
		if seen[fk.Name] {
			t.Errorf("duplicate foreign key constraint name: %s", fk.Name)
		}
		seen[fk.Name] = true
	}

	// 验证关键外键关系存在（按设计文档 §10.2）
	expectedFKs := []struct {
		Table    string
		Column   string
		RefTable string
		OnDelete string
	}{
		// 级联删除
		{"channels", "space_id", "spaces", "CASCADE"},
		{"messages", "channel_id", "channels", "CASCADE"},
		{"messages", "parent_id", "messages", "CASCADE"},
		{"reactions", "message_id", "messages", "CASCADE"},
		{"channel_role_permissions", "channel_id", "channels", "CASCADE"},
		// fk_shared_file_entries_folder removed in migration 000016; referential
		// integrity is enforced in cloudfs/service.go instead.
		{"shared_document_versions", "document_id", "shared_documents", "CASCADE"},
		{"shared_document_comments", "document_id", "shared_documents", "CASCADE"},
		{"shared_document_comments", "parent_id", "shared_document_comments", "CASCADE"},
		{"remote_assist_sessions", "channel_id", "channels", "CASCADE"},
		// 限制删除
		{"spaces", "owner_id", "users", "RESTRICT"},
		{"memberships", "user_id", "users", "RESTRICT"},
		{"messages", "user_id", "users", "RESTRICT"},
		{"reactions", "user_id", "users", "RESTRICT"},
		{"user_presences", "user_id", "users", "RESTRICT"},
		{"shared_document_comments", "created_by", "users", "RESTRICT"},
		{"remote_assist_sessions", "requester_id", "users", "RESTRICT"},
		{"remote_assist_sessions", "target_id", "users", "RESTRICT"},
		// SET NULL
		{"channels", "pinned_message_id", "messages", "SET NULL"},
	}

	for _, expected := range expectedFKs {
		found := false
		for _, fk := range foreignKeys {
			if fk.Table == expected.Table && fk.Column == expected.Column &&
				fk.RefTable == expected.RefTable && fk.OnDelete == expected.OnDelete {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected foreign key not found: %s.%s -> %s (ON DELETE %s)",
				expected.Table, expected.Column, expected.RefTable, expected.OnDelete)
		}
	}
}

// TestAddForeignKeyConstraints_SqliteSkipped 验证 SQLite 环境跳过外键添加（C7）
func TestAddForeignKeyConstraints_SqliteSkipped(t *testing.T) {
	// 确保使用 SQLite（默认）
	t.Setenv("RRT_DB_DRIVER", "")

	db, err := testutil.SetupTestDB()
	if err != nil {
		t.Fatalf("setup test db: %v", err)
	}

	log := testutil.TestLogger()

	// SQLite 环境应跳过外键添加，不报错
	if err := addForeignKeyConstraints(db, log); err != nil {
		t.Errorf("addForeignKeyConstraints should not fail on SQLite: %v", err)
	}

	// 显式设置 SQLite
	t.Setenv("RRT_DB_DRIVER", "sqlite")
	if err := addForeignKeyConstraints(db, log); err != nil {
		t.Errorf("addForeignKeyConstraints should not fail on explicit SQLite: %v", err)
	}
}

// TestAddForeignKeyConstraints_PostgresFlag 验证 PostgreSQL 环境标志检测（C7）
// 注意：此测试不连接真实 PostgreSQL，仅验证驱动检测逻辑
func TestAddForeignKeyConstraints_PostgresFlag(t *testing.T) {
	t.Setenv("RRT_DB_DRIVER", "postgres")

	db, err := testutil.SetupTestDB()
	if err != nil {
		t.Fatalf("setup test db: %v", err)
	}

	log := testutil.TestLogger()

	// 设置为 PostgreSQL 但实际使用 SQLite 连接
	// addForeignKeyConstraints 会尝试查询 information_schema，SQLite 中不存在该表
	// 但函数应优雅处理错误，不 panic，不返回致命错误
	err = addForeignKeyConstraints(db, log)
	if err != nil {
		t.Errorf("addForeignKeyConstraints should not return error even on SQLite with postgres flag: %v", err)
	}
}

// TestMigrate_WithForeignKeysOnSQLite 验证 Migrate 在 SQLite 环境正常工作（C7）
func TestMigrate_WithForeignKeysOnSQLite(t *testing.T) {
	t.Setenv("RRT_ENV", "development")
	t.Setenv("RRT_SKIP_AUTOMIGRATE", "")

	db, err := testutil.SetupTestDB()
	if err != nil {
		t.Fatalf("setup test db: %v", err)
	}

	log := testutil.TestLogger()
	if err := Migrate(&DB{db}, log); err != nil {
		t.Fatalf("Migrate should succeed on SQLite with foreign key setup: %v", err)
	}
}
