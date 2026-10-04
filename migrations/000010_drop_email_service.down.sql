-- Rollback: drop_email_service
-- Date: 2026-06-25
-- Description: 回滚 - 重建邮箱服务相关数据库对象
-- Spec: .trae/specs/remove-email-service/
-- 字段类型参考 000001_baseline.up.sql，确保与 baseline 一致

-- 重建密码重置令牌表（与 000001_baseline.up.sql 完全对齐）
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id                   VARCHAR(64) PRIMARY KEY,
    user_id              VARCHAR(64) NOT NULL,
    token                VARCHAR(255) NOT NULL,
    token_hash           VARCHAR(64) NOT NULL,
    email_code           VARCHAR(64),
    email_code_used      BOOLEAN DEFAULT FALSE,
    email_code_used_at   TIMESTAMPTZ,
    login_challenge      VARCHAR(255),
    expires_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used                 BOOLEAN DEFAULT FALSE,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user_id ON password_reset_tokens (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_password_reset_tokens_token ON password_reset_tokens (token);
CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_token_hash ON password_reset_tokens (token_hash);

-- 重建 admin_configs 表的 SMTP 配置字段
-- smtp_port / smtp_password 类型严格对齐 baseline（BIGINT / VARCHAR(255)）
-- smtp_verified / smtp_verified_at 不在 baseline，但存在于 model.go（运行时由 GORM AutoMigrate 维护），
-- 为保持 up/down 对称一并重建
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS smtp_host        VARCHAR(255);
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS smtp_port        BIGINT;
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS smtp_user        VARCHAR(255);
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS smtp_password    VARCHAR(255);
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS smtp_from        VARCHAR(255);
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS smtp_enable      BOOLEAN DEFAULT FALSE;
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS smtp_verified    BOOLEAN DEFAULT FALSE;
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS smtp_verified_at TIMESTAMPTZ;
