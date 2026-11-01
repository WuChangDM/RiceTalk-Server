BEGIN;

-- ── 回滚 task-131：移除 device_id 与部分唯一索引 ──────────────────────────
-- 说明：历史重复会话行在 up 迁移中已被删除，回滚**无法恢复**这些行（它们携带的
-- refresh token 也早已失效）。回滚后服务端回到「每次登录新增一行」的旧行为，
-- 用户重新登录即可得到新会话行，无需人工修数据。
-- 若回滚前需要留档，请先 pg_dump 或至少 SELECT * FROM user_sessions 备份。

DROP INDEX IF EXISTS idx_user_sessions_user_device;

ALTER TABLE user_sessions DROP COLUMN IF EXISTS device_id;

COMMIT;
