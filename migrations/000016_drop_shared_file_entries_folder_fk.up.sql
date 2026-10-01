BEGIN;

-- Drop the foreign key constraint on shared_file_entries.folder_id.
-- Root-path uploads set folder_id to the empty string (GORM string zero value),
-- which violates the FK constraint (empty string is not present in shared_folders.id
-- and is not NULL). Removing the FK allows root uploads to succeed while keeping
-- folder_id as a plain string column. Folder-level uploads still resolve the folder
-- ID via service.go's explicit lookup, so referential integrity is enforced in
-- application code.
ALTER TABLE shared_file_entries DROP CONSTRAINT IF EXISTS fk_shared_file_entries_folder;

COMMIT;
