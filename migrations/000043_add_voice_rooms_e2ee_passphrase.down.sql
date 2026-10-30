BEGIN;

-- 回滚：删除 E2EE 二期房间口令字段（清除后所有房间退回一期服务端分发模式）
ALTER TABLE voice_rooms DROP COLUMN IF EXISTS e2_ee_passphrase_enabled;
ALTER TABLE voice_rooms DROP COLUMN IF EXISTS e2_ee_magic;

COMMIT;
