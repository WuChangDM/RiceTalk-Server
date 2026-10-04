-- DES-2026-0731-02 回滚：从 EasyTier 恢复为 Headscale（仅用于回滚，不恢复数据）

-- 移除 network_name 字段及其索引
DROP INDEX IF EXISTS idx_virtual_net_sessions_network_name;
ALTER TABLE virtual_net_sessions DROP COLUMN IF EXISTS network_name;

-- 恢复 network_cidr 字段（旧 Headscale 模式使用）
ALTER TABLE virtual_net_sessions ADD COLUMN network_cidr VARCHAR(64);

-- 注意：回滚后旧会话数据无法恢复，需用户重新连接 Headscale
