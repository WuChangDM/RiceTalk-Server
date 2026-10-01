package database

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readMigrationContract(t *testing.T, name string) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(sourcePath), "../../../migrations", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return string(content)
}

func TestMigrationForeignKeyChecksAreTableScoped(t *testing.T) {
	tests := []struct {
		name  string
		table string
		keys  []string
	}{
		{
			name:  "000031_shared_document_comments.up.sql",
			table: "public.shared_document_comments",
			keys: []string{
				"fk_shared_document_comments_document",
				"fk_shared_document_comments_parent",
				"fk_shared_document_comments_created_by",
				"fk_shared_document_comments_resolved_by",
			},
		},
		{
			name:  "000032_remote_assist_sessions.up.sql",
			table: "public.remote_assist_sessions",
			keys: []string{
				"fk_remote_assist_sessions_requester",
				"fk_remote_assist_sessions_target",
				"fk_remote_assist_sessions_channel",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := readMigrationContract(t, test.name)
			for _, key := range test.keys {
				marker := "conrelid = '" + test.table + "'::regclass"
				if !strings.Contains(content, marker) {
					t.Errorf("constraint %s is not checked against %s", key, test.table)
				}
			}
		})
	}
}

func TestPresenceMigrationRefusesAmbiguousStateAndReusesCompatibleObjects(t *testing.T) {
	content := readMigrationContract(t, "000033_reconcile_user_presence_table.up.sql")

	for _, expected := range []string{
		"both user_presence and user_presences exist; refusing automatic merge",
		"index_info.indrelid = 'public.user_presences'::regclass",
		"conrelid = 'public.user_presences'::regclass",
		"IF legacy_index_name IS NULL THEN",
		"IF legacy_constraint_name IS NULL THEN",
		"cardinality(pg_constraint.conkey) = 1",
		"cardinality(child_constraint.conkey) = 1",
	} {
		if !strings.Contains(content, expected) {
			t.Errorf("presence migration is missing contract marker %q", expected)
		}
	}

	if strings.Contains(content, "ALTER INDEX") || strings.Contains(content, "RENAME CONSTRAINT") {
		t.Fatal("presence migration must not rename existing PostgreSQL objects")
	}
}

// TestSessionIPUserAgentMigration 契约检查（A4-S1，DES-20261001-01 §5.2）：
// 迁移 000038 up/down 成对，up 加 ip/user_agent 两列（NOT NULL DEFAULT ''，
// 老会话行保持空串语义），down 对称删除。
func TestSessionIPUserAgentMigration(t *testing.T) {
	up := readMigrationContract(t, "000038_session_ip_user_agent.up.sql")
	down := readMigrationContract(t, "000038_session_ip_user_agent.down.sql")

	for _, expected := range []string{
		"BEGIN;",
		"ALTER TABLE user_sessions ADD COLUMN ip VARCHAR(45) NOT NULL DEFAULT '';",
		"ALTER TABLE user_sessions ADD COLUMN user_agent VARCHAR(256) NOT NULL DEFAULT '';",
		"COMMIT;",
	} {
		if !strings.Contains(up, expected) {
			t.Errorf("000038 up migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"BEGIN;",
		"ALTER TABLE user_sessions DROP COLUMN IF EXISTS ip;",
		"ALTER TABLE user_sessions DROP COLUMN IF EXISTS user_agent;",
		"COMMIT;",
	} {
		if !strings.Contains(down, expected) {
			t.Errorf("000038 down migration is missing %q", expected)
		}
	}
}

// TestScheduleEventInvitesMigration 契约检查（A8-S1，DES-20261001-01 §9.2）：
// 迁移 000039 up/down 成对，up 建邀请表（(event_id,user_id) 唯一 + 用户索引）并为
// schedule_events 加 invitee_ids JSONB NOT NULL DEFAULT '[]'（老事件行保持空数组语义，
// 即普通事件），down 对称删表删列。
func TestScheduleEventInvitesMigration(t *testing.T) {
	up := readMigrationContract(t, "000039_schedule_event_invites.up.sql")
	down := readMigrationContract(t, "000039_schedule_event_invites.down.sql")

	for _, expected := range []string{
		"BEGIN;",
		"CREATE TABLE schedule_event_invites (",
		"event_id VARCHAR(64) NOT NULL",
		"user_id VARCHAR(64) NOT NULL",
		"status VARCHAR(16) NOT NULL DEFAULT 'pending'",
		"responded_at TIMESTAMPTZ NULL",
		"CREATE UNIQUE INDEX idx_schedule_invite_event_user ON schedule_event_invites(event_id, user_id);",
		"CREATE INDEX idx_schedule_invite_user ON schedule_event_invites(user_id);",
		"ALTER TABLE schedule_events ADD COLUMN invitee_ids JSONB NOT NULL DEFAULT '[]';",
		"COMMIT;",
	} {
		if !strings.Contains(up, expected) {
			t.Errorf("000039 up migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"BEGIN;",
		"ALTER TABLE schedule_events DROP COLUMN IF EXISTS invitee_ids;",
		"DROP TABLE IF EXISTS schedule_event_invites;",
		"COMMIT;",
	} {
		if !strings.Contains(down, expected) {
			t.Errorf("000039 down migration is missing %q", expected)
		}
	}
}
