BEGIN;

-- Re-apply NOT NULL constraint. Any NULL folder_id rows must be backfilled first
-- (e.g. UPDATE shared_file_entries SET folder_id = '' WHERE folder_id IS NULL;)
-- before this constraint can be re-applied cleanly.
ALTER TABLE shared_file_entries ALTER COLUMN folder_id SET NOT NULL;

COMMIT;
