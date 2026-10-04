BEGIN;

-- bots: migrate from channel-scoped to space-scoped independent bot instances.
ALTER TABLE bots ADD COLUMN space_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE bots ADD COLUMN output_room_id VARCHAR(64);

-- Backfill space_id from the bot's original channel.
UPDATE bots
SET space_id = channels.space_id
FROM channels
WHERE bots.channel_id = channels.id;

ALTER TABLE bots DROP COLUMN channel_id;

CREATE INDEX IF NOT EXISTS idx_bots_space_id ON bots(space_id);
CREATE INDEX IF NOT EXISTS idx_bots_output_room_id ON bots(output_room_id);

-- bot_play_queues: key queue items by bot instead of channel.
ALTER TABLE bot_play_queues RENAME COLUMN channel_id TO bot_id;

-- bot_player_states: key player state by bot instead of channel.
ALTER TABLE bot_player_states RENAME COLUMN channel_id TO bot_id;

-- bot_tts_messages: key TTS messages by bot instead of channel.
ALTER TABLE bot_tts_messages RENAME COLUMN channel_id TO bot_id;

COMMIT;
