BEGIN;

-- Re-add the foreign key constraint. Only safe to re-apply after backfilling all
-- empty-string folder_id rows to NULL or a valid shared_folders.id.
ALTER TABLE shared_file_entries
    ADD CONSTRAINT fk_shared_file_entries_folder
    FOREIGN KEY (folder_id) REFERENCES shared_folders(id) ON DELETE CASCADE;

COMMIT;
