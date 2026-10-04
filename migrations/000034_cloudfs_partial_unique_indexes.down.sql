BEGIN;

-- DES-2026-0912-02 §6 — revert 000034: restore FULL unique indexes on
-- shared_file_entries(folder_id, file_name) and shared_folders(parent_id, name).
--
-- Precondition: the partial index allowed several rows with the same
-- (parent, name) to coexist as long as at most one of them was live. A full
-- unique index cannot represent that state, so the redundant SOFT-DELETED rows
-- (deleted_at IS NOT NULL) are physically removed first.
--
-- This is safe by construction: CloudFS deletes are GORM soft deletes whose
-- physical content was already os.Remove'd (service.go deleteFileEntry /
-- deleteFolder), so these rows are inert tombstones with no recoverable content.
-- Only duplicates are removed — the newest row per key is kept.
--
-- Order matters: dedupe first, otherwise the CREATE below fails mid-migration
-- and leaves the schema_migrations row dirty.

-- (1) Drop tombstones that shadow a LIVE row with the same name.
DELETE FROM shared_file_entries d
 WHERE d.deleted_at IS NOT NULL
   AND EXISTS (
       SELECT 1 FROM shared_file_entries l
        WHERE l.deleted_at IS NULL
          AND l.folder_id = d.folder_id
          AND l.file_name = d.file_name
   );

DELETE FROM shared_folders d
 WHERE d.deleted_at IS NOT NULL
   AND d.parent_id IS NOT NULL
   AND EXISTS (
       SELECT 1 FROM shared_folders l
        WHERE l.deleted_at IS NULL
          AND l.parent_id = d.parent_id
          AND l.name = d.name
   );

-- (2) Collapse the remaining duplicate tombstones, keeping the newest per key.
DELETE FROM shared_file_entries
 WHERE deleted_at IS NOT NULL
   AND id NOT IN (
       SELECT DISTINCT ON (folder_id, file_name) id
         FROM shared_file_entries
        WHERE deleted_at IS NOT NULL
        ORDER BY folder_id, file_name, created_at DESC, id DESC
   );

DELETE FROM shared_folders
 WHERE deleted_at IS NOT NULL
   AND parent_id IS NOT NULL
   AND id NOT IN (
       SELECT DISTINCT ON (parent_id, name) id
         FROM shared_folders
        WHERE deleted_at IS NOT NULL
          AND parent_id IS NOT NULL
        ORDER BY parent_id, name, created_at DESC, id DESC
   );

DROP INDEX IF EXISTS idx_file_folder_name;
CREATE UNIQUE INDEX IF NOT EXISTS idx_file_folder_name
    ON shared_file_entries (folder_id, file_name);

DROP INDEX IF EXISTS idx_folder_parent_name;
CREATE UNIQUE INDEX IF NOT EXISTS idx_folder_parent_name
    ON shared_folders (parent_id, name);

COMMIT;
