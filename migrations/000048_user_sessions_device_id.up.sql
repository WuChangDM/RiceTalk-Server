BEGIN;

-- ── task-131：登录设备去重（同一台机器重复登录不再多出一台设备） ──────────
-- 背景：user_sessions 一行 = 一个 refresh token，旧实现每次登录无条件 INSERT，
-- 同一设备每次重登都会新增一行，「登录设备」列表因此越堆越多。
-- 方案：新增安装级稳定标识 device_id（客户端持久化的 UUIDv4），非空时
-- (user_id, device_id) 唯一——同一设备重登原地更新既有会话行。
--
-- 口径（Lead 拍板）：
--   1) device_id 为空串 = 老客户端未上报，行为保持 1.9.4 之前不变（每次登录新行），
--      因此唯一索引必须是部分索引（WHERE device_id <> ''），不能约束老行。
--   2) 历史行没有 device_id，无法按标识合并；按「用户 + 设备类型 + 设备名 + 完整 IP」
--      四元组合并（设备类型/设备名均非空才合并，避免把未上报设备的行混在一起），
--      保留 last_active_at 最大的一行（其 refresh token 即当前设备在用的那行），
--      其余 DELETE。被删行的 refresh token 随之失效——属可接受的代价：
--      这些行本来就是同一台设备的重复记录；未合并的旧行 7 天内自然过期。
--   3) 时间列一律 TIMESTAMPTZ（000044 的 8 小时时区教训）。
--
-- 注意：本迁移必须在服务端新代码上线前/同时执行；老代码不看 device_id，
-- 新列有 DEFAULT '' 因而对老代码完全透明。

ALTER TABLE user_sessions ADD COLUMN IF NOT EXISTS device_id VARCHAR(64) NOT NULL DEFAULT '';

-- 历史重复会话合并：先把每组（同用户+同设备类型+同设备名+同 IP，且 device_id 为空）
-- 中「存活最久」的时间回填到该组每一行，随后删除非最大的行。
-- 写法保持可移植（相关子查询，PG 与 SQLite 均可执行），便于单测在 SQLite 上验证。
UPDATE user_sessions
SET last_active_at = (
    SELECT MAX(k.last_active_at)
    FROM user_sessions AS k
    WHERE k.user_id = user_sessions.user_id
      AND k.device_type = user_sessions.device_type
      AND k.device_name = user_sessions.device_name
      AND k.ip = user_sessions.ip
      AND k.device_id = ''
)
WHERE device_id = ''
  AND device_type <> ''
  AND device_name <> ''
  AND EXISTS (
      SELECT 1
      FROM user_sessions AS k2
      WHERE k2.user_id = user_sessions.user_id
        AND k2.device_type = user_sessions.device_type
        AND k2.device_name = user_sessions.device_name
        AND k2.ip = user_sessions.ip
        AND k2.device_id = ''
        AND k2.last_active_at > user_sessions.last_active_at
  );

DELETE FROM user_sessions
WHERE device_id = ''
  AND device_type <> ''
  AND device_name <> ''
  AND EXISTS (
      SELECT 1
      FROM user_sessions AS keep
      WHERE keep.user_id = user_sessions.user_id
        AND keep.device_type = user_sessions.device_type
        AND keep.device_name = user_sessions.device_name
        AND keep.ip = user_sessions.ip
        AND keep.device_id = ''
        AND (
            keep.last_active_at > user_sessions.last_active_at
            OR (keep.last_active_at = user_sessions.last_active_at AND keep.id > user_sessions.id)
        )
  );

-- 部分唯一索引：只约束上报了设备标识的新会话行，老行（device_id = ''）不受影响。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_sessions_user_device
    ON user_sessions (user_id, device_id)
    WHERE device_id <> '';

COMMIT;
