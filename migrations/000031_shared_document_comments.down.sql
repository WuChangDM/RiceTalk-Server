BEGIN;

-- This rollback is intended for an empty/test database only. Production
-- rollback must keep user comments and restore the previous application code.
DROP TABLE IF EXISTS shared_document_comments;

COMMIT;
