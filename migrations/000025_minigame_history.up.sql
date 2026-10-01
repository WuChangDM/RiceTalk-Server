-- ============================================================
-- 000025: 小游戏历史记录表 (MinigameHistory → minigame_histories)
-- 每局游戏结束时写入一条记录，永久保留（参见设计文档 §15.4）
-- ============================================================

CREATE TABLE IF NOT EXISTS minigame_histories (
    id            VARCHAR(64) PRIMARY KEY,
    game_type     VARCHAR(32) NOT NULL,
    space_id      VARCHAR(64) NOT NULL,
    session_id    VARCHAR(64),
    host_id       VARCHAR(64) NOT NULL,
    host_name     VARCHAR(64),
    winner_id     VARCHAR(64),
    winner_name   VARCHAR(64),
    is_draw       BOOLEAN NOT NULL DEFAULT FALSE,
    players_json  TEXT,
    final_scores  TEXT,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    duration      BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_minigame_history_game
    ON minigame_histories(game_type, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_minigame_history_space
    ON minigame_histories(space_id, created_at DESC);
