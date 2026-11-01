BEGIN;

-- 1.9.4 A 方案回滚：删除共享文档协作者表。
-- 表名与 GORM 默认表名一致（shared_document_collaborators），见 up 迁移注释。
-- 索引与 CHECK/UNIQUE 约束随表一并删除；文档本身与版本历史不受影响，
-- 回滚后文档回到「仅创建者可编辑」的旧语义。
DROP TABLE IF EXISTS shared_document_collaborators;

COMMIT;
