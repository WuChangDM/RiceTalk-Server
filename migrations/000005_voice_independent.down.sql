-- ════════════════════════════════════════════════════════════
-- Reverse voice module independence migration.
-- ════════════════════════════════════════════════════════════

BEGIN;

-- ── e2ee_keys: room_id -> channel_id ──
ALTER TABLE e2ee_keys ADD COLUMN IF NOT EXISTS channel_id VARCHAR(64);

UPDATE e2ee_keys k
SET channel_id = COALESCE(vr.bind_channel_id, k.room_id)
FROM voice_rooms vr
WHERE k.room_id = vr.id;

ALTER TABLE e2ee_keys
    ALTER COLUMN channel_id SET NOT NULL;

DROP INDEX IF EXISTS idx_e2ee_keys_room_id;
CREATE UNIQUE INDEX IF NOT EXISTS idx_e2ee_keys_channel_id ON e2ee_keys (channel_id);

ALTER TABLE e2ee_keys DROP COLUMN IF EXISTS room_id;

-- ── voice_recordings: room_id -> channel_id ──
ALTER TABLE voice_recordings ADD COLUMN IF NOT EXISTS channel_id VARCHAR(64);

UPDATE voice_recordings rec
SET channel_id = COALESCE(vr.bind_channel_id, rec.room_id)
FROM voice_rooms vr
WHERE rec.room_id = vr.id;

ALTER TABLE voice_recordings
    ALTER COLUMN channel_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_voice_recordings_channel_id ON voice_recordings (channel_id);

ALTER TABLE voice_recordings DROP COLUMN IF EXISTS room_id;

-- ── voice_rooms: space_id + bind_channel_id + quality -> channel_id ──
ALTER TABLE voice_rooms
    ADD COLUMN IF NOT EXISTS channel_id VARCHAR(64);

UPDATE voice_rooms
SET channel_id = COALESCE(bind_channel_id, id);

ALTER TABLE voice_rooms
    ALTER COLUMN channel_id SET NOT NULL;

-- Restore the legacy LiveKit room name prefix.
UPDATE voice_rooms
SET live_kit_room = 'rrt-channel-' || channel_id
WHERE live_kit_room IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_voice_rooms_channel_id ON voice_rooms (channel_id);

ALTER TABLE voice_rooms
    DROP COLUMN IF EXISTS space_id,
    DROP COLUMN IF EXISTS bind_channel_id,
    DROP COLUMN IF EXISTS quality;

DROP INDEX IF EXISTS idx_voice_rooms_space_id;
DROP INDEX IF EXISTS idx_voice_rooms_bind_channel_id;

COMMIT;
