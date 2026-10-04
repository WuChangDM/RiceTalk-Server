-- Migration: drop_mfa_tables
-- Date: 2026-06-26
-- Description: 移除 MFA/两步验证功能：删除 MFA 相关表和字段
-- Spec: .trae/specs/remove-mfa-feature/

-- 删除 MFA 密钥表（TOTP 密钥存储）
DROP TABLE IF EXISTS mfa_secrets CASCADE;

-- 删除 TOTP 恢复码表
DROP TABLE IF EXISTS totp_recovery_codes CASCADE;

-- 删除 users 表的 mfa_enabled 字段
ALTER TABLE users DROP COLUMN IF EXISTS mfa_enabled;
