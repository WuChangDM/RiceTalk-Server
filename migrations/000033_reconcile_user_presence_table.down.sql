BEGIN;

-- Intentionally a no-op. Presence rows are durable user state and must not be
-- deleted or renamed speculatively during rollback. Restore the pre-migration
-- database snapshot when reversing a table-name reconciliation.

COMMIT;
