CREATE TABLE IF NOT EXISTS minigame_leaderboard (
    id          VARCHAR(64) PRIMARY KEY,
    game_type   VARCHAR(32) NOT NULL,
    user_id     VARCHAR(64) NOT NULL,
    username    VARCHAR(64) NOT NULL,
    best_score  BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(game_type, user_id)
);

CREATE INDEX IF NOT EXISTS idx_minigame_leaderboard_game_score
    ON minigame_leaderboard(game_type, best_score DESC);

CREATE INDEX IF NOT EXISTS idx_minigame_leaderboard_user
    ON minigame_leaderboard(user_id, game_type);
