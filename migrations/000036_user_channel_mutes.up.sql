BEGIN;

-- T4「通知与未读治理」/ 审计 S-1 — 频道级静音表。
--
-- 设计要点：
--   * 每用户每频道最多一行静音（唯一索引 idx_user_channel_mute），重复开启走删除重建；
--   * muted_until 可空：NULL = 永久静音；非 NULL 且已过期视为未静音
--     （查询侧过滤 `muted_until IS NULL OR muted_until > now()`，不做后台清理任务）；
--   * 静音是个人偏好而非频道属性：删除频道时需级联清理（channel.Service.DeleteChannel
--     第 9 步），删除用户沿用软删除，不清理本表（保留偏好供重新加入后恢复）。
--
-- 时间列统一使用 TIMESTAMPTZ：过期判定依赖 muted_until > now() 的比较，
-- PostgreSQL 与 SQLite（开发/测试走 AutoMigrate）对 UTC 时间的比较语义一致。

CREATE TABLE IF NOT EXISTS user_channel_mutes (
    id          VARCHAR(64)  PRIMARY KEY,
    user_id     VARCHAR(64)  NOT NULL,
    channel_id  VARCHAR(64)  NOT NULL,
    muted_until TIMESTAMPTZ,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- 索引名与 GORM 模型 tag 中的名字保持一致，避免启用 AutoMigrate 的环境
-- （RRT_FORCE_AUTOMIGRATE=1）再建一份同义索引。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_channel_mute ON user_channel_mutes(user_id, channel_id);
CREATE INDEX IF NOT EXISTS idx_user_channel_mutes_channel_id ON user_channel_mutes(channel_id);

COMMIT;
