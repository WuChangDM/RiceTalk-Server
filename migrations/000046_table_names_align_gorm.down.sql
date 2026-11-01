BEGIN;

-- 000046 down：把 000046 up 改过的两张表名/索引名/约束名改回历史名字。
--
-- 说明：
--   1. 这是「结构回滚」，不删任何行：PG 的 RENAME 往返不迁移数据，回滚后数据仍在表里。
--   2. 只有当前是规范名（GORM 名）、且历史名不存在时才反向改名；两者同时存在时报错交给运维，
--      避免把数据写到错误的一张表（同 000033 的保守口径）。
--   3. 与 up 对称地写成 IF EXISTS / to_regclass 守卫，误在 SQLite 或无对应表的库执行不会报错。

-- ── 1) 小游戏排行榜：minigame_leaderboards → minigame_leaderboard ──
DO $$
BEGIN
    IF to_regclass('public.minigame_leaderboards') IS NULL THEN
        NULL;
    ELSIF to_regclass('public.minigame_leaderboard') IS NOT NULL THEN
        RAISE EXCEPTION 'both minigame_leaderboards and minigame_leaderboard exist; refusing automatic rollback rename';
    ELSE
        ALTER TABLE IF EXISTS minigame_leaderboards RENAME TO minigame_leaderboard;
    END IF;
END;
$$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = to_regclass('public.minigame_leaderboard')
          AND conname = 'minigame_leaderboards_game_type_user_id_key'
          AND contype = 'u'
    ) AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = to_regclass('public.minigame_leaderboard')
          AND conname = 'minigame_leaderboard_game_type_user_id_key'
    ) THEN
        ALTER TABLE minigame_leaderboard
            RENAME CONSTRAINT minigame_leaderboards_game_type_user_id_key TO minigame_leaderboard_game_type_user_id_key;
    END IF;
END;
$$;

DO $$
BEGIN
    IF to_regclass('public.idx_minigame_leaderboards_game_score') IS NOT NULL
       AND to_regclass('public.idx_minigame_leaderboard_game_score') IS NULL THEN
        ALTER INDEX idx_minigame_leaderboards_game_score RENAME TO idx_minigame_leaderboard_game_score;
    END IF;
    IF to_regclass('public.idx_minigame_leaderboards_user') IS NOT NULL
       AND to_regclass('public.idx_minigame_leaderboard_user') IS NULL THEN
        ALTER INDEX idx_minigame_leaderboards_user RENAME TO idx_minigame_leaderboard_user;
    END IF;
END;
$$;

-- ── 2) E2EE 密钥：e2_ee_keys → e2ee_keys ──
DO $$
BEGIN
    IF to_regclass('public.e2_ee_keys') IS NULL THEN
        NULL;
    ELSIF to_regclass('public.e2ee_keys') IS NOT NULL THEN
        RAISE EXCEPTION 'both e2_ee_keys and e2ee_keys exist; refusing automatic rollback rename';
    ELSE
        ALTER TABLE IF EXISTS e2_ee_keys RENAME TO e2ee_keys;
    END IF;
END;
$$;

DO $$
BEGIN
    IF to_regclass('public.idx_e2_ee_keys_room_id') IS NOT NULL
       AND to_regclass('public.idx_e2ee_keys_room_id') IS NULL THEN
        ALTER INDEX idx_e2_ee_keys_room_id RENAME TO idx_e2ee_keys_room_id;
    END IF;
END;
$$;

COMMIT;
