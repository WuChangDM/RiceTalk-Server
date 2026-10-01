// Package werewolf 实现简化版狼人杀权威规则引擎。
// 角色：村民、狼人、预言家。阶段：night -> day -> vote -> night。
// 系统法官：阶段由倒计时驱动自动推进（TickTimeout 惰性托管），
// 动作提前完成（狼全杀+预言家全验 / 全员投完）立即推进，无需玩家点"下一阶段"。
package werewolf

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"ridgericetalk/features/minigames/internal/util"
)

// Engine 实现简化版狼人杀权威规则。
type Engine struct{}

// New 创建一个狼人杀引擎实例。
func New() *Engine {
	return &Engine{}
}

// PhaseDurations 为各阶段系统法官推进时长（秒）。
// 可用环境变量 RRT_WEREWOLF_PHASE_SECONDS 覆盖（形如 "night=10,day=20,vote=10"），
// 便于测试环境缩短周期；默认 night=30, day=60, vote=30。
const (
	NightDuration = 30
	DayDuration   = 60
	VoteDuration  = 30
)

// phaseDurations 返回各阶段时长，支持环境变量覆盖（测试用）。
func phaseDurations() map[string]int {
	d := map[string]int{"night": NightDuration, "day": DayDuration, "vote": VoteDuration}
	if ov := os.Getenv("RRT_WEREWOLF_PHASE_SECONDS"); ov != "" {
		for _, kv := range strings.Split(ov, ",") {
			parts := strings.SplitN(strings.TrimSpace(kv), "=", 2)
			if len(parts) != 2 {
				continue
			}
			if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
				if _, ok := d[parts[0]]; ok {
					d[parts[0]] = n
				}
			}
		}
	}
	return d
}

type werewolfRole string

const (
	roleVillager werewolfRole = "villager"
	roleWerewolf werewolfRole = "werewolf"
	roleSeer     werewolfRole = "seer"
)

func (e *Engine) Init(players []map[string]interface{}) (map[string]interface{}, error) {
	ids := util.PlayerUserIDs(players)
	roles := e.assignRoles(len(ids))
	playerRoles := make(map[string]string)
	for i, id := range ids {
		playerRoles[id] = string(roles[i])
	}

	state := make(map[string]interface{})
	state["status"] = "playing"
	state["players"] = ids
	state["currentPlayer"] = ids[0]
	state["winner"] = nil
	state["phase"] = "night" // night | day | vote
	state["day"] = 1
	state["roles"] = playerRoles
	state["alive"] = ids
	state["votes"] = make(map[string]string)
	// 夜晚行动记录字段命名为 nightActions，避免与 handler 写入 state["actions"] 的
	// 动作日志（[]interface{}）冲突——handler 每次动作后用日志覆盖该字段，
	// 会导致第二个狼人的 allWerewolvesActed 找不到第一个狼人的击杀，永不进入白天。
	state["nightActions"] = make(map[string]interface{})
	// 系统法官：阶段开始时间与各阶段时长，驱动倒计时自动推进。
	state["phaseStartedAt"] = time.Now().UTC().Unix()
	state["phaseDurations"] = phaseDurations()
	return state, nil
}

