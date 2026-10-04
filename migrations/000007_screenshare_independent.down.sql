BEGIN;

-- Restore channel_id and drop space/bind-channel columns.
ALTER TABLE screen_share_sessions ADD COLUMN channel_id VARCHAR(64) NOT NULL DEFAULT '';

-- Backfill channel_id from the optional bind channel.
UPDATE screen_share_sessions SET channel_id = COALESCE(bind_channel_id, '');

ALTER TABLE screen_share_sessions DROP COLUMN space_id;
ALTER TABLE screen_share_sessions DROP COLUMN bind_channel_id;

DROP INDEX IF EXISTS idx_screen_share_sessions_space_id;
DROP INDEX IF EXISTS idx_screen_share_sessions_bind_channel_id;

COMMIT;
