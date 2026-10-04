BEGIN;

-- DES-20261001-01 §12.1「A11 管理后台监控告警」— 告警表。
--
-- 设计要点：
--   * admin_alerts 存评估器（internal/admin/alerts.go）产生的运维告警，
--     type 取值域：disk|health|livekit|database|tts_worker；
--   * severity 取值域：warn|alert（alert 比 warn 更严重，UI 红色横幅）；
--   * 同一 type 同时至多一行未恢复告警（resolved_at IS NULL）：防抖 open 后
--     复用该行更新 last_seen_at/message，不重复插入（评估器业务保证）；
--   * muted_until 为该告警行的静默截止时间：静默期间评估器照常记账
--     （insert/update/resolved 均写表），但同 type 不再向 admin WS 推送；
--   * idx_admin_alerts_open 支撑「活跃告警」（resolved_at IS NULL）高频查询
--     ——WS 连接快照与 REST GET /api/admin/alerts 都以活跃为主。

CREATE TABLE admin_alerts (
  id            VARCHAR(64) PRIMARY KEY,
  type          VARCHAR(32) NOT NULL,
  severity      VARCHAR(8)  NOT NULL,
  message       TEXT        NOT NULL,
  first_seen_at TIMESTAMPTZ NOT NULL,
  last_seen_at  TIMESTAMPTZ NOT NULL,
  resolved_at   TIMESTAMPTZ NULL,
  muted_until   TIMESTAMPTZ NULL
);

CREATE INDEX idx_admin_alerts_open ON admin_alerts(resolved_at);

COMMIT;
