// Package util 提供 minigames 各游戏子包共用的辅助函数与错误变量。
//
// 本包位于 minigames/internal/util，遵循 Go internal 机制：
// 仅可被 ridgericetalk/features/minigames/ 及其子包 import，
// 避免外部依赖，保证 helper 不会被误用为通用工具库。
//
// 这里集中存放所有引擎都需要的状态处理工具：
//   - JSON 反序列化后的类型归一化（getStringSlice）
//   - 玩家列表处理（playerUserIDs, nextPlayer, contains, filterOut）
//   - 状态浅拷贝（shallowCopy）
//   - 引擎通用错误（ErrInvalidAction, ErrNotYourTurn）
package util

import "errors"

// 引擎通用错误。所有游戏引擎在非法行动或非当前回合时应返回这两个错误，
// 便于 handler.go 统一识别并转换为对应的 HTTP 响应。
var (
	ErrInvalidAction = errors.New("invalid game action")
	ErrNotYourTurn   = errors.New("not your turn")
)

// getStringSlice 归一化应为 []string 的 state 字段。
//
// 背景：session.State 以 TEXT 存储在数据库中，每次 move_made action 都会
// JSON 序列化/反序列化 state。JSON 反序列化后：
//   - []string 变为 []interface{}
//   - map[string]string 变为 map[string]interface{}
//   - int 变为 float64
//
// 导致类型断言 state["players"].([]string) 静默失败（返回 nil），
// 引发 symbol 错误、胜者丢失、回合错乱等问题。
// 本函数同时处理 []string（Init 后新鲜数据）和 []interface{}（JSON 反序列化后数据）。
func GetStringSlice(state map[string]interface{}, key string) []string {
	raw, ok := state[key]
	if !ok || raw == nil {
		return nil
	}
	// Fast path: already []string (fresh from Init, before DB round-trip)
	if s, ok := raw.([]string); ok {
		return s
	}
	// Slow path: []interface{} after JSON unmarshal
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// PlayerUserIDs 从玩家信息 map 列表中提取 userId 列表。
// 各引擎在 Init 时调用此函数将 players 参数转换为 userId 切片存入 state。
func PlayerUserIDs(players []map[string]interface{}) []string {
	ids := make([]string, 0, len(players))
	for _, p := range players {
		if id, ok := p["userId"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// NextPlayer 返回玩家列表中 currentUser 之后的下一个玩家（轮询）。
// 用于回合制游戏中推进 currentPlayer。
func NextPlayer(players []string, currentUser string) string {
	for i, id := range players {
		if id == currentUser {
			return players[(i+1)%len(players)]
		}
	}
	return ""
}

// ShallowCopy 对 state map 进行浅拷贝。
// 引擎在 ValidateMove 中返回新 state 时使用，避免修改入参 state。
func ShallowCopy(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Contains 判断字符串切片是否包含指定值。
// 目前主要用于 werewolf 引擎判断目标是否在存活玩家列表中。
func Contains(arr []string, v string) bool {
	for _, x := range arr {
		if x == v {
			return true
		}
	}
	return false
}

// FilterOut 从字符串切片中移除指定值，返回新切片。
// 目前主要用于 werewolf 引擎在击杀/投票后从存活列表移除玩家。
func FilterOut(arr []string, v string) []string {
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
