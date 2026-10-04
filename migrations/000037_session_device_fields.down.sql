BEGIN;

-- T16「S-2：多设备标识」回滚：删除会话表的设备标识与最近活跃字段。
--
-- 这些字段均为客户端可选上报的展示性元数据，不参与认证判定；
-- 回滚后仅丢失设备标识与最近活跃展示，登录态（token_hash）不受影响。

ALTER TABLE user_sessions DROP COLUMN IF EXISTS device_type;
ALTER TABLE user_sessions DROP COLUMN IF EXISTS device_name;
ALTER TABLE user_sessions DROP COLUMN IF EXISTS last_active_at;

COMMIT;
