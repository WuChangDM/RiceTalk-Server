BEGIN;

-- DES-20261001-01 §9.2「A8-S1：日程事件邀请 RSVP」— 邀请表 + 事件受邀人快照列。
--
-- 设计要点：
--   * schedule_event_invites 存每个受邀人的 RSVP 状态（pending/accepted/declined），
--     (event_id, user_id) 唯一：同一事件对同一用户至多一条邀请；
--   * idx_schedule_invite_user 支撑「我收到的邀请」按用户检索与提醒收件人合并；
--   * schedule_events.invitee_ids 为受邀人快照（JSONB 数组），仅供列表/详情快照展示，
--     权威状态以 schedule_event_invites 为准；普通事件存 '[]'，行为与无邀请完全一致；
--   * 不加外键（与 000038 风格一致）：事件删除时由业务层（DeleteEvent）级联清理邀请行；
--   * SQLite（开发/测试走 AutoMigrate，RRT_FORCE_AUTOMIGRATE=1）与 PostgreSQL 语义一致，
--     SQLite 侧 serializer:json 读写 JSON 文本。

CREATE TABLE schedule_event_invites (
  id VARCHAR(64) PRIMARY KEY,
  event_id VARCHAR(64) NOT NULL,
  user_id VARCHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  responded_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_schedule_invite_event_user ON schedule_event_invites(event_id, user_id);
CREATE INDEX idx_schedule_invite_user ON schedule_event_invites(user_id);

ALTER TABLE schedule_events ADD COLUMN invitee_ids JSONB NOT NULL DEFAULT '[]';

COMMIT;
