BEGIN;

-- Add note column to shared_document_versions to align with GORM model
-- SharedDocumentVersion.Note (Phase 2: version note).
-- Production DB was missing this column, causing PATCH /api/sharedoc/:id to 500.
ALTER TABLE shared_document_versions ADD COLUMN IF NOT EXISTS note TEXT;

COMMIT;
