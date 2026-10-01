BEGIN;

DROP INDEX IF EXISTS idx_minigame_space;
ALTER TABLE minigame_sessions DROP COLUMN IF EXISTS last_joined_at;
ALTER TABLE minigame_sessions DROP COLUMN IF EXISTS spectators_json;
ALTER TABLE minigame_sessions DROP COLUMN IF EXISTS space_id;

COMMIT;
