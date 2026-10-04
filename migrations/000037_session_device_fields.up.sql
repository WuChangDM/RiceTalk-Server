BEGIN;

-- T16「S-2：多设备标识」— 会话表补充设备标识与最近活跃字段。
--
-- 设计要点：
--   * device_type / device_name 由客户端登录与 refresh 时可选上报（缺省空串），
--     服务端截断存储（type ≤ 32 字符 / name ≤ 64 字符），不参与认证判定；
--   * last_active_at 记录会话最近一次 refresh 轮换时间（refresh 是客户端活跃的
--     确定性信号），新登录行等于 created_at；用于 GET /auth/sessions 的「最近活跃」展示；
--   * 老会话行回填 last_active_at = created_at，device_* 保持空串（兼容不做强制）；
--   * SQLite（开发/测试走 AutoMigrate，RRT_FORCE_AUTOMIGRATE=1）与 PostgreSQL 语义一致。

ALTER TABLE user_sessions ADD COLUMN device_type VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE user_sessions ADD COLUMN device_name VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE user_sessions ADD COLUMN last_active_at TIMESTAMPTZ;

-- 老会话行回填：最近活跃退化为创建时间，避免列表排序出现 NULL。
UPDATE user_sessions SET last_active_at = created_at WHERE last_active_at IS NULL;

COMMIT;
