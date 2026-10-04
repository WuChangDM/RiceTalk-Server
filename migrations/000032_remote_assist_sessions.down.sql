BEGIN;

-- This rollback is intended for an empty/test database only. Do not remove
-- production sessions after a release has started writing them.
DROP TABLE IF EXISTS remote_assist_sessions;

COMMIT;
