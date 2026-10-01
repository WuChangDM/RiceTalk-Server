BEGIN;

-- T49: persisted remote-assist request and authorization state.
CREATE TABLE IF NOT EXISTS remote_assist_sessions (
    id           VARCHAR(64) PRIMARY KEY,
    requester_id VARCHAR(64) NOT NULL,
    target_id    VARCHAR(64) NOT NULL,
    channel_id   VARCHAR(64) NOT NULL,
    status       VARCHAR(16) NOT NULL DEFAULT 'pending',
    permissions  TEXT NOT NULL DEFAULT '{}',
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_remote_assist_sessions_requester_id
    ON remote_assist_sessions(requester_id);
CREATE INDEX IF NOT EXISTS idx_remote_assist_sessions_target_id
    ON remote_assist_sessions(target_id);
CREATE INDEX IF NOT EXISTS idx_remote_assist_sessions_channel_id
    ON remote_assist_sessions(channel_id);
CREATE INDEX IF NOT EXISTS idx_remote_assist_sessions_status
    ON remote_assist_sessions(status);
CREATE INDEX IF NOT EXISTS idx_remote_assist_sessions_expires_at
    ON remote_assist_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_remote_assist_sessions_requester_status
    ON remote_assist_sessions(requester_id, status);
CREATE INDEX IF NOT EXISTS idx_remote_assist_sessions_target_status
    ON remote_assist_sessions(target_id, status);

-- Keep production migrations authoritative when AutoMigrate is disabled.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.remote_assist_sessions'::regclass
          AND conname = 'fk_remote_assist_sessions_requester'
    ) THEN
        ALTER TABLE remote_assist_sessions
            ADD CONSTRAINT fk_remote_assist_sessions_requester
            FOREIGN KEY (requester_id) REFERENCES users(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.remote_assist_sessions'::regclass
          AND conname = 'fk_remote_assist_sessions_target'
    ) THEN
        ALTER TABLE remote_assist_sessions
            ADD CONSTRAINT fk_remote_assist_sessions_target
            FOREIGN KEY (target_id) REFERENCES users(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.remote_assist_sessions'::regclass
          AND conname = 'fk_remote_assist_sessions_channel'
    ) THEN
        ALTER TABLE remote_assist_sessions
            ADD CONSTRAINT fk_remote_assist_sessions_channel
            FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;
    END IF;
END;
$$;

COMMIT;
