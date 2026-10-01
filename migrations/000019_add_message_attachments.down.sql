BEGIN;

DROP INDEX IF EXISTS idx_message_attachments_message_id;
DROP TABLE IF EXISTS message_attachments;

COMMIT;
