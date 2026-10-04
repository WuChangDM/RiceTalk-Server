-- 说明: 回滚 000004，恢复 minigame_sessions 的 bind_channel_id 列
-- 适用: PostgreSQL 生产环境

ALTER TABLE minigame_sessions ADD COLUMN IF NOT EXISTS bind_channel_id VARCHAR(64);
