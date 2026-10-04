BEGIN;

-- 回滚：删除 voice_rooms.e2_ee_enabled 字段
ALTER TABLE voice_rooms DROP COLUMN IF EXISTS e2_ee_enabled;

COMMIT;
