-- Migration: add_channel_permissions
-- Date: 2026-07-12
-- Description: 为 channels 表添加 permissions 列，与 GORM Channel 模型保持一致
-- Design: 文档/设计/DES-2026-0712-01-fix-default-channel-creation.md

ALTER TABLE channels ADD COLUMN IF NOT EXISTS permissions TEXT DEFAULT '';
