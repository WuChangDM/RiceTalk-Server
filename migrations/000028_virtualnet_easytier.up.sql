-- DES-2026-0731-02: 虚拟局域网从 WireGuard+Headscale 切换到 EasyTier
-- 1. virtual_net_sessions 表：移除 network_cidr 字段，新增 network_name 字段
-- 2. 重置所有会话和节点状态（旧 Headscale 会话已失效）
-- 3. 新增 network_name 字段索引（用于按网络名查询会话）

-- 移除旧的 network_cidr 字段（存储子网 CIDR，EasyTier 模式下不再需要）
ALTER TABLE virtual_net_sessions DROP COLUMN IF EXISTS network_cidr;

-- 新增 network_name 字段（EasyTier 网络名称，客户端通过相同网络名加入同一虚拟网络）
ALTER TABLE virtual_net_sessions ADD COLUMN network_name VARCHAR(64);

-- 创建 network_name 索引（用于按网络名查询会话）
CREATE INDEX IF NOT EXISTS idx_virtual_net_sessions_network_name
    ON virtual_net_sessions(network_name);

-- 重置所有旧会话状态（Headscale 会话已失效，需重新连接）
UPDATE virtual_net_sessions SET status = 'disconnected' WHERE status = 'connected';

-- 重置所有旧节点状态
UPDATE virtual_net_nodes SET status = 'inactive' WHERE status = 'active';

-- 清空旧的 IP 字段（EasyTier 模式下 IP 由客户端连接后回填）
UPDATE virtual_net_sessions SET ip = '' WHERE ip IS NOT NULL AND ip != '';
UPDATE virtual_net_nodes SET ip = '' WHERE ip IS NOT NULL AND ip != '';

-- 清空旧的 NodeID 字段（EasyTier 模式下 NodeID 即节点名称 rrt-<username>）
UPDATE virtual_net_sessions SET node_id = '' WHERE node_id != '';
