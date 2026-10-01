BEGIN;

DROP TABLE IF EXISTS user_sound_favorites;
DROP INDEX IF EXISTS idx_user_sounds_is_preset;
ALTER TABLE user_sounds DROP COLUMN IF EXISTS is_preset;

COMMIT;
