-- ============================================================
-- RidgeRiceTalk - Whiteboard 缩略图与统计字段回滚
-- 版本: 026
-- ============================================================

ALTER TABLE whiteboards
    DROP COLUMN IF EXISTS thumbnail_url,
    DROP COLUMN IF EXISTS stroke_count,
    DROP COLUMN IF EXISTS last_drawn_at;
