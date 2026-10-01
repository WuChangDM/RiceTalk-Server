-- 说明: 回滚 000003，恢复残留的 channel_id 列
-- 适用: PostgreSQL 生产环境

ALTER TABLE file_metadata ADD COLUMN IF NOT EXISTS channel_id VARCHAR(64);
ALTER TABLE schedule_events ADD COLUMN IF NOT EXISTS channel_id VARCHAR(64);
ALTER TABLE shared_documents ADD COLUMN IF NOT EXISTS channel_id VARCHAR(64);
