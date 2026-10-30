BEGIN;

-- DES-20261029-01（A9 二期）§五：线程级已读位表。
-- 线程未读 = 该 parent_id 下回复中 created_at > COALESCE(本表 read_at, 频道已读水位)
--            且 user_id != 本人。
-- 惰性建行：仅在用户对某线程 mark-read 时 upsert 一行，不为用户×线程预建行。
-- 双引擎兼容：不使用 JSONB / 方言特有类型；索引名与 GORM 模型 tag 一致。
CREATE TABLE IF NOT EXISTS message_thread_reads (
    id VARCHAR(64) PRIMARY KEY,
    user_id VARCHAR(64) NOT NULL,
    parent_id VARCHAR(64) NOT NULL,
    channel_id VARCHAR(64) NOT NULL,
    last_read_message_id VARCHAR(64) DEFAULT '',
    read_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_thread_read_user_parent
    ON message_thread_reads (user_id, parent_id);
CREATE INDEX IF NOT EXISTS idx_thread_read_user_channel
    ON message_thread_reads (user_id, channel_id);

COMMIT;
