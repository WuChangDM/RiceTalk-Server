package database

import (
	"fmt"
	"os"

	"gorm.io/gorm"

	applogger "ridgericetalk/internal/logger"
)

// foreignKeyDef 定义一个外键约束
type foreignKeyDef struct {
	Name      string // 约束名称
	Table     string // 子表名
	Column    string // 子字段名（snake_case）
	RefTable  string // 父表名
	RefColumn string // 父字段名（snake_case）
	OnDelete  string // CASCADE / RESTRICT / SET NULL
}

// foreignKeys 是需要添加的外键约束列表（按设计文档 §10.2 级联策略）
// C7: PostgreSQL 环境添加物理外键约束，SQLite 环境跳过（PRAGMA foreign_keys 已启用）
var foreignKeys = []foreignKeyDef{
	// === 级联删除（CASCADE）===
	{Name: "fk_channels_space", Table: "channels", Column: "space_id", RefTable: "spaces", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_memberships_space", Table: "memberships", Column: "space_id", RefTable: "spaces", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_messages_channel", Table: "messages", Column: "channel_id", RefTable: "channels", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_messages_parent", Table: "messages", Column: "parent_id", RefTable: "messages", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_reactions_message", Table: "reactions", Column: "message_id", RefTable: "messages", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_channel_role_permissions_channel", Table: "channel_role_permissions", Column: "channel_id", RefTable: "channels", RefColumn: "id", OnDelete: "CASCADE"},
	// fk_shared_file_entries_folder removed in migration 000016: root-path uploads
	// set folder_id to empty string, which violates the FK constraint. Folder
	// referential integrity is enforced in cloudfs/service.go via explicit lookup.
	// {Name: "fk_shared_file_entries_folder", Table: "shared_file_entries", Column: "folder_id", RefTable: "shared_folders", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_shared_document_versions_document", Table: "shared_document_versions", Column: "document_id", RefTable: "shared_documents", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_shared_document_comments_document", Table: "shared_document_comments", Column: "document_id", RefTable: "shared_documents", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_shared_document_comments_parent", Table: "shared_document_comments", Column: "parent_id", RefTable: "shared_document_comments", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_schedule_events_space", Table: "schedule_events", Column: "space_id", RefTable: "spaces", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_virtual_net_sessions_space", Table: "virtual_net_sessions", Column: "space_id", RefTable: "spaces", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_virtual_net_nodes_session", Table: "virtual_net_nodes", Column: "session_id", RefTable: "virtual_net_sessions", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_voice_participants_room", Table: "voice_participants", Column: "room_id", RefTable: "voice_rooms", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_bot_play_queue_bot", Table: "bot_play_queues", Column: "bot_id", RefTable: "bots", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_screen_share_sessions_space", Table: "screen_share_sessions", Column: "space_id", RefTable: "spaces", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_screen_share_sessions_bind_channel", Table: "screen_share_sessions", Column: "channel_id", RefTable: "channels", RefColumn: "id", OnDelete: "SET NULL"},
	{Name: "fk_message_acks_message", Table: "message_acks", Column: "message_id", RefTable: "messages", RefColumn: "id", OnDelete: "CASCADE"},
	{Name: "fk_message_acks_channel", Table: "message_acks", Column: "channel_id", RefTable: "channels", RefColumn: "id", OnDelete: "CASCADE"},

	// === 限制删除（RESTRICT）===
	{Name: "fk_spaces_owner", Table: "spaces", Column: "owner_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_memberships_user", Table: "memberships", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_messages_user", Table: "messages", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_reactions_user", Table: "reactions", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_user_sessions_user", Table: "user_sessions", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_security_questions_user", Table: "security_questions", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_password_reset_tokens_user", Table: "password_reset_tokens", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_user_presences_user", Table: "user_presences", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_shared_document_comments_created_by", Table: "shared_document_comments", Column: "created_by", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_shared_document_comments_resolved_by", Table: "shared_document_comments", Column: "resolved_by", RefTable: "users", RefColumn: "id", OnDelete: "SET NULL"},
	{Name: "fk_remote_assist_sessions_requester", Table: "remote_assist_sessions", Column: "requester_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_remote_assist_sessions_target", Table: "remote_assist_sessions", Column: "target_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_password_history_user", Table: "password_history", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_bot_upload_audios_user", Table: "bot_upload_audios", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_schedule_events_creator", Table: "schedule_events", Column: "created_by", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_shared_documents_owner", Table: "shared_documents", Column: "owner_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_shared_document_versions_editor", Table: "shared_document_versions", Column: "edited_by", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},
	{Name: "fk_audit_logs_user", Table: "audit_logs", Column: "user_id", RefTable: "users", RefColumn: "id", OnDelete: "RESTRICT"},

	// === SET NULL ===
	{Name: "fk_channels_pinned_message", Table: "channels", Column: "pinned_message_id", RefTable: "messages", RefColumn: "id", OnDelete: "SET NULL"},
	{Name: "fk_remote_assist_sessions_channel", Table: "remote_assist_sessions", Column: "channel_id", RefTable: "channels", RefColumn: "id", OnDelete: "CASCADE"},
}

// addForeignKeyConstraints 在 PostgreSQL 环境添加外键约束（C7）
//
// 行为：
//   - SQLite 环境跳过（不支持 ALTER TABLE ADD CONSTRAINT，PRAGMA foreign_keys 已启用）
//   - PostgreSQL 环境通过原生 SQL 添加外键约束
//   - 添加失败时（如孤儿数据）记录警告但继续，不阻塞启动
//   - 约束已存在时跳过，避免重复创建错误
func addForeignKeyConstraints(db *gorm.DB, log *applogger.Logger) error {
	driver := os.Getenv("RRT_DB_DRIVER")
	if driver == "" {
		driver = "sqlite"
	}
	if driver != "postgres" {
		// SQLite 不支持 ALTER TABLE ADD CONSTRAINT，跳过
		// SQLite 的 PRAGMA foreign_keys(1) 已在 sqlite_std.go 中启用
		return nil
	}

	added := 0
	skipped := 0
	failed := 0

	for _, fk := range foreignKeys {
		// 检查约束是否已存在
		var count int64
		err := db.Raw(`
			SELECT COUNT(*) FROM information_schema.table_constraints
			WHERE constraint_name = ? AND table_name = ?
		`, fk.Name, fk.Table).Scan(&count).Error

		if err != nil {
			log.Warn("failed to check foreign key constraint existence, skipping",
				"constraint", fk.Name, "error", err)
			failed++
			continue
		}

		if count > 0 {
			skipped++
			continue
		}

		// 添加外键约束
		sql := fmt.Sprintf(
			"ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE %s",
			fk.Table, fk.Name, fk.Column, fk.RefTable, fk.RefColumn, fk.OnDelete,
		)
		if err := db.Exec(sql).Error; err != nil {
			log.Warn("failed to add foreign key constraint, continuing",
				"constraint", fk.Name, "table", fk.Table, "error", err)
			failed++
			continue
		}
		log.Info("added foreign key constraint", "name", fk.Name, "table", fk.Table)
		added++
	}

	log.Info("foreign key constraints setup complete",
		"added", added, "skipped", skipped, "failed", failed, "total", len(foreignKeys))
	return nil
}
