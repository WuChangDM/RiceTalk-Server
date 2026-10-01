BEGIN;

-- 为 admin_configs 表添加 LiveKit TCP/UDP 端口字段
-- 用于管理后台端口配置面板（admin-port-config-panel spec）
-- 允许 NULL，应用层读取时若为 0/NULL 回退到默认值（7881/7882），不影响现有数据
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS live_kit_tcp_port BIGINT;
ALTER TABLE admin_configs ADD COLUMN IF NOT EXISTS live_kit_udp_port BIGINT;

COMMIT;
