// Package minigames 提供联机小游戏的 HTTP 路由、会话管理、历史记录与排行榜服务。
//
// 本包是 minigames 模块的根包，负责：
//   - 定义 GameEngine 接口（各游戏子包隐式实现）
//   - 维护 EngineRegistry（在初始化时显式注册各子包引擎）
//   - 提供 HTTP Handler（路由、会话、action 分发）
//   - 历史记录与排行榜服务
//
// 各游戏引擎实现位于独立子包（tictactoe/、gobang/、chess/、werewolf/、ludo/、doudizhu/），
// 通过实现 GameEngine 接口注册到 EngineRegistry。子包不 import 根包，避免循环依赖。
// 共用的 helper 函数与错误变量位于 internal/util 子包。
package minigames

import (
	"encoding/json"

	"ridgericetalk/features/minigames/chess"
	"ridgericetalk/features/minigames/doudizhu"
	"ridgericetalk/features/minigames/gobang"
	"ridgericetalk/features/minigames/ludo"
	"ridgericetalk/features/minigames/tictactoe"
	"ridgericetalk/features/minigames/werewolf"
)

// GameEngine is the authoritative rule engine for a minigame.
// Each supported game provides an implementation that validates moves,
// advances state, and reports when the game is over.
//
// 各游戏子包通过实现这三个方法隐式满足本接口（Go 鸭子类型），
// 无需 import 根包，从而避免循环依赖。
type GameEngine interface {
	// Init returns the initial authoritative state for the given players.
	// The returned map must include: currentPlayer (first player's userId),
	// players, status="playing", winner=nil, and game-specific fields.
	Init(players []map[string]interface{}) (map[string]interface{}, error)

	// ValidateMove validates an action payload against the current state.
	// It returns the updated state (or an error). The engine is responsible for
	// turn order, move legality, win/draw detection, and advancing currentPlayer.
	ValidateMove(state map[string]interface{}, userID string, action string, payload json.RawMessage) (map[string]interface{}, error)

	// IsGameOver reports whether the game has ended. winnerID is empty for draws.
	IsGameOver(state map[string]interface{}) (winnerID string, draw bool)
}

// SpectatorStateProvider 是可选接口（§15.7.5.2）。
// 仅含私密信息的游戏（斗地主手牌、狼人杀角色）需要实现，
// 返回脱敏后的观战 state；棋类等公开信息游戏无需实现（直接返回完整 state）。
type SpectatorStateProvider interface {
	SpectatorState(state map[string]interface{}) map[string]interface{}
}

// EngineRegistry maps gameType IDs to their engines.
type EngineRegistry struct {
	engines map[string]GameEngine
}

// NewEngineRegistry creates a registry populated with built-in engines.
//
// 显式注册各游戏子包引擎。子包通过 New() 返回实现了 GameEngine 接口的 *Engine。
// 新增游戏时，在此处追加一行 r.Register(<gameType>, <subpackage>.New()) 即可。
func NewEngineRegistry() *EngineRegistry {
	r := &EngineRegistry{engines: make(map[string]GameEngine)}
	r.Register("tictactoe", tictactoe.New())
	r.Register("gobang", gobang.New())
	r.Register("chess", chess.New())
	r.Register("werewolf", werewolf.New())
	r.Register("ludo", ludo.New())
	r.Register("doudizhu", doudizhu.New())
	return r
}

// Register adds or overrides an engine for a game type.
func (r *EngineRegistry) Register(gameType string, engine GameEngine) {
	r.engines[gameType] = engine
}

// Get returns the engine for the given game type, or nil if not registered.
func (r *EngineRegistry) Get(gameType string) (GameEngine, bool) {
	engine, ok := r.engines[gameType]
	return engine, ok
}
