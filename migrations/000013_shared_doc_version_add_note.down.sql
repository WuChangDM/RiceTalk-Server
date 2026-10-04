BEGIN;

ALTER TABLE shared_document_versions DROP COLUMN IF EXISTS note;

COMMIT;
