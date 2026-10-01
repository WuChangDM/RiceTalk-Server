BEGIN;

-- 预设音效标记（软件自带，全员可用、不可删除）
ALTER TABLE user_sounds ADD COLUMN IF NOT EXISTS is_preset BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS idx_user_sounds_is_preset ON user_sounds(is_preset);

-- 音效收藏（用户可收藏任何人的音效）
CREATE TABLE IF NOT EXISTS user_sound_favorites (
    id         VARCHAR(64) PRIMARY KEY,
    user_id    VARCHAR(64) NOT NULL,
    sound_id   VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sound_fav_user_sound ON user_sound_favorites(user_id, sound_id);

COMMIT;
