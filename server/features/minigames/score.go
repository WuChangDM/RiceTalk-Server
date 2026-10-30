package minigames

import (
	"fmt"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
)

// D1（task-65）：棋类服务端权威计分。
//
// 设计要点：
//   - 可判胜负的多人棋类（井字棋/国际象棋/中国象棋/五子棋/飞行棋）在 GameInfo 上
//     声明 ScoreDimension="胜场数"；服务端在每局结算时给胜者累加 1 场，
//     客户端 POST /minigames/leaderboard 上传的 score 一律忽略（字段保留以兼容旧客户端）；
//   - 斗地主/日麻/狼人杀的分数由各自引擎结算，本文件不介入（它们无「胜场数」维度）；
//   - 单机游戏仍保留客户端上报，但服务端加「分数上限 + 短时暴涨」两道闸。
//
// 阈值与口径集中在本文件，改动阈值需同步 rule/score 单测。

// authoritativeScoreDimension 是服务端权威计分的排行榜维度名。
const authoritativeScoreDimension = "胜场数"

// singlePlayerScoreCaps 单机排行榜分数上限（D1「其余品类限上限」）。
// 取值口径：覆盖人类可达范围并留足冗余，超出即视为异常上报并拒收。
var singlePlayerScoreCaps = map[string]int64{
	"2048":            10_000_000, // 最高分：理论上限约 3.9M，10M 留余量
	"tictactoe-local": 1_000_000,  // 最高连胜：人类极限远小于此
	"chess-local":     86_400,     // 最短用时(秒)：24 小时，超过视为异常
	"xiangqi-local":   86_400,     // 同上
}

// scoreAnomalyFactor 单机 desc 类排行榜的暴涨倍数阈值：
// 不超过已有成绩的 20 倍仍接受，超过视为异常（防一次性刷到顶）。
const scoreAnomalyFactor = 20

// scoreAnomalyFloor 暴涨检测的最低基数：已有成绩低于该值时不判暴涨（避免早期误伤）。
const scoreAnomalyFloor = 1000

// isAuthoritativeScored 判断某游戏是否走服务端权威计分（多人 + 胜场数维度）。
func isAuthoritativeScored(gameType string) bool {
	info, ok := getGameInfo(gameType)
	if !ok {
		return false
	}
	return info.Online && info.ScoreDimension == authoritativeScoreDimension
}

// finishGame 是「一局结束」的统一出口：写历史 +（权威计分游戏）给胜者累加胜场。
// 所有终局路径（引擎判定、超时托管、1v1 判负）都必须走这里，避免漏记或口径不一。
func (h *Handler) finishGame(session *model.MinigameSession, winnerID string, isDraw bool) {
	h.RecordGameEnd(session, winnerID, isDraw)
	if isDraw || winnerID == "" {
		return
	}
	if !isAuthoritativeScored(session.GameType) {
		return
	}
	h.addAuthoritativeWin(session.GameType, winnerID)
}

// addAuthoritativeWin 给胜者累加 1 场胜利。
// 幂等性由「每局只经过一次 finishGame」保证（会话结束即不再进入 action/超时路径）。
func (h *Handler) addAuthoritativeWin(gameType, userID string) {
	if userID == "" {
		return
	}
	var row model.MinigameLeaderboard
	err := h.db.Where("game_type = ? AND user_id = ?", gameType, userID).First(&row).Error
	if err == nil {
		row.BestScore++
		_ = h.db.Save(&row).Error
		return
	}
	_ = h.db.Create(&model.MinigameLeaderboard{
		ID:        idgen.NextString(),
		GameType:  gameType,
		UserID:    userID,
		BestScore: 1,
	}).Error
}

// validateSinglePlayerScore 校验单机排行榜上报的分数（D1「限上限 + 异常分检测」）。
// currentBest 为该用户在该游戏的已有最好成绩（无记录为 0）。
func validateSinglePlayerScore(info GameInfo, score, currentBest int64) error {
	cap, capped := singlePlayerScoreCaps[info.ID]
	if capped && score > cap {
		return errors.New(errors.MINIGAME_INVALID_ACTION,
			fmt.Sprintf("score exceeds cap for this game (cap=%d)", cap))
	}
	if !capped || info.ScoreOrder == "asc" {
		return nil
	}
	if currentBest > 0 && score > currentBest*scoreAnomalyFactor && currentBest >= scoreAnomalyFloor {
		return errors.New(errors.MINIGAME_INVALID_ACTION,
			fmt.Sprintf("score jump looks anomalous (previous=%d, submitted=%d)", currentBest, score))
	}
	return nil
}
