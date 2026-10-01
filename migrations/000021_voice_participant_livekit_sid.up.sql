-- Migration: voice_participant_livekit_sid
-- Date: 2026-06-29
-- Description: VoiceParticipant 新增 livekit_sid 字段，用于 webhook 精确匹配 LiveKit 会话
-- Spec: .trae/specs/fix-voice-rejoin-webhook-sid/

-- 新增 livekit_sid 列（可空，用于精确匹配 LiveKit participant Sid）
ALTER TABLE voice_participants ADD COLUMN IF NOT EXISTS livekit_sid VARCHAR(64) DEFAULT '';

-- 创建索引加速 Sid 匹配查询
CREATE INDEX IF NOT EXISTS idx_voice_participant_sid ON voice_participants (livekit_sid);
