BEGIN;

-- DES-2026-0912-05 §2 — 云文件「分享链接」表。
--
-- 设计要点（与安全直接相关，改动前请先读设计文档）：
--   * token_hash 只存 SHA-256(token)，不存明文 token；
--   * expires_at 必填（无「永久链接」形态）；
--   * max_downloads = 0 表示不限次数，否则下载时用原子 UPDATE 判定并递增
--     download_count（见 cloudfs.Service.ClaimShareDownload）；
--   * revoked_at 一旦置位即永久失效，不提供「复活」路径；
--   * 本表只承载「单文件」分享，不做文件夹分享（§1）。
--
-- 时间列统一使用 TIMESTAMPTZ：下载判定依赖 expires_at > now 的比较，
-- PostgreSQL 与 SQLite（开发/测试走 AutoMigrate）对 UTC 时间的比较语义一致。

CREATE TABLE IF NOT EXISTS cloud_file_shares (
    id             VARCHAR(64)  PRIMARY KEY,
    file_id        VARCHAR(64)  NOT NULL,
    space_id       VARCHAR(64)  NOT NULL,
    owner_id       VARCHAR(64)  NOT NULL,
    token_hash     VARCHAR(64)  NOT NULL,
    token_prefix   VARCHAR(16)  NOT NULL DEFAULT '',
    password_hash  VARCHAR(255) NOT NULL DEFAULT '',
    expires_at     TIMESTAMPTZ  NOT NULL,
    max_downloads  INTEGER      NOT NULL DEFAULT 0,
    download_count INTEGER      NOT NULL DEFAULT 0,
    revoked_at     TIMESTAMPTZ,
    last_access_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- 索引名与 GORM 模型 tag 中的名字保持一致，避免启用 AutoMigrate 的环境
-- （RRT_FORCE_AUTOMIGRATE=1）再建一份同义索引。
CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_file_shares_token_hash ON cloud_file_shares(token_hash);
CREATE INDEX IF NOT EXISTS idx_cloud_file_shares_file_id ON cloud_file_shares(file_id);
CREATE INDEX IF NOT EXISTS idx_cloud_file_shares_space_id ON cloud_file_shares(space_id);
CREATE INDEX IF NOT EXISTS idx_cloud_file_shares_owner_id ON cloud_file_shares(owner_id);
CREATE INDEX IF NOT EXISTS idx_cloud_file_shares_expires_at ON cloud_file_shares(expires_at);

COMMIT;
