BEGIN;

-- Re-apply NOT NULL constraint. Any NULL/empty rows must be backfilled first
-- (e.g. UPDATE minigame_sessions SET channel_id = 'legacy' WHERE channel_id IS NULL OR channel_id = '';)
-- before this constraint can be re-applied cleanly.
ALTER TABLE minigame_sessions ALTER COLUMN channel_id SET NOT NULL;

COMMIT;
