-- Migration: drop_email_service
-- Date: 2026-06-25
-- Description: 删除邮箱服务相关数据库对象
-- Spec: .trae/specs/remove-email-service/

-- 删除密码重置令牌表（邮箱验证码找回密码流程已删除）
DROP TABLE IF EXISTS password_reset_tokens CASCADE;

-- 删除 admin_configs 表的 SMTP 配置字段
ALTER TABLE admin_configs DROP COLUMN IF EXISTS smtp_host;
ALTER TABLE admin_configs DROP COLUMN IF EXISTS smtp_port;
ALTER TABLE admin_configs DROP COLUMN IF EXISTS smtp_user;
ALTER TABLE admin_configs DROP COLUMN IF EXISTS smtp_password;
ALTER TABLE admin_configs DROP COLUMN IF EXISTS smtp_from;
ALTER TABLE admin_configs DROP COLUMN IF EXISTS smtp_enable;
ALTER TABLE admin_configs DROP COLUMN IF EXISTS smtp_verified;
ALTER TABLE admin_configs DROP COLUMN IF EXISTS smtp_verified_at;
