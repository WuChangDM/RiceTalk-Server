BEGIN;

-- 回滚：删除 LiveKit TCP/UDP 端口字段
ALTER TABLE admin_configs DROP COLUMN IF EXISTS live_kit_tcp_port;
ALTER TABLE admin_configs DROP COLUMN IF EXISTS live_kit_udp_port;

COMMIT;
