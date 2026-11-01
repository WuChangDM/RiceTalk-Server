BEGIN;

-- 000047 down：删除通知中心表（结构回滚）。
--
-- 注意：这是破坏性回滚 —— 通知是用户可见的持久状态，回滚会丢掉已落库的通知与
-- 已读时间。生产回滚前应先备份（pg_dump）或接受通知历史丢失；SQLite 部署不跑
-- 版本化迁移（回落 AutoMigrate），本文件不会被使用。
DROP INDEX IF EXISTS idx_notifications_user_read;
DROP INDEX IF EXISTS idx_notifications_user_created;
DROP INDEX IF EXISTS idx_notifications_space;
DROP TABLE IF EXISTS notifications;

COMMIT;
