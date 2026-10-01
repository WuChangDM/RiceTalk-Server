BEGIN;

-- Phase 2: comments and replies for shared documents.
CREATE TABLE IF NOT EXISTS shared_document_comments (
    id            VARCHAR(64) PRIMARY KEY,
    document_id   VARCHAR(64) NOT NULL,
    parent_id     VARCHAR(64),
    anchor_text   TEXT,
    anchor_offset INTEGER DEFAULT 0,
    anchor_length INTEGER DEFAULT 0,
    content       TEXT NOT NULL,
    created_by    VARCHAR(64) NOT NULL,
    resolved      BOOLEAN NOT NULL DEFAULT FALSE,
    resolved_by   VARCHAR(64),
    resolved_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_shared_document_comments_document_id
    ON shared_document_comments(document_id);
CREATE INDEX IF NOT EXISTS idx_shared_document_comments_parent_id
    ON shared_document_comments(parent_id);
CREATE INDEX IF NOT EXISTS idx_shared_document_comments_deleted_at
    ON shared_document_comments(deleted_at);

-- Keep production migrations authoritative when AutoMigrate is disabled.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.shared_document_comments'::regclass
          AND conname = 'fk_shared_document_comments_document'
    ) THEN
        ALTER TABLE shared_document_comments
            ADD CONSTRAINT fk_shared_document_comments_document
            FOREIGN KEY (document_id) REFERENCES shared_documents(id) ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.shared_document_comments'::regclass
          AND conname = 'fk_shared_document_comments_parent'
    ) THEN
        ALTER TABLE shared_document_comments
            ADD CONSTRAINT fk_shared_document_comments_parent
            FOREIGN KEY (parent_id) REFERENCES shared_document_comments(id) ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.shared_document_comments'::regclass
          AND conname = 'fk_shared_document_comments_created_by'
    ) THEN
        ALTER TABLE shared_document_comments
            ADD CONSTRAINT fk_shared_document_comments_created_by
            FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.shared_document_comments'::regclass
          AND conname = 'fk_shared_document_comments_resolved_by'
    ) THEN
        ALTER TABLE shared_document_comments
            ADD CONSTRAINT fk_shared_document_comments_resolved_by
            FOREIGN KEY (resolved_by) REFERENCES users(id) ON DELETE SET NULL;
    END IF;
END;
$$;

COMMIT;
