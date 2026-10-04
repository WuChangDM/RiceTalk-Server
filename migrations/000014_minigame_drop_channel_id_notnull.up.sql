BEGIN;

-- Drop NOT NULL constraint on minigame_sessions.channel_id.
-- The GORM model MinigameSession no longer has a ChannelID field (migration 000006
-- moved bots/queues to be bot-scoped, and minigame sessions are now space-scoped).
-- Handler creates new rooms without setting channel_id, which violates the legacy
-- NOT NULL constraint and causes POST /api/v1/minigames/join to 500.
ALTER TABLE minigame_sessions ALTER COLUMN channel_id DROP NOT NULL;

COMMIT;
