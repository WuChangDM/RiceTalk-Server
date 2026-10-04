BEGIN;

-- DES-20261002-01 §4.7「B7 自定义服务器表情·轻量版」— 回滚 000041。
--
-- 该表只存空间自定义表情索引，表情文件本体在 internal/storage（emojis/ 前缀）
-- 由部署方按需清理；回滚后 `:name:` 自定义表情渲染失效，消息原文不受影响。

DROP TABLE IF EXISTS server_emojis;

COMMIT;
