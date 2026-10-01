BEGIN;

-- DES-20261001-01 §9.2「A8-S1」回滚：删除邀请表与事件受邀人快照列。
--
-- 回滚后 RSVP 状态与受邀人快照全部丢失（事件本体不受影响）；
-- 客户端对无邀请事件按普通事件渲染，行为不受影响。

ALTER TABLE schedule_events DROP COLUMN IF EXISTS invitee_ids;
DROP TABLE IF EXISTS schedule_event_invites;

COMMIT;
