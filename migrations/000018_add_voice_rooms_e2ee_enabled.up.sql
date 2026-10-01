BEGIN;

-- 为 voice_rooms 表添加 E2EE 字段
-- ISSUE-043 P0 修复：VoiceRoom.E2EEEnabled 字段在生产环境（禁用 GORM AutoMigrate）下
-- 缺少对应数据库列，导致 POST /api/v1/voice/token 返回 503 VOICE_SERVICE_UNAVAILABLE
-- 默认 false（关闭端到端加密），与 GORM tag `default:false` 一致
ALTER TABLE voice_rooms ADD COLUMN IF NOT EXISTS e2_ee_enabled BOOLEAN DEFAULT false;

COMMIT;
