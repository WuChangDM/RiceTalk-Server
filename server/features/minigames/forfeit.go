package minigames

import (
	"log"
	"time"

	"ridgericetalk/internal/model"
)

// D3（task-65）：1v1 判负（断线 / 离开）。
//
// 语义（Lead 拍板）：
//   - 仅 1v1 联机棋类（GameInfo.Players == 2 且 Online）；
//   - 「彻底离开」（显式 leave）→ 对手立即判胜；
//   - 「断线超时」：超过 60s 无心跳/动作 → 判负；窗口内重连（拉取状态或提交动作即心跳）不判负；
//   - 判负后对手获胜、走统一终局出口（写历史 + 权威计分），并广播 game_ended；
//   - 1v1 之外的多人房逻辑完全不变（仍走 Leave / cleanupStaleRooms 的既有分支）。
//
// 心跳载体：state["presence"] = {userId: unix 毫秒}，由 Action / GetState 顺带刷新（节流 15s），
// 不新增数据库列、不加迁移（task-65 明确禁止迁移）。
const (
	// disconnectGrace 断线重连宽限期：窗口内重连不判负。
	disconnectGrace = 60 * time.Second
	// presenceHeartbeat 心跳写回节流：避免每次轮询都写库。
	presenceHeartbeat = 15 * time.Second
)

const presenceKey = "presence"

// is1v1Game 判断是否为 1v1 联机对局（仅棋类：井字棋/国际象棋/中国象棋/五子棋）。
func is1v1Game(gameType string) bool {
	info, ok := getGameInfo(gameType)
	return ok && info.Online && info.Players == 2
}

// presenceOf 读取 state 内的心跳表（userId → unix 毫秒）。
func presenceOf(state map[string]interface{}) map[string]int64 {
	out := make(map[string]int64)
	raw, ok := state[presenceKey].(map[string]interface{})
	if !ok {
		return out
	}
	for uid, v := range raw {
		switch n := v.(type) {
		case float64:
			out[uid] = int64(n)
		case int64:
			out[uid] = n
		case int:
			out[uid] = int64(n)
		}
	}
	return out
}

// touchPresence 刷新某玩家的心跳；返回是否需要把 state 写回（节流窗口内返回 false）。
func touchPresence(state map[string]interface{}, userID string, now time.Time) bool {
	if userID == "" {
		return false
	}
	nowMs := now.UnixMilli()
	current := presenceOf(state)
	if last, ok := current[userID]; ok && nowMs-last < presenceHeartbeat.Milliseconds() {
		return false
	}
	current[userID] = nowMs
	raw := make(map[string]interface{}, len(current))
	for uid, ms := range current {
		raw[uid] = ms
	}
	state[presenceKey] = raw
	return true
}

// forfeit1v1 把 1v1 对局判给 winnerID。
// reason："opponent_left"（显式离开）/ "disconnect_timeout"（断线超时）。
// 返回 true 表示已成功落库并完成结算（调用方不应再走普通 Leave/超时逻辑）。
func (h *Handler) forfeit1v1(session *model.MinigameSession, winnerID, loserID, reason string) bool {
	now := time.Now().UTC()
	state := parseState(session.State)
	state["winner"] = winnerID
	state["endReason"] = reason
	state["forfeitBy"] = loserID
	prevState := session.State
	session.State = marshalState(state)
	session.Status = "ended"
	session.UpdatedAt = now
	if err := h.persistSessionState(session, prevState); err != nil {
		return false
	}
	// 统一终局出口：写历史 + 权威计分（棋类胜场 +1）。
	if err := h.finishGame(session, winnerID, false); err != nil {
		log.Printf("[minigames] 结算权威计分失败 session=%s game=%s winner=%s: %v",
			session.ID, session.GameType, winnerID, err)
	}
	h.broadcastToRoom(session.ID, map[string]interface{}{
		"gameId":    session.ID,
		"eventType": "game_ended",
		"winnerId":  winnerID,
		"reason":    reason,
		"status":    "ended",
	})
	return true
}

// applyDisconnectForfeit 检查 1v1 对局是否有人超过宽限期未心跳；有则判负并返回 true。
// 由 tickTimedGames 每 2s 调用一次。
func (h *Handler) applyDisconnectForfeit(session *model.MinigameSession, now time.Time) bool {
	if session.Status != "playing" || !is1v1Game(session.GameType) {
		return false
	}
	players := parsePlayers(session.PlayersJSON)
	if len(players) != 2 {
		return false
	}
	state := parseState(session.State)
	presence := presenceOf(state)
	if len(presence) == 0 {
		// 没有心跳数据（升级前创建的对局）：以「现在」为基线，避免立刻误判，
		// 下一轮起才开始计时。
		updated := false
		for _, p := range players {
			if uid, _ := p["userId"].(string); uid != "" {
				updated = touchPresence(state, uid, now) || updated
			}
		}
		if updated {
			prev := session.State
			session.State = marshalState(state)
			_ = h.persistSessionState(session, prev)
		}
		return false
	}
	graceMs := disconnectGrace.Milliseconds()
	for i, p := range players {
		uid, _ := p["userId"].(string)
		if uid == "" {
			continue
		}
		last, ok := presence[uid]
		if !ok {
			continue // 缺心跳的玩家视为下一轮才开始计时
		}
		if now.UnixMilli()-last <= graceMs {
			continue
		}
		other, _ := players[1-i]["userId"].(string)
		if other == "" {
			return false
		}
		return h.forfeit1v1(session, other, uid, "disconnect_timeout")
	}
	return false
}
