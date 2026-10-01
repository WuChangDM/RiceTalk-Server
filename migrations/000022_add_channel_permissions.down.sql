-- Migration: add_channel_permissions (down)
-- Date: 2026-07-12

ALTER TABLE channels DROP COLUMN IF EXISTS permissions;
