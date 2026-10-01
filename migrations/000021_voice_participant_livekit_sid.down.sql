-- Migration: voice_participant_livekit_sid (down)
-- Date: 2026-06-29
-- Description: 回滚 livekit_sid 字段

DROP INDEX IF EXISTS idx_voice_participant_sid;
ALTER TABLE voice_participants DROP COLUMN IF EXISTS livekit_sid;
