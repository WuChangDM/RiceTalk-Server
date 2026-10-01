ALTER TABLE bot_upload_audios ADD COLUMN last_played_at TIMESTAMP;
ALTER TABLE bot_upload_audios ADD COLUMN pending_delete BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_bot_upload_audios_last_played_at
    ON bot_upload_audios(last_played_at);
CREATE INDEX IF NOT EXISTS idx_bot_upload_audios_pending_delete
    ON bot_upload_audios(pending_delete);
