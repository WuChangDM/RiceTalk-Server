-- 说明: 删除已解耦模块残留的 channel_id 列
-- 适用: PostgreSQL 生产环境
-- 对应设计: docs/FUNCTIONAL_MODULE_INDEPENDENCE_DESIGN.md

ALTER TABLE file_metadata DROP COLUMN IF EXISTS channel_id;
ALTER TABLE schedule_events DROP COLUMN IF EXISTS channel_id;
ALTER TABLE shared_documents DROP COLUMN IF EXISTS channel_id;
