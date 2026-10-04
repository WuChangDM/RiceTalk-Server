BEGIN;

-- DES-20261001-01 §5.2「A4-S1：下线其他设备 + 异地登录提醒」— 会话表补充来源网络信息。
--
-- 设计要点：
--   * ip / user_agent 由服务端在登录与 refresh 时自动采集（ip = ClientIP，
--     user_agent = 请求头 User-Agent 截断存储），客户端不可自报；
--   * ip 列宽 45 字符（IPv6 文本形式上限），user_agent 按 rune 截断 256 后
--     UTF-8 编码仍不超列宽；
--   * 用途有二：GET /auth/sessions 的「登录设备」列表展示来源 IP（脱敏由
--     客户端完成，端点本身只返回会话归属者本人可见的数据）；新登录时与既有
--     活跃会话比对网段（IPv4 /24、IPv6 /64）判定是否触发 security_alert
--     「异地登录提醒」广播；
--   * 老会话行保持空串（视为「未知网段」，首次异网登录最多告警一次，可接受）；
--   * SQLite（开发/测试走 AutoMigrate，RRT_FORCE_AUTOMIGRATE=1）与 PostgreSQL 语义一致。

ALTER TABLE user_sessions ADD COLUMN ip VARCHAR(45) NOT NULL DEFAULT '';
ALTER TABLE user_sessions ADD COLUMN user_agent VARCHAR(256) NOT NULL DEFAULT '';

COMMIT;
