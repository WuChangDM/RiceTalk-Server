BEGIN;

-- 000046：把两张「手写迁移表名 ≠ GORM 默认表名」的表原地改名（1.9.4，PG 部署 P1）。
--
-- 背景：生产 RRT_ENV=production 默认关闭 AutoMigrate（见 internal/database/database.go），
-- PG 库里只有版本化迁移建的表；而代码全部走 GORM 模型查询，GORM 按默认命名策略
-- （schema.NamingStrategy）推导表名，与下面两张表的历史手写名不一致：
--   * MinigameLeaderboard → minigame_leaderboards（000024 建的是单数 minigame_leaderboard）
--   * E2EEKey            → e2_ee_keys          （000001 baseline 建的是 e2ee_keys）
-- 后果：PG 部署下排行榜读写（features/minigames/leaderboard.go）、语音 E2EE 密钥读写
-- （internal/voice/service.go）全部 "no such table" 500。SQLite 部署因 AutoMigrate 按
-- GORM 名建表，一切正常 —— 与 000044（TIMESTAMP/TIMESTAMPTZ）同一类「SQLite 绿、PG 红」。
--
-- 为什么选 rename 而不是给模型加 TableName() 迁就旧名：模型是 AutoMigrate（SQLite /
-- RRT_FORCE_AUTOMIGRATE=1）与所有 GORM 查询的唯一真源，改模型会让 SQLite 新建旧名表、
-- 已有开发库里的新名表变孤儿数据；反之把迁移表名改成 GORM 名只需原地改名，行数据不丢。
--
-- 注意：
--   1. 只对 PG 生效（SQLite 部署不跑版本化迁移，AutoMigrate 本来就是 GORM 名，无需动作）；
--      但下面统一写成 IF EXISTS / to_regclass 守卫，误在 SQLite 或无旧表的库执行也不会报错。
--   2. PG 的 ALTER TABLE ... RENAME TO 保留表内数据；索引/约束随表自动跟随（对象 OID 不变），
--      下面额外改索引/约束「名字」只是为了对齐 GORM 风格，不影响可用性。
--   3. 若旧名与新名同时存在（例如有人开过 RRT_FORCE_AUTOMIGRATE=1 让 GORM 建了新表），
--      说明数据可能已被写到新表，自动合并会丢行 —— 直接报错交给运维人工核对（同 000033 口径）。

-- ── 1) 小游戏排行榜：minigame_leaderboard → minigame_leaderboards ──
DO $$
BEGIN
    IF to_regclass('public.minigame_leaderboard') IS NULL THEN
        -- 已是规范名（全新库由 AutoMigrate/migrate 建）：无需动作
        NULL;
    ELSIF to_regclass('public.minigame_leaderboards') IS NOT NULL THEN
        RAISE EXCEPTION 'both minigame_leaderboard and minigame_leaderboards exist; refusing automatic rename (reconcile leaderboard rows manually first)';
    ELSE
        ALTER TABLE IF EXISTS minigame_leaderboard RENAME TO minigame_leaderboards;
    END IF;
END;
$$;

-- 唯一约束名（PG 对 000024 的 UNIQUE(game_type, user_id) 自动命名为
-- minigame_leaderboard_game_type_user_id_key）
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = to_regclass('public.minigame_leaderboards')
          AND conname = 'minigame_leaderboard_game_type_user_id_key'
          AND contype = 'u'
    ) AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = to_regclass('public.minigame_leaderboards')
          AND conname = 'minigame_leaderboards_game_type_user_id_key'
    ) THEN
        ALTER TABLE minigame_leaderboards
            RENAME CONSTRAINT minigame_leaderboard_game_type_user_id_key TO minigame_leaderboards_game_type_user_id_key;
    END IF;
END;
$$;

-- 索引改名（PG 已在表改名时自动跟随表 OID，这里只统一名字）
DO $$
BEGIN
    IF to_regclass('public.idx_minigame_leaderboard_game_score') IS NOT NULL
       AND to_regclass('public.idx_minigame_leaderboards_game_score') IS NULL THEN
        ALTER INDEX idx_minigame_leaderboard_game_score RENAME TO idx_minigame_leaderboards_game_score;
    END IF;
    IF to_regclass('public.idx_minigame_leaderboard_user') IS NOT NULL
       AND to_regclass('public.idx_minigame_leaderboards_user') IS NULL THEN
        ALTER INDEX idx_minigame_leaderboard_user RENAME TO idx_minigame_leaderboards_user;
    END IF;
END;
$$;

-- ── 2) E2EE 密钥：e2ee_keys → e2_ee_keys ──
DO $$
BEGIN
    IF to_regclass('public.e2ee_keys') IS NULL THEN
        NULL;
    ELSIF to_regclass('public.e2_ee_keys') IS NOT NULL THEN
        RAISE EXCEPTION 'both e2ee_keys and e2_ee_keys exist; refusing automatic rename (reconcile E2EE key rows manually first)';
    ELSE
        ALTER TABLE IF EXISTS e2ee_keys RENAME TO e2_ee_keys;
    END IF;
END;
$$;

-- 000005 已把 e2ee_keys 的唯一索引改为 idx_e2ee_keys_room_id（room_id 唯一）
DO $$
BEGIN
    IF to_regclass('public.idx_e2ee_keys_room_id') IS NOT NULL
       AND to_regclass('public.idx_e2_ee_keys_room_id') IS NULL THEN
        ALTER INDEX idx_e2ee_keys_room_id RENAME TO idx_e2_ee_keys_room_id;
    END IF;
END;
$$;

COMMIT;
