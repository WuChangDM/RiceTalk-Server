BEGIN;

-- Screen share V2: bind sessions to a voice channel and add capture/source settings.
-- This migration is idempotent and tolerates databases that were created or
-- already upgraded by GORM AutoMigrate (which creates channel_id directly).

DO $$
BEGIN
    -- Only rename bind_channel_id -> channel_id if the legacy column exists and
    -- the target column does not. This handles databases that ran 000007.
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'screen_share_sessions'
          AND column_name = 'bind_channel_id'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'screen_share_sessions'
          AND column_name = 'channel_id'
    ) THEN
        ALTER TABLE screen_share_sessions RENAME COLUMN bind_channel_id TO channel_id;
    END IF;
END $$;

-- Ensure channel_id is non-null. Use a safe default for any legacy NULL rows.
UPDATE screen_share_sessions SET channel_id = COALESCE(channel_id, '') WHERE channel_id IS NULL;
ALTER TABLE screen_share_sessions ALTER COLUMN channel_id SET NOT NULL;

-- New capture/source and quality columns (idempotent).
ALTER TABLE screen_share_sessions ADD COLUMN IF NOT EXISTS source_id VARCHAR(128);
ALTER TABLE screen_share_sessions ADD COLUMN IF NOT EXISTS resolution VARCHAR(16);
ALTER TABLE screen_share_sessions ADD COLUMN IF NOT EXISTS frame_rate INTEGER;
ALTER TABLE screen_share_sessions ADD COLUMN IF NOT EXISTS max_bitrate INTEGER;
ALTER TABLE screen_share_sessions ADD COLUMN IF NOT EXISTS share_audio BOOLEAN DEFAULT FALSE;
ALTER TABLE screen_share_sessions ADD COLUMN IF NOT EXISTS suppress_voice BOOLEAN DEFAULT FALSE;
ALTER TABLE screen_share_sessions ADD COLUMN IF NOT EXISTS max_viewers INTEGER DEFAULT 50;
ALTER TABLE screen_share_sessions ADD COLUMN IF NOT EXISTS viewer_count INTEGER DEFAULT 0;

-- Index for channel-scoped active session lookups.
CREATE INDEX IF NOT EXISTS idx_screen_share_sessions_channel_id_status ON screen_share_sessions(channel_id, status);

COMMIT;
