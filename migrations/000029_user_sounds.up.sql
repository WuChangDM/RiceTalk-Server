BEGIN;

CREATE TABLE IF NOT EXISTS user_sounds (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    title       VARCHAR(256) NOT NULL DEFAULT '',
    file_path   VARCHAR(512) NOT NULL DEFAULT '',
    file_size   BIGINT NOT NULL DEFAULT 0,
    mime_type   VARCHAR(64) NOT NULL DEFAULT '',
    duration    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_user_sounds_user_id ON user_sounds(user_id);
CREATE INDEX IF NOT EXISTS idx_user_sounds_deleted_at ON user_sounds(deleted_at);

CREATE TABLE IF NOT EXISTS user_sound_settings (
    user_id        VARCHAR(64) PRIMARY KEY,
    join_sound_id  VARCHAR(64),
    leave_sound_id VARCHAR(64),
    join_volume    INTEGER NOT NULL DEFAULT 100,
    leave_volume   INTEGER NOT NULL DEFAULT 100,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMIT;
