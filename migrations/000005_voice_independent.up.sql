-- ════════════════════════════════════════════════════════════
-- Voice module independence: move voice from channel coupling to Space-bound
-- independent voice rooms.
-- ════════════════════════════════════════════════════════════

BEGIN;

-- ── voice_rooms: channel_id -> space_id + bind_channel_id + quality ──
ALTER TABLE voice_rooms
    ADD COLUMN IF NOT EXISTS space_id        VARCHAR(64),
    ADD COLUMN IF NOT EXISTS bind_channel_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS quality         VARCHAR(16) DEFAULT 'standard';

-- Backfill space_id and optional channel binding from the legacy channel_id.
UPDATE voice_rooms vr
SET space_id       = c.space_id,
    bind_channel_id = vr.channel_id,
    quality        = COALESCE(NULLIF(c.voice_quality, ''), 'standard')
FROM channels c
WHERE vr.channel_id = c.id;

-- Any orphaned rooms (no matching channel) get a placeholder SpaceID so the
-- NOT NULL constraint can be enforced. Operators can reconcile these later.
UPDATE voice_rooms
SET space_id = ''
WHERE space_id IS NULL OR space_id = '';

ALTER TABLE voice_rooms
    ALTER COLUMN space_id SET NOT NULL,
    ALTER COLUMN quality  SET NOT NULL;

-- Switch the LiveKit room name prefix to the new room-scoped format.
UPDATE voice_rooms
SET live_kit_room = 'rrt-room-' || id
WHERE live_kit_room IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_voice_rooms_space_id        ON voice_rooms (space_id);
CREATE INDEX IF NOT EXISTS idx_voice_rooms_bind_channel_id ON voice_rooms (bind_channel_id);

DROP INDEX IF EXISTS idx_voice_rooms_channel_id;
ALTER TABLE voice_rooms DROP COLUMN IF EXISTS channel_id;

-- ── voice_recordings: channel_id -> room_id ──
ALTER TABLE voice_recordings ADD COLUMN IF NOT EXISTS room_id VARCHAR(64);

UPDATE voice_recordings rec
SET room_id = vr.id
FROM voice_rooms vr
WHERE rec.channel_id = vr.bind_channel_id;

-- Recordings whose legacy channel has no bound room are mapped to the room
-- with the same identifier, preserving the data.
UPDATE voice_recordings rec
SET room_id = rec.channel_id
WHERE room_id IS NULL;

ALTER TABLE voice_recordings
    ALTER COLUMN room_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_voice_recordings_room_id ON voice_recordings (room_id);

ALTER TABLE voice_recordings DROP COLUMN IF EXISTS channel_id;

-- ── e2ee_keys: channel_id -> room_id ──
ALTER TABLE e2ee_keys ADD COLUMN IF NOT EXISTS room_id VARCHAR(64);

UPDATE e2ee_keys k
SET room_id = vr.id
FROM voice_rooms vr
WHERE k.channel_id = vr.bind_channel_id;

UPDATE e2ee_keys
SET room_id = channel_id
WHERE room_id IS NULL;

ALTER TABLE e2ee_keys
    ALTER COLUMN room_id SET NOT NULL;

DROP INDEX IF EXISTS idx_e2ee_keys_channel_id;
CREATE UNIQUE INDEX IF NOT EXISTS idx_e2ee_keys_room_id ON e2ee_keys (room_id);

ALTER TABLE e2ee_keys DROP COLUMN IF EXISTS channel_id;

COMMIT;