func (e *Engine) ValidateMove(state map[string]interface{}, userID string, action string, payload json.RawMessage) (map[string]interface{}, error) {
	phase, _ := state["phase"].(string)
	alive := e.aliveSlice(state)

	switch action {
	case "werewolf_kill":
		if phase != "night" {
			return nil, util.ErrInvalidAction
		}
		role := e.roleOf(state, userID)
		if role != "werewolf" {
			return nil, util.ErrInvalidAction
		}
		var p struct{ Target string `json:"target"` }
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, util.ErrInvalidAction
		}
		if !util.Contains(alive, p.Target) {
			return nil, util.ErrInvalidAction
		}
		newState := util.ShallowCopy(state)
		actions, _ := newState["nightActions"].(map[string]interface{})
		if actions == nil {
			actions = make(map[string]interface{})
		}
		actions[userID] = p.Target
		actions["werewolfTarget"] = p.Target
		newState["nightActions"] = actions
		// 夜晚齐活（狼全杀 + 预言家全验）立即天亮：结算死讯并进入白天。
		if e.nightComplete(newState) {
			e.advanceToDay(newState)
		}
		return newState, nil
	case "seer_check":
		if phase != "night" {
			return nil, util.ErrInvalidAction
		}
		if e.roleOf(state, userID) != "seer" {
			return nil, util.ErrInvalidAction
		}
		var p struct{ Target string `json:"target"` }
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, util.ErrInvalidAction
		}
		newState := util.ShallowCopy(state)
		actions, _ := newState["nightActions"].(map[string]interface{})
		if actions == nil {
			actions = make(map[string]interface{})
		}
		actions["seerTarget"] = p.Target
		actions["seerResult"] = e.roleOf(state, p.Target) == "werewolf"
		newState["nightActions"] = actions
		// 夜晚齐活（狼全杀 + 预言家全验）立即天亮。
		if e.nightComplete(newState) {
			e.advanceToDay(newState)
		}
		return newState, nil
	case "vote":
		if phase != "vote" {
			return nil, util.ErrInvalidAction
		}
		if !util.Contains(alive, userID) {
			return nil, util.ErrInvalidAction
		}
		var p struct{ Target string `json:"target"` }
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, util.ErrInvalidAction
		}
		if !util.Contains(alive, p.Target) || p.Target == userID {
			return nil, util.ErrInvalidAction
		}
		newState := util.ShallowCopy(state)
		votes := e.votesMap(newState)
		votes[userID] = p.Target
		newState["votes"] = votes
		if len(votes) >= len(alive) {
			e.resolveVote(newState)
		}
		return newState, nil
	case "next_phase":
		// 系统法官已按倒计时自动推进；此动作保留兼容，但不依赖玩家点击。
		if !util.Contains(alive, userID) {
			return nil, util.ErrInvalidAction
		}
		newState := util.ShallowCopy(state)
		switch phase {
		case "night":
			e.advanceToDay(newState)
		case "day":
			newState["phase"] = "vote"
			newState["votes"] = make(map[string]string)
			newState["phaseStartedAt"] = time.Now().UTC().Unix()
		case "vote":
			e.resolveVote(newState)
		}
		return newState, nil
	default:
		return nil, util.ErrInvalidAction
	}
}

// SpectatorState 返回脱敏后的观战 state（§15.7.5.2）。
// 隐藏角色分配，仅保留存活玩家、当前阶段与投票进度等公开信息。
func (e *Engine) SpectatorState(state map[string]interface{}) map[string]interface{} {
	masked := util.ShallowCopy(state)
	// 角色分配与夜晚行动（狼刀目标 + 预言家查验结果）都是私密信息，
	// 观战者只能看到存活名单、当前阶段与投票进度。
	delete(masked, "roles")
	delete(masked, "nightActions")
	return masked
}

func (e *Engine) IsGameOver(state map[string]interface{}) (winnerID string, draw bool) {
	alive := e.aliveSlice(state)
	werewolfCount := 0
	villagerCount := 0
	for _, id := range alive {
		if e.roleOf(state, id) == "werewolf" {
			werewolfCount++
		} else {
			villagerCount++
		}
	}
	if werewolfCount == 0 {
		return "villagers", false
	}
	if werewolfCount >= villagerCount {
		return "werewolves", false
	}
	return "", false
}

func (e *Engine) assignRoles(n int) []werewolfRole {
	roles := make([]werewolfRole, n)
	for i := range roles {
		roles[i] = roleVillager
	}
	// Simplified role distribution for the MVP.
	if n >= 1 {
		roles[0] = roleWerewolf
	}
	if n >= 4 {
		roles[3] = roleSeer
	}
	if n >= 6 {
		roles[1] = roleWerewolf
	}
	if n >= 9 {
		roles[4] = roleWerewolf
	}
	return roles
}

func (e *Engine) roleOf(state map[string]interface{}, userID string) string {
	// roles may be map[string]string (fresh) or map[string]interface{} (after JSON)
	if roles, ok := state["roles"].(map[string]string); ok {
		return roles[userID]
	}
	if raw, ok := state["roles"].(map[string]interface{}); ok {
		if v, ok := raw[userID].(string); ok {
			return v
		}
	}
	return ""
}

func (e *Engine) aliveSlice(state map[string]interface{}) []string {
	// alive may be []string (fresh) or []interface{} (after JSON round-trip)
	return util.GetStringSlice(state, "alive")
}

