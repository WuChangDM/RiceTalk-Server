BEGIN;

-- Screen share sessions: migrate from channel-scoped to space-scoped independent sessions.
ALTER TABLE screen_share_sessions ADD COLUMN space_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE screen_share_sessions ADD COLUMN bind_channel_id VARCHAR(64);

-- Backfill space_id from the session's original channel.
UPDATE screen_share_sessions
SET space_id = channels.space_id
FROM channels
WHERE screen_share_sessions.channel_id = channels.id;

-- Backfill bind_channel_id with the original channel so existing notifications keep working.
UPDATE screen_share_sessions SET bind_channel_id = channel_id;

ALTER TABLE screen_share_sessions DROP COLUMN channel_id;

CREATE INDEX IF NOT EXISTS idx_screen_share_sessions_space_id ON screen_share_sessions(space_id);
CREATE INDEX IF NOT EXISTS idx_screen_share_sessions_bind_channel_id ON screen_share_sessions(bind_channel_id);

COMMIT;
