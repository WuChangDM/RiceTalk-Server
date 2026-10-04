BEGIN;

-- DES-20261001-01 §5.2「A4-S1」回滚：删除会话表的来源网络信息字段。
--
-- ip / user_agent 仅用于「登录设备」展示与异地登录提醒判定，不参与认证判定；
-- 回滚后仅丢失来源 IP/UA 展示与告警能力（security_alert 不再触发），
-- 登录态（token_hash）与会话有效性不受影响。

ALTER TABLE user_sessions DROP COLUMN IF EXISTS ip;
ALTER TABLE user_sessions DROP COLUMN IF EXISTS user_agent;

COMMIT;
