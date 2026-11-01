BEGIN;

-- 1.9.4 A 方案：共享文档协作者。
--
-- 此前共享文档只有创建者能 PATCH（非创建者 SHAREDOC_PERMISSION_DENIED），
-- 多人协作只能靠「各自建一份」。本表让创建者（owner）可邀请同空间成员共同编辑。
--
-- 关键约束（与 internal/model.SharedDocumentCollaborator 对齐）：
--   * 表名必须等于 GORM 默认表名 shared_document_collaborators（= 结构体名
--     SharedDocumentCollaborator 的 snake_case 复数）。生产环境 RRT_ENV=production
--     默认关闭 AutoMigrate，代码只认 GORM 表名；若迁移建成别的名字（例如初版曾
--     误用 sharedoc_collaborators），PG 上所有协作者查询都会 no such table →
--     editor PATCH 与 GET /editors 直接 500，而 SQLite 因 AutoMigrate 建表测不出来。
--     防回归用例：server/internal/database/migration_table_name_test.go。
--   * owner 不入表：创建者由 shared_documents.owner_id 承载，恒有全部权限；
--   * role 仅允许 editor（可改内容/标题）/ viewer（只读）；owner 需更高权限请改
--     owner_id（暂不提供接口）；
--   * (doc_id, user_id) 唯一：同一文档同一用户只允许一条授权行，重复邀请按
--     role 覆盖（handler 内 upsert 语义），避免脏数据分叉；
--   * 时间列一律 TIMESTAMPTZ（000044 的教训：裸 TIMESTAMP 在 PG 下与 Go 端
--     time.Time 差 8 小时，SQLite 测不出来）。
CREATE TABLE IF NOT EXISTS shared_document_collaborators (
    id VARCHAR(64) PRIMARY KEY,
    doc_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(64) NOT NULL,
    role VARCHAR(16) NOT NULL DEFAULT 'editor',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT shared_document_collaborators_doc_user_key UNIQUE (doc_id, user_id),
    CONSTRAINT shared_document_collaborators_role_check CHECK (role IN ('editor', 'viewer'))
);

-- 权限判定按 (doc_id, user_id) 单行查询（requireEdit），列表按 doc_id 排序取。
CREATE INDEX IF NOT EXISTS idx_shared_document_collab_doc ON shared_document_collaborators (doc_id);
CREATE INDEX IF NOT EXISTS idx_shared_document_collab_user ON shared_document_collaborators (user_id);

COMMIT;
