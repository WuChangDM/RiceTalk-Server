-- ============================================================
-- RidgeRiceTalk - Whiteboard 独立化迁移
-- 版本: 002
-- 说明: 将白板从频道依附改为空间级独立实体
-- ============================================================

-- ── 独立白板表 (Whiteboard → whiteboards) ──
CREATE TABLE IF NOT EXISTS whiteboards (
    id          VARCHAR(64) PRIMARY KEY,
    space_id    VARCHAR(64) NOT NULL,
    name        VARCHAR(128) NOT NULL,
    archived    BOOLEAN DEFAULT FALSE,
    created_by  VARCHAR(64) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_whiteboards_space_id ON whiteboards (space_id);
CREATE INDEX IF NOT EXISTS idx_whiteboards_archived ON whiteboards (archived);
CREATE INDEX IF NOT EXISTS idx_whiteboards_created_by ON whiteboards (created_by);

-- ── 白板笔触表增加 whiteboard_id ──
ALTER TABLE whiteboard_strokes
    ADD COLUMN IF NOT EXISTS whiteboard_id VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_whiteboard_strokes_whiteboard_id ON whiteboard_strokes (whiteboard_id);

-- ── 为每个有笔触的频道迁移成独立白板 ──
DO $$
DECLARE
    r RECORD;
    new_wb_id VARCHAR(64);
BEGIN
    FOR r IN
        SELECT ws.channel_id, c.space_id
        FROM whiteboard_strokes ws
        JOIN channels c ON c.id = ws.channel_id
        WHERE ws.channel_id IS NOT NULL AND ws.channel_id != ''
        GROUP BY ws.channel_id, c.space_id
    LOOP
        -- 幂等：若该频道已迁移过则复用已有白板
        SELECT id INTO new_wb_id
        FROM whiteboards
        WHERE space_id = r.space_id AND name = r.channel_id
        LIMIT 1;

        IF new_wb_id IS NULL THEN
            new_wb_id := 'wb_' || REPLACE(gen_random_uuid()::text, '-', '');
            INSERT INTO whiteboards (id, space_id, name, archived, created_by)
            VALUES (new_wb_id, r.space_id, r.channel_id, FALSE, 'system_migration');
        END IF;

        UPDATE whiteboard_strokes
        SET whiteboard_id = new_wb_id
        WHERE channel_id = r.channel_id
          AND (whiteboard_id IS NULL OR whiteboard_id = '');
    END LOOP;
END $$;

-- ── 为每个空间确保至少有一张默认白板 ──
INSERT INTO whiteboards (id, space_id, name, archived, created_by)
SELECT
    'wb_' || REPLACE(gen_random_uuid()::text, '-', '') AS id,
    s.id AS space_id,
    '默认白板' AS name,
    FALSE AS archived,
    'system' AS created_by
FROM spaces s
WHERE NOT EXISTS (
    SELECT 1 FROM whiteboards w WHERE w.space_id = s.id
);

-- ── 将未迁移的笔触归到空间默认白板（兜底）──
UPDATE whiteboard_strokes ws
SET whiteboard_id = (
    SELECT w.id FROM whiteboards w
    JOIN channels c ON c.space_id = w.space_id
    WHERE c.id = ws.channel_id
    LIMIT 1
)
WHERE (whiteboard_id IS NULL OR whiteboard_id = '')
  AND channel_id IS NOT NULL AND channel_id != '';

-- ── 删除旧 channel_id 列（数据已迁移）──
ALTER TABLE whiteboard_strokes DROP COLUMN IF EXISTS channel_id;
