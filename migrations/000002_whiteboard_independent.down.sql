-- ============================================================
-- RidgeRiceTalk - Whiteboard 独立化迁移回滚
-- 版本: 002
-- 说明: 恢复 channel_id 列，并将 whiteboard_id 映射回去
-- ============================================================

-- ── 恢复 channel_id 列 ──
ALTER TABLE whiteboard_strokes
    ADD COLUMN IF NOT EXISTS channel_id VARCHAR(64);

-- ── 根据 whiteboard 名称（原频道 ID）回写 channel_id ──
UPDATE whiteboard_strokes ws
SET channel_id = w.name
FROM whiteboards w
WHERE ws.whiteboard_id = w.id
  AND w.created_by = 'system_migration';

CREATE INDEX IF NOT EXISTS idx_whiteboard_strokes_channel_id ON whiteboard_strokes (channel_id);

-- ── 删除 whiteboard_id 列 ──
ALTER TABLE whiteboard_strokes DROP COLUMN IF EXISTS whiteboard_id;

-- ── 删除独立白板表 ──
DROP TABLE IF EXISTS whiteboards;
