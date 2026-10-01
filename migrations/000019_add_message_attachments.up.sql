BEGIN;

CREATE TABLE IF NOT EXISTS message_attachments (
    id VARCHAR(64) PRIMARY KEY,
    message_id VARCHAR(64) NOT NULL,
    type VARCHAR(16) NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    file_size BIGINT NOT NULL,
    mime_type VARCHAR(64),
    file_url VARCHAR(512),
    thumb_url VARCHAR(512),
    width INT DEFAULT 0,
    height INT DEFAULT 0,
    cloud_file_id VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_message_attachments_message_id ON message_attachments(message_id);

COMMIT;