func (e *Engine) resolveVote(state map[string]interface{}) {
	votes := e.votesMap(state)
	counts := make(map[string]int)
	for _, target := range votes {
		counts[target]++
	}
	// 计票：得票最多且唯一者出局；平票则平安日（无人放逐）。
	max := 0
	eliminated := ""
	tie := false
	for target, count := range counts {
		if count > max {
			max = count
			eliminated = target
			tie = false
		} else if count == max {
			tie = true
		}
	}
	if eliminated != "" && !tie {
		alive := e.aliveSlice(state)
		state["alive"] = util.FilterOut(alive, eliminated)
		state["lastEliminated"] = eliminated
	} else {
		state["lastEliminated"] = "" // 平安日
	}
	state["votes"] = make(map[string]string)
	state["phase"] = "night"
	day, _ := state["day"].(int)
	state["day"] = day + 1
	// 清空夜晚行动记录，进入下一夜
	state["nightActions"] = make(map[string]interface{})
	state["phaseStartedAt"] = time.Now().UTC().Unix()
}

// votesMap 解析投票（votes 可能为 map[string]string 或 JSON 后的 map[string]interface{}）。
func (e *Engine) votesMap(state map[string]interface{}) map[string]string {
	if m, ok := state["votes"].(map[string]string); ok {
		return m
	}
	out := make(map[string]string)
	if raw, ok := state["votes"].(map[string]interface{}); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

// nightComplete 判断夜晚是否齐活：所有存活狼人已杀 且 所有存活预言家已验。
func (e *Engine) nightComplete(state map[string]interface{}) bool {
	alive := e.aliveSlice(state)
	actions, _ := state["nightActions"].(map[string]interface{})
	if actions == nil {
		return false
	}
	werewolfCount := 0
	seerCount := 0
	for _, id := range alive {
		switch e.roleOf(state, id) {
		case "werewolf":
			werewolfCount++
			if _, ok := actions[id]; !ok {
				return false
			}
		case "seer":
			seerCount++
			if _, ok := actions["seerTarget"]; !ok {
				return false
			}
		}
	}
	// 无狼人或无预言家的边界：已检查完毕即视为齐活
	return werewolfCount > 0
}

// advanceToDay 夜晚结束，结算死讯并进入白天。
func (e *Engine) advanceToDay(state map[string]interface{}) {
	alive := e.aliveSlice(state)
	actions, _ := state["nightActions"].(map[string]interface{})
	target, _ := actions["werewolfTarget"].(string)
	if target != "" {
		state["alive"] = util.FilterOut(alive, target)
		state["lastKilled"] = target
	} else {
		state["lastKilled"] = "" // 平安夜
	}
	// 保留 seerResult 供预言家在白天查看；仅清理击杀目标
	delete(actions, "werewolfTarget")
	state["nightActions"] = actions
	state["phase"] = "day"
	state["phaseStartedAt"] = time.Now().UTC().Unix()
}

// phaseDuration 返回当前阶段时长（秒）。
func (e *Engine) phaseDuration(state map[string]interface{}) int {
	phase, _ := state["phase"].(string)
	if d, ok := state["phaseDurations"].(map[string]int); ok {
		if v, ok2 := d[phase]; ok2 {
			return v
		}
	}
	if raw, ok := state["phaseDurations"].(map[string]interface{}); ok {
		if v, ok2 := raw[phase].(float64); ok2 {
			return int(v)
		}
	}
	switch phase {
	case "night":
		return NightDuration
	case "day":
		return DayDuration
	case "vote":
		return VoteDuration
	}
	return 30
}

// TickTimeout 系统法官：阶段倒计时到期自动推进（惰性托管，由 handler GetState/Action 触发）。
// 离线/不操作兜底：夜晚未杀视为空刀、未验视为放弃、未投视为弃票。
func (e *Engine) TickTimeout(state map[string]interface{}) (map[string]interface{}, bool) {
	startedAt := e.phaseStartedAt(state)
	if startedAt == 0 {
		return state, false
	}
	if time.Now().UTC().Unix()-startedAt < int64(e.phaseDuration(state)) {
		return state, false
	}
	phase, _ := state["phase"].(string)
	newState := util.ShallowCopy(state)
	switch phase {
	case "night":
		// 夜晚到期：结算（可能空刀）→ 白天
		e.advanceToDay(newState)
	case "day":
		// 白天到期：→ 投票
		newState["phase"] = "vote"
		newState["votes"] = make(map[string]string)
		newState["phaseStartedAt"] = time.Now().UTC().Unix()
	case "vote":
		// 投票到期：未投视为弃票，计票 → 下一夜
		e.resolveVote(newState)
	default:
		return state, false
	}
	return newState, true
}

func (e *Engine) phaseStartedAt(state map[string]interface{}) int64 {
	if v, ok := state["phaseStartedAt"].(int64); ok {
		return v
	}
	if f, ok := state["phaseStartedAt"].(float64); ok {
		return int64(f)
	}
	return 0
}
