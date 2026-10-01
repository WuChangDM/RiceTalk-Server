BEGIN;

-- Drop NOT NULL constraint on shared_file_entries.folder_id.
-- The GORM model SharedFileEntry.FolderID is a plain string (empty for root uploads),
-- but the DB schema declared it NOT NULL, causing POST /api/cloudfs/upload (root path)
-- to 500 when folderID is the empty string.
ALTER TABLE shared_file_entries ALTER COLUMN folder_id DROP NOT NULL;

COMMIT;
