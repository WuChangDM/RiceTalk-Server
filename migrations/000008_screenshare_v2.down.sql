BEGIN;

DROP INDEX IF EXISTS idx_screen_share_sessions_channel_id_status;

-- Remove V2 columns (idempotent).
ALTER TABLE screen_share_sessions DROP COLUMN IF EXISTS source_id;
ALTER TABLE screen_share_sessions DROP COLUMN IF EXISTS resolution;
ALTER TABLE screen_share_sessions DROP COLUMN IF EXISTS frame_rate;
ALTER TABLE screen_share_sessions DROP COLUMN IF EXISTS max_bitrate;
ALTER TABLE screen_share_sessions DROP COLUMN IF EXISTS share_audio;
ALTER TABLE screen_share_sessions DROP COLUMN IF EXISTS suppress_voice;
ALTER TABLE screen_share_sessions DROP COLUMN IF EXISTS max_viewers;
ALTER TABLE screen_share_sessions DROP COLUMN IF EXISTS viewer_count;

-- Restore nullable bind_channel_id only if the target does not already exist.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'screen_share_sessions'
          AND column_name = 'channel_id'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'screen_share_sessions'
          AND column_name = 'bind_channel_id'
    ) THEN
        ALTER TABLE screen_share_sessions RENAME COLUMN channel_id TO bind_channel_id;
        ALTER TABLE screen_share_sessions ALTER COLUMN bind_channel_id DROP NOT NULL;
    END IF;
END $$;

COMMIT;
