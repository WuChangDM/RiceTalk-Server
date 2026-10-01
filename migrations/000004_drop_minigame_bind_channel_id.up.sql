-- 说明: 删除 minigame_sessions 已解耦的 bind_channel_id 列
-- 适用: PostgreSQL 生产环境
-- 对应设计: docs/FUNCTIONAL_MODULE_INDEPENDENCE_DESIGN.md

ALTER TABLE minigame_sessions DROP COLUMN IF EXISTS bind_channel_id;
