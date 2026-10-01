-- Rollback: drop_mfa_tables
-- Date: 2026-06-26
-- Description: 回滚 - 重建 MFA 相关表和字段（数据无法恢复）
-- Spec: .trae/specs/remove-mfa-feature/
-- 字段类型参考 000001_baseline.up.sql，确保与 baseline 一致

-- 重建 users 表的 mfa_enabled 字段
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_enabled BOOLEAN DEFAULT FALSE;

-- 重建 MFA 密钥表（与 000001_baseline.up.sql 完全对齐）
CREATE TABLE IF NOT EXISTS mfa_secrets (
    id               VARCHAR(64) PRIMARY KEY,
    user_id          VARCHAR(64) NOT NULL,
    secret           VARCHAR(64) NOT NULL,
    enabled          BOOLEAN DEFAULT FALSE,
    login_challenge  VARCHAR(255),
    failed_attempts  BIGINT DEFAULT 0,
    locked_until     TIMESTAMPTZ,
    last_failed_at   TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_mfa_secrets_user_id ON mfa_secrets (user_id);

-- 重建 TOTP 恢复码表（与 000001_baseline.up.sql 完全对齐）
CREATE TABLE IF NOT EXISTS totp_recovery_codes (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     VARCHAR(64) NOT NULL,
    code_hash   VARCHAR(255) NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_totp_recovery_codes_user_id ON totp_recovery_codes (user_id);
