BEGIN;

-- 000047：通知中心落库（1.9.4 task-118 服务端半边）。
--
-- 背景：/api/v1/notifications 三条路由此前都是桩实现（GET 恒返回空列表，
-- read / read-all 只回 {"message":"ok"}），通知完全没有存储、拉取与已读语义；
-- @提及只推 WebSocket 事件 + 累加 UserChannelRead.MentionCount，服务端重启即丢。
--
-- 口径（务必遵守，否则 PG 生产出错而 SQLite 单测全绿）：
--   1. 表名必须是 GORM 默认名 notifications（模型 model.Notification）。生产
--      RRT_ENV=production 默认关闭 AutoMigrate，PG 库里只有版本化迁移建的表，
--      代码按 GORM 名查询 → 表名不一致就 no such table（000045 教训；守卫见
--      server/internal/database/migration_table_name_test.go 的通用用例）。
--   2. 时间列一律 TIMESTAMPTZ（000044 教训：裸 TIMESTAMP 在 PG 下与 Go time.Time
--      差 8 小时，SQLite 测不出来）。
--   3. 索引名与模型 gorm index 标签一致，让 AutoMigrate（SQLite / 开发）与版本化
--      迁移（PG / 生产）建出同构结构：未读计数走 (user_id, read_at)，
--      列表分页走 (user_id, created_at)，space 维度排障走 space_id。
CREATE TABLE IF NOT EXISTS notifications (
    id         VARCHAR(64) PRIMARY KEY,
    user_id    VARCHAR(64) NOT NULL,
    type       VARCHAR(16) NOT NULL,
    title      VARCHAR(255) NOT NULL,
    body       TEXT,
    ref_type   VARCHAR(32),
    ref_id     VARCHAR(64),
    space_id   VARCHAR(64),
    read_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notifications_user_read ON notifications (user_id, read_at);
CREATE INDEX IF NOT EXISTS idx_notifications_user_created ON notifications (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_notifications_space ON notifications (space_id);

COMMIT;
