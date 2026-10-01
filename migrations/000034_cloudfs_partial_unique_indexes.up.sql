BEGIN;

-- DES-2026-0912-02 §2.5 / §6 — rebuild the CloudFS unique indexes as PARTIAL
-- unique indexes.
--
-- Why: rows in shared_file_entries / shared_folders are deleted with GORM soft
-- delete (deleted_at is set, the row survives), but the unique indexes created
-- by 000001_baseline covered every row regardless of deleted_at. Re-uploading or
-- re-creating an item with the same name in the same parent therefore failed
-- with a unique-constraint violation (HTTP 409 SYSTEM_CONFLICT) even though the
-- previous incarnation is invisible to every query.
--
-- Effect: only LIVE rows (deleted_at IS NULL) participate in name uniqueness.
-- No column or data change; rollback is possible while no soft-deleted duplicate
-- names exist (see the .down.sql for the cleanup precondition).

-- Idempotent: DROP INDEX IF EXISTS + CREATE ... IF NOT EXISTS make this safe to
-- re-run against a database where the partial index is already in place.

DROP INDEX IF EXISTS idx_file_folder_name;
CREATE UNIQUE INDEX IF NOT EXISTS idx_file_folder_name
    ON shared_file_entries (folder_id, file_name)
    WHERE deleted_at IS NULL;

DROP INDEX IF EXISTS idx_folder_parent_name;
CREATE UNIQUE INDEX IF NOT EXISTS idx_folder_parent_name
    ON shared_folders (parent_id, name)
    WHERE deleted_at IS NULL;

COMMIT;
