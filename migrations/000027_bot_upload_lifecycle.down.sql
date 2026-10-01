DROP INDEX IF EXISTS idx_bot_upload_audios_pending_delete;
DROP INDEX IF EXISTS idx_bot_upload_audios_last_played_at;

ALTER TABLE bot_upload_audios DROP COLUMN pending_delete;
ALTER TABLE bot_upload_audios DROP COLUMN last_played_at;
