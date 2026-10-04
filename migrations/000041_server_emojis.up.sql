BEGIN;

-- DES-20261002-01 §4.7「B7 自定义服务器表情·轻量版」— 空间自定义表情表。
--
-- 设计要点：
--   * 表情属于空间（space_id）：仅该空间成员可见可用，`:name:` 渲染在客户端，
--     服务端只存原文，不参与消息渲染；
--   * name 限 [a-z0-9_]{2,32}，空间内唯一（唯一索引 idx_server_emoji_space_name），
--     冲突时 API 返回 409 EMOJI_NAME_TAKEN；
--   * file_id 是 internal/storage 的对象 key（emojis/ 前缀，PNG/GIF/WebP ≤128KB），
--     内容写入后不可变，同一 id 永远指向同一字节流；
--   * 物理删除（无 deleted_at 列）：对齐白板（whiteboards）无软删的惯例，
--     删除时 API 层同步删除存储对象；
--   * idx_server_emoji_creator_id 支撑「按创建者排查」的管理查询。
--
-- 时间列统一使用 TIMESTAMPTZ，与 000036/000040 等近期迁移一致。

CREATE TABLE server_emojis (
    id         VARCHAR(64)  PRIMARY KEY,
    space_id   VARCHAR(64)  NOT NULL,
    name       VARCHAR(32)  NOT NULL,
    file_id    VARCHAR(256) NOT NULL,
    creator_id VARCHAR(64)  NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_server_emoji_space_name ON server_emojis(space_id, name);
CREATE INDEX idx_server_emoji_creator_id ON server_emojis(creator_id);

COMMIT;
