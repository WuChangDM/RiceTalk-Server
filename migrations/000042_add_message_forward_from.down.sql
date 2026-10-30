BEGIN;

-- 消息转发（一期）回滚：删除 messages.forward_from 列。
--
-- 该列仅为转发来源显示名快照（展示性元数据），删除后消息正文与附件不受影响，
-- 仅历史转发消息的「转发自 xxx」标识不再展示。

ALTER TABLE messages DROP COLUMN IF EXISTS forward_from;

COMMIT;
