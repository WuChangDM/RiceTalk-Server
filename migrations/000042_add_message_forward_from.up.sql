BEGIN;

-- 消息转发（一期）：messages 表新增「转发来源作者显示名快照」列。
--
-- 设计要点：
--   * forward_from 只存转发动作发生时源消息原作者的显示名快照（≤64 字符，服务端
--     截断校验），空串 = 非转发消息；
--   * 不存被转发消息的引用 ID，不做反向链接 / 转发计数（一期不做合并转发），
--     再转发时覆盖为最近一次转发动作时源消息的作者名，不保留转发链；
--   * SQLite（开发/测试）走 GORM AutoMigrate 自动加列，无需本文件；
--     PostgreSQL（生产）由版本化迁移补列，旧行回填空串（非转发）。

ALTER TABLE messages ADD COLUMN forward_from VARCHAR(64) NOT NULL DEFAULT '';

COMMIT;
