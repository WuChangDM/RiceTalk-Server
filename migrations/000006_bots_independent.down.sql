BEGIN;

-- bot_tts_messages: restore channel-scoped key.
ALTER TABLE bot_tts_messages RENAME COLUMN bot_id TO channel_id;

-- bot_player_states: restore channel-scoped key.
ALTER TABLE bot_player_states RENAME COLUMN bot_id TO channel_id;

-- bot_play_queues: restore channel-scoped key.
ALTER TABLE bot_play_queues RENAME COLUMN bot_id TO channel_id;

-- bots: restore channel_id and drop space/output-room columns.
ALTER TABLE bots ADD COLUMN channel_id VARCHAR(64) NOT NULL DEFAULT '';
UPDATE bots SET channel_id = COALESCE(output_room_id, '');
ALTER TABLE bots DROP COLUMN space_id;
ALTER TABLE bots DROP COLUMN output_room_id;

DROP INDEX IF EXISTS idx_bots_space_id;
DROP INDEX IF EXISTS idx_bots_output_room_id;

COMMIT;
