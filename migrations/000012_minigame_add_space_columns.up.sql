BEGIN;

-- Add columns required by GORM model MinigameSession that were missing from baseline.
ALTER TABLE minigame_sessions ADD COLUMN IF NOT EXISTS space_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE minigame_sessions ADD COLUMN IF NOT EXISTS spectators_json TEXT;
ALTER TABLE minigame_sessions ADD COLUMN IF NOT EXISTS last_joined_at TIMESTAMPTZ;

-- Backfill space_id from channel_id via channels table (mirrors 000006_bots_independent pattern).
UPDATE minigame_sessions
SET space_id = c.space_id
FROM channels c
WHERE minigame_sessions.channel_id = c.id
  AND minigame_sessions.space_id = '';

-- New index for space-scoped queries (model defines: game_type, space_id, status).
CREATE INDEX IF NOT EXISTS idx_minigame_space ON minigame_sessions (game_type, space_id, status);

COMMIT;
