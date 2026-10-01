-- ============================================================
-- RidgeRiceTalk - Whiteboard 缩略图与统计字段迁移
-- 版本: 026
-- 说明: 为 whiteboards 表增加缩略图、笔触数、最近绘制时间
-- ============================================================

ALTER TABLE whiteboards
    ADD COLUMN IF NOT EXISTS thumbnail_url VARCHAR(512) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stroke_count BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_drawn_at TIMESTAMPTZ;
