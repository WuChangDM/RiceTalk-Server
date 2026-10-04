package minigames

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/features/minigames/internal/util"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// GameInfo describes a registered minigame (L21: 可扩展注册机制)
type GameInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Players    int    `json:"players"`    // 人数上限
	Online     bool   `json:"online"`     // 是否支持多人联机
	LocalOnly  bool   `json:"localOnly"`  // 是否仅本地单机
	MinPlayers int    `json:"minPlayers"` // 开局人数下限（0 表示等于 Players）
	// Category 供客户端分流（设计 §15.3.3）："single" 单机 / "multi" 多人联机。
	Category string `json:"category"`
	// ScoreDimension/ScoreOrder 仅单机游戏有排行榜（设计 §15.7.4）：
	// 维度名（如"最高分"/"最高连胜"/"最短用时(秒)"），方向 "desc"（越大越好）或 "asc"（越小越好）。
	// ScoreDimension 为空表示该游戏无排行榜（多人联机游戏）。
	ScoreDimension string `json:"scoreDimension,omitempty"`
	ScoreOrder     string `json:"scoreOrder,omitempty"`
}

// MinPlayersOrDefault returns the effective lower bound for starting a game.
func (g GameInfo) MinPlayersOrDefault() int {
	if g.MinPlayers > 0 {
		return g.MinPlayers
	}
	return g.Players
}

// defaultGames is the built-in game registry (L21: 可扩展注册机制)
//
// 单机条目（Category=single）只作为「列表项 + 排行榜宿主」存在，不注册引擎：
// 客户端本地实现玩法，服务端只负责排行榜。Join/game_start 的 Online 校验会拒绝其建房。
var defaultGames = []GameInfo{
	// —— 多人联机（需要房间，无排行榜）——
	{ID: "tictactoe", Name: "井字棋", Players: 2, Online: true, Category: "multi"},
	{ID: "chess", Name: "国际象棋", Players: 2, Online: true, Category: "multi"},
	{ID: "gobang", Name: "五子棋", Players: 2, Online: true, Category: "multi"},
	{ID: "werewolf", Name: "狼人杀", Players: 6, Online: true, Category: "multi"},
	// 飞行棋支持 2-4 人（设计 §15.7.5.1）
	{ID: "ludo", Name: "飞行棋", Players: 4, MinPlayers: 2, Online: true, Category: "multi"},
	{ID: "doudizhu", Name: "斗地主", Players: 3, Online: true, Category: "multi"},
	// —— 单机（无需房间，每个游戏各有排行榜）——
	{ID: "2048", Name: "2048", Players: 1, LocalOnly: true, Category: "single", ScoreDimension: "最高分", ScoreOrder: "desc"},
	{ID: "tictactoe-local", Name: "井字棋（单机）", Players: 1, LocalOnly: true, Category: "single", ScoreDimension: "最高连胜", ScoreOrder: "desc"},
	{ID: "chess-local", Name: "国际象棋（单机）", Players: 1, LocalOnly: true, Category: "single", ScoreDimension: "最短用时(秒)", ScoreOrder: "asc"},
}

// L21: 游戏注册表（线程安全，支持运行时动态注册）
var (
	gamesMu         sync.RWMutex
	registeredGames = make(map[string]GameInfo)
)

func init() {
	for _, g := range defaultGames {
		registeredGames[g.ID] = g
	}
}

// RegisterGame registers a new game type (L21: 可扩展注册机制入口)
// 线程安全，可在 init 时注册内置游戏，也可在运行时扩展
func RegisterGame(g GameInfo) {
	gamesMu.Lock()
	defer gamesMu.Unlock()
	registeredGames[g.ID] = g
}

// getGameInfo returns game info by ID (L22/L23: 用于获取游戏显示名)
func getGameInfo(gameID string) (GameInfo, bool) {
	gamesMu.RLock()
	defer gamesMu.RUnlock()
	g, ok := registeredGames[gameID]
	return g, ok
}

// parsePlayers parses the PlayersJSON field into a slice.
func parsePlayers(playersJSON string) []map[string]interface{} {
	var players []map[string]interface{}
	if playersJSON != "" {
		_ = json.Unmarshal([]byte(playersJSON), &players)
	}
	if players == nil {
		players = []map[string]interface{}{}
	}
	return players
}

// parseSpectators parses the SpectatorsJSON field into a slice.
func parseSpectators(spectatorsJSON string) []map[string]interface{} {
	var spectators []map[string]interface{}
	if spectatorsJSON != "" {
		_ = json.Unmarshal([]byte(spectatorsJSON), &spectators)
	}
	if spectators == nil {
		spectators = []map[string]interface{}{}
	}
	return spectators
}

// isPlayerInSession checks whether the user is already a player in the session.
func isPlayerInSession(players []map[string]interface{}, userID string) bool {
	for _, p := range players {
		if pid, ok := p["userId"].(string); ok && pid == userID {
			return true
		}
	}
	return false
}

// getPlayerIndex returns the index of the user in players, or -1 if not found.
func getPlayerIndex(players []map[string]interface{}, userID string) int {
	for i, p := range players {
		if pid, ok := p["userId"].(string); ok && pid == userID {
			return i
		}
	}
	return -1
}

// parseState parses the session.State JSON into a map.
func parseState(stateJSON string) map[string]interface{} {
	var state map[string]interface{}
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		state = make(map[string]interface{})
	}
	if state == nil {
		state = make(map[string]interface{})
	}
	return state
}

// marshalState serializes a state map to JSON string.
func marshalState(state map[string]interface{}) string {
	b, _ := json.Marshal(state)
	return string(b)
}

// nextPlayerID returns the next player's userId in a round-robin fashion.
func nextPlayerID(players []map[string]interface{}, currentUserID string) string {
	idx := getPlayerIndex(players, currentUserID)
	if idx < 0 || len(players) == 0 {
		return ""
	}
	nextIdx := (idx + 1) % len(players)
	if nid, ok := players[nextIdx]["userId"].(string); ok {
		return nid
	}
	return ""
}

// isSpectatorInSession checks whether the user is already a spectator in the session.
func isSpectatorInSession(spectators []map[string]interface{}, userID string) bool {
	for _, s := range spectators {
		if sid, ok := s["userId"].(string); ok && sid == userID {
			return true
		}
	}
	return false
}

// Handler handles minigame HTTP requests
type Handler struct {
	db      *gorm.DB
	cfg     *config.Config
	hub     *realtime.Hub
	engines *EngineRegistry
}

// NewHandler creates a new minigame handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{db: db, cfg: cfg, hub: hub, engines: NewEngineRegistry()}
}

// RegisterRoutes registers minigame routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	mg := r.Group("/minigames")
	mg.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		mg.GET("/list", h.GetGames)
		mg.POST("/join", h.Join)
		mg.POST("/leave", h.Leave)
		mg.POST("/action", h.Action)
		// L24: 断线重连状态恢复
		mg.GET("/sessions/:id/state", h.GetState)
		// L23: 游戏内聊天
		mg.POST("/chat", h.Chat)
		// L22: 房间列表
		mg.GET("/rooms", h.GetRooms)
		mg.POST("/invite", h.Invite)
		// Leaderboard
		mg.POST("/leaderboard", h.SubmitScore)
		mg.GET("/leaderboard", h.GetLeaderboard)
		mg.GET("/scores/me", h.GetMyScore)
		// §15.4: 游戏历史记录
		mg.GET("/history", h.GetHistory)
	}
}

// AuthorizeRoom is used by the realtime Hub to allow/deny room subscriptions.
func (h *Handler) AuthorizeRoom(userID, roomID string) bool {
	if userID == "" || roomID == "" {
		return false
	}
	var session model.MinigameSession
	if err := h.db.First(&session, "id = ? AND is_active = ?", roomID, true).Error; err != nil {
		return false
	}
	return h.checkSpaceMembership(session.SpaceID, userID)
}

// GetGames returns available games (L21: 从注册表读取，支持动态扩展)
func (h *Handler) GetGames(c *gin.Context) {
	gamesMu.RLock()
	defer gamesMu.RUnlock()
	games := make([]GameInfo, 0, len(registeredGames))
	for _, g := range registeredGames {
		games = append(games, g)
	}
	errors.Success(c, games)
}

// checkSpaceMembership verifies the user is a member of the given space.
func (h *Handler) checkSpaceMembership(spaceID, userID string) bool {
	if spaceID == "" || userID == "" {
		return false
	}
	var count int64
	if err := h.db.Model(&model.Membership{}).
		Where("space_id = ? AND user_id = ?", spaceID, userID).
		Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

// broadcastToRoom sends a minigame event to all subscribers of the session's room.
func (h *Handler) broadcastToRoom(sessionID string, payload map[string]interface{}) {
	if h.hub == nil {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type":    "minigame_event",
		"payload": payload,
	})
	h.hub.BroadcastToRoom(sessionID, data)
}

// broadcastChatToRoom sends a minigame chat event to all room subscribers.
func (h *Handler) broadcastChatToRoom(sessionID string, payload map[string]interface{}) {
	if h.hub == nil {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type":    "minigame_chat",
		"payload": payload,
	})
	h.hub.BroadcastToRoom(sessionID, data)
}

// Join creates or joins a game session.
// If sessionId is provided, join existing room; otherwise create a new room.
// When joining, the user becomes a player if the room is waiting and not full,
// otherwise becomes a spectator once the game has started.
func (h *Handler) Join(c *gin.Context) {
	var body struct {
		GameType  string `json:"gameType"`
		SessionID string `json:"sessionId"`
		RuleMode  string `json:"ruleMode"` // 斗地主规则模式：classic | laizi（可选，默认 classic）
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if body.SessionID == "" && body.GameType == "" {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("gameType or sessionId is required"))
		return
	}

	userID := middleware.GetUserID(c)
	// username 已退役：玩家/旁观者名用显示名（history.lookupDisplayName）
	username := h.lookupDisplayName(userID)

	var session model.MinigameSession
	if body.SessionID != "" {
		// Join existing room
		if err := h.db.First(&session, "id = ? AND is_active = ?", body.SessionID, true).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				errors.JSONError(c, errors.New(errors.MINIGAME_NOT_FOUND, "room not found"))
				return
			}
			errors.JSONError(c, errors.ErrInternal)
			return
		}
		// Verify user belongs to the room's space
		if !h.checkSpaceMembership(session.SpaceID, userID) {
			errors.JSONError(c, errors.ErrForbidden)
			return
		}
	} else {
		// Create new room
		spaceID, err := middleware.RequireSpaceID(c)
		if err != nil {
			errors.JSONError(c, err)
			return
		}
		if !h.checkSpaceMembership(spaceID, userID) {
			errors.JSONError(c, errors.ErrForbidden)
			return
		}
		// 单机游戏（LocalOnly，如 2048/单机井字棋/单机国际象棋）没有服务端引擎，
		// 建房只会得到一个人数永远不足、无法开局的死房间 —— 直接拒绝（设计 §15.3.3）。
		if info, ok := getGameInfo(body.GameType); ok && !info.Online {
			errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "game does not support online play"))
			return
		}

		now := time.Now().UTC()
		// 斗地主规则模式写入初始 state，对局期间不可变更（§15.10 R8）。
		initialState := "{}"
		if body.GameType == "doudizhu" {
			ruleMode := body.RuleMode
			if ruleMode != "laizi" {
				ruleMode = "classic"
			}
			initialState = marshalState(map[string]interface{}{"ruleMode": ruleMode})
		}
		session = model.MinigameSession{
			ID:             idgen.GenerateID(idgen.PrefixGame),
			GameType:       body.GameType,
			SpaceID:        spaceID,
			HostID:         userID,
			State:          initialState,
			Status:         "waiting",
			PlayersJSON:    "[]",
			SpectatorsJSON: "[]",
			IsActive:       true,
			LastJoinedAt:   &now,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := h.db.Create(&session).Error; err != nil {
			errors.JSONError(c, errors.ErrInternal)
			return
		}
	}

	players := parsePlayers(session.PlayersJSON)
	spectators := parseSpectators(session.SpectatorsJSON)

	// If already in session (player or spectator), just return current state.
	if isPlayerInSession(players, userID) || isSpectatorInSession(spectators, userID) {
		h.sendJoinResponse(c, &session, players, spectators)
		return
	}

	role := ""
	switch session.Status {
	case "waiting":
		info, ok := getGameInfo(session.GameType)
		if ok && info.Players > 0 && len(players) >= info.Players {
			errors.JSONError(c, errors.New(errors.MINIGAME_ROOM_FULL, "room is full; wait for the game to start to spectate"))
			return
		}
		players = append(players, map[string]interface{}{
			"userId":   userID,
			"username": username,
			"joinedAt": time.Now().UTC().Unix(),
		})
		playersJSON, _ := json.Marshal(players)
		session.PlayersJSON = string(playersJSON)
		now := time.Now().UTC()
		session.LastJoinedAt = &now
		role = "player"
	case "playing":
		spectators = append(spectators, map[string]interface{}{
			"userId":   userID,
			"username": username,
			"joinedAt": time.Now().UTC().Unix(),
		})
		spectatorsJSON, _ := json.Marshal(spectators)
		session.SpectatorsJSON = string(spectatorsJSON)
		role = "spectator"
	case "ended":
		errors.JSONError(c, errors.New(errors.MINIGAME_GAME_ALREADY_ENDED, "game has ended"))
		return
	default:
		errors.JSONError(c, errors.ErrInternal.WithDetails("unknown session status"))
		return
	}

	session.UpdatedAt = time.Now().UTC()
	if err := h.db.Save(&session).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	h.broadcastToRoom(session.ID, map[string]interface{}{
		"gameId":    session.ID,
		"eventType": "player_joined",
		"playerId":  userID,
		"username":  username,
		"role":      role,
		"gameType":  session.GameType,
	})

	h.sendJoinResponse(c, &session, players, spectators)
}

// sendJoinResponse returns the join response with players/spectators lists.
func (h *Handler) sendJoinResponse(c *gin.Context, session *model.MinigameSession, players, spectators []map[string]interface{}) {
	errors.Success(c, gin.H{
		"sessionId":  session.ID,
		"gameType":   session.GameType,
		"spaceId":    session.SpaceID,
		"hostId":     session.HostID,
		"status":     session.Status,
		"isActive":   session.IsActive,
		"players":    players,
		"spectators": spectators,
	})
}

// Leave ends a game session (host only) or records player/spectator leaving.
func (h *Handler) Leave(c *gin.Context) {
	var body struct {
		SessionID string `json:"sessionId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	// username 已退役：玩家/旁观者名用显示名（history.lookupDisplayName）
	username := h.lookupDisplayName(userID)

	var session model.MinigameSession
	if err := h.db.First(&session, "id = ?", body.SessionID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.MINIGAME_NOT_FOUND, "room not found"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	wasPlayer := false
	wasSpectator := false

	// Remove user from PlayersJSON
	if session.PlayersJSON != "" {
		players := parsePlayers(session.PlayersJSON)
		filtered := make([]map[string]interface{}, 0, len(players))
		for _, p := range players {
			if pid, ok := p["userId"].(string); ok && pid != userID {
				filtered = append(filtered, p)
			} else {
				wasPlayer = true
			}
		}
		playersJSON, _ := json.Marshal(filtered)
		session.PlayersJSON = string(playersJSON)
	}

	// Remove user from SpectatorsJSON
	if session.SpectatorsJSON != "" {
		spectators := parseSpectators(session.SpectatorsJSON)
		filtered := make([]map[string]interface{}, 0, len(spectators))
		for _, s := range spectators {
			if sid, ok := s["userId"].(string); ok && sid != userID {
				filtered = append(filtered, s)
			} else {
				wasSpectator = true
			}
		}
		spectatorsJSON, _ := json.Marshal(filtered)
		session.SpectatorsJSON = string(spectatorsJSON)
	}

	if !wasPlayer && !wasSpectator {
		errors.Success(c, gin.H{"message": "left"})
		return
	}

	// Host leaving or last player leaving marks session inactive/ended.
	players := parsePlayers(session.PlayersJSON)
	if session.HostID == userID || (wasPlayer && len(players) == 0) {
		session.IsActive = false
		session.Status = "ended"
	}
	session.UpdatedAt = time.Now().UTC()
	if err := h.db.Save(&session).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	eventType := "player_left"
	if session.HostID == userID {
		eventType = "game_ended"
	}
	h.broadcastToRoom(session.ID, map[string]interface{}{
		"gameId":    session.ID,
		"eventType": eventType,
		"playerId":  userID,
		"username":  username,
	})

	errors.Success(c, gin.H{"message": "left"})
}

// Action records a game action and updates session state.
// Only players can submit actions; spectators are not allowed.
func (h *Handler) Action(c *gin.Context) {
	var body struct {
		SessionID string          `json:"sessionId" binding:"required"`
		Action    string          `json:"action" binding:"required"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	// username 已退役：玩家/旁观者名用显示名（history.lookupDisplayName）
	username := h.lookupDisplayName(userID)

	var session model.MinigameSession
	if err := h.db.First(&session, "id = ? AND is_active = ?", body.SessionID, true).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.MINIGAME_NOT_FOUND, "room not found"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	if !h.checkSpaceMembership(session.SpaceID, userID) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	players := parsePlayers(session.PlayersJSON)
	if !isPlayerInSession(players, userID) {
		errors.JSONError(c, errors.New(errors.MINIGAME_NOT_IN_ROOM, "not a player in this session"))
		return
	}

	// Handle game_start / game_restart: (re)initialize the authoritative state.
	// game_start 从 waiting 开局；game_restart 是设计 §15.3.4 的「再来一局」，
	// 保留房间与玩家、重置 state，可在 playing/ended 后由房主发起。
	if body.Action == "game_start" || body.Action == "game_restart" {
		info, ok := getGameInfo(session.GameType)
		if !ok || !info.Online {
			errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "game does not support online play"))
			return
		}
		if session.HostID != userID {
			errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "only host can start the game"))
			return
		}
		if body.Action == "game_start" {
			if session.Status != "waiting" {
				errors.JSONError(c, errors.New(errors.MINIGAME_GAME_ALREADY_STARTED, "game already started"))
				return
			}
		} else if session.Status != "playing" && session.Status != "ended" {
			errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "game is not in progress"))
			return
		}
		if len(players) < info.MinPlayersOrDefault() {
			errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "not enough players"))
			return
		}

		engine, ok := h.engines.Get(session.GameType)
		if !ok {
			errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "no engine for this game"))
			return
		}
		// 斗地主规则模式：读取建房时写入 state 的 ruleMode，用模式感知初始化。
		var initialState map[string]interface{}
		var initErr error
		if session.GameType == "doudizhu" {
			ruleMode := "classic"
			if rm, ok := parseState(session.State)["ruleMode"].(string); ok && rm != "" {
				ruleMode = rm
			}
			if mi, ok2 := engine.(interface {
				InitWithMode(players []map[string]interface{}, mode string) (map[string]interface{}, error)
			}); ok2 {
				initialState, initErr = mi.InitWithMode(players, ruleMode)
			} else {
				initialState, initErr = engine.Init(players)
			}
		} else {
			initialState, initErr = engine.Init(players)
		}
		if initErr != nil {
			errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, initErr.Error()))
			return
		}
		initialState["actions"] = []interface{}{}
		// 斗地主的 ruleMode 需在重开时保留（InitWithMode 不会写回该字段）。
		if rm, ok := parseState(session.State)["ruleMode"]; ok {
			initialState["ruleMode"] = rm
		}
		session.State = marshalState(initialState)
		session.Status = "playing"
		session.IsActive = true
	}

	// §15.4: 追踪游戏结束状态，用于在保存后写入历史记录
	var gameEnded bool
	var endedWinnerID string
	var endedIsDraw bool

	// For all gameplay actions (except game_start/game_restart which are handled above),
	// forward to the engine's ValidateMove. Each engine declares its own
	// action vocabulary (move_made/roll_dice/move_pawn/bid/play/werewolf_kill/
	// seer_check/vote/next_phase) and returns util.ErrInvalidAction for
	// unrecognized actions, so no handler-level whitelist is needed.
	if body.Action != "game_start" && body.Action != "game_restart" {
		if session.Status != "playing" {
			errors.JSONError(c, errors.New(errors.MINIGAME_GAME_NOT_STARTED, "game is not in progress"))
			return
		}
		engine, ok := h.engines.Get(session.GameType)
		if !ok {
			errors.JSONError(c, errors.New(errors.MINIGAME_INVALID_ACTION, "no engine for this game"))
			return
		}
		state := parseState(session.State)
		newState, err := engine.ValidateMove(state, userID, body.Action, body.Payload)
		if err != nil {
			code := errors.MINIGAME_INVALID_ACTION
			if err == util.ErrNotYourTurn {
				code = errors.MINIGAME_NOT_YOUR_TURN
			}
			errors.JSONError(c, errors.New(code, err.Error()))
			return
		}
		winnerID, draw := engine.IsGameOver(newState)
		if winnerID != "" || draw {
			session.Status = "ended"
			gameEnded = true
			endedWinnerID = winnerID
			endedIsDraw = draw
			if draw {
				newState["winner"] = "draw"
			} else {
				newState["winner"] = winnerID
			}
		}
		session.State = marshalState(newState)
	}

	// Update session state with the action
	state := parseState(session.State)
	actions, _ := state["actions"].([]interface{})
	actions = append(actions, map[string]interface{}{
		"userId":    userID,
		"username":  username,
		"action":    body.Action,
		"payload":   body.Payload,
		"timestamp": time.Now().UTC().Unix(),
	})
	state["actions"] = actions

	stateJSON, _ := json.Marshal(state)
	session.State = string(stateJSON)
	session.UpdatedAt = time.Now().UTC()

	if err := h.db.Save(&session).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	// §15.4: 游戏结束时写入历史记录（写入失败不影响主流程）
	if gameEnded {
		h.RecordGameEnd(&session, endedWinnerID, endedIsDraw)
	}

	broadcast := map[string]interface{}{
		"gameId":    session.ID,
		"eventType": body.Action,
		"playerId":  userID,
		"username":  username,
		"data":      body.Payload,
		"status":    session.Status,
	}
	// 公开信息游戏（井字棋/五子棋/国际象棋等不实现 SpectatorStateProvider 的引擎）
	// 直接在广播中携带权威 state，客户端不必再用「发起方提交的 payload」拼局面
	// ——此前国际象棋客户端只发 {from,to}，导致其他人每步都看到开局盘面。
	// 隐藏信息游戏（斗地主/狼人杀）不下发 state，仍由各客户端按用户拉取脱敏的 GetState。
	if engine, ok := h.engines.Get(session.GameType); ok {
		if _, needsMasking := engine.(SpectatorStateProvider); !needsMasking {
			// actions 是内部动作日志，不下发（体积随对局增长）。
			broadcastState := parseState(session.State)
			delete(broadcastState, "actions")
			broadcast["state"] = broadcastState
		}
	}
	h.broadcastToRoom(session.ID, broadcast)

	errors.Success(c, gin.H{
		"message":   "action received",
		"sessionId": session.ID,
		"status":    session.Status,
	})
}

// initialChessBoard returns the starting board for online chess.
func initialChessBoard() [][]interface{} {
	// 8x8 board; uppercase = white, lowercase = black.
	return [][]interface{}{
		{"r", "n", "b", "q", "k", "b", "n", "r"},
		{"p", "p", "p", "p", "p", "p", "p", "p"},
		{nil, nil, nil, nil, nil, nil, nil, nil},
		{nil, nil, nil, nil, nil, nil, nil, nil},
		{nil, nil, nil, nil, nil, nil, nil, nil},
		{nil, nil, nil, nil, nil, nil, nil, nil},
		{"P", "P", "P", "P", "P", "P", "P", "P"},
		{"R", "N", "B", "Q", "K", "B", "N", "R"},
	}
}

// ===== L24: 断线重连状态恢复 =====

// GetState returns the current game session state for reconnection (L24)
func (h *Handler) GetState(c *gin.Context) {
	sessionID := c.Param("id")
	userID := middleware.GetUserID(c)

	var session model.MinigameSession
	if err := h.db.First(&session, "id = ? AND is_active = ?", sessionID, true).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.MINIGAME_NOT_FOUND, "room not found"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	if !h.checkSpaceMembership(session.SpaceID, userID) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	// §3.5 惰性超时：拉取状态时触发一次超时托管（实现 TickTimeout 的引擎，如斗地主/狼人杀）。
	// 若当前阶段/回合超时，引擎自动推进并持久化 + 广播，保证对局推进。
	if session.Status == "playing" {
		if engine, ok := h.engines.Get(session.GameType); ok {
			if ticker, ok2 := engine.(interface {
				TickTimeout(state map[string]interface{}) (map[string]interface{}, bool)
			}); ok2 {
				state := parseState(session.State)
				if newState, changed := ticker.TickTimeout(state); changed {
					session.State = marshalState(newState)
					session.UpdatedAt = time.Now().UTC()
					if err := h.db.Save(&session).Error; err == nil {
						if winnerID, draw := engine.IsGameOver(newState); winnerID != "" || draw {
							session.Status = "ended"
							h.db.Save(&session)
							h.RecordGameEnd(&session, winnerID, draw)
						}
						h.broadcastToRoom(session.ID, map[string]interface{}{
							"gameId":    session.ID,
							"eventType": "play",
							"auto":      true,
							"status":    session.Status,
						})
					}
				}
			}
		}
	}

	players := parsePlayers(session.PlayersJSON)
	spectators := parseSpectators(session.SpectatorsJSON)

	// §15.7.5.2: 观战者返回脱敏 state（隐藏手牌/角色等私密信息）。
	// 玩家与房主返回完整 state；棋类等无私密信息的游戏直接返回完整 state。
	stateStr := session.State
	isSpectator := !isPlayerInSession(players, userID) && isSpectatorInSession(spectators, userID)
	if isSpectator {
		if engine, ok := h.engines.Get(session.GameType); ok {
			if spec, ok2 := engine.(SpectatorStateProvider); ok2 {
				masked := spec.SpectatorState(parseState(session.State))
				stateStr = marshalState(masked)
			}
		}
	}

	errors.Success(c, gin.H{
		"sessionId":  session.ID,
		"gameType":   session.GameType,
		"spaceId":    session.SpaceID,
		"hostId":     session.HostID,
		"status":     session.Status,
		"state":      stateStr,
		"players":    players,
		"spectators": spectators,
		"isActive":   session.IsActive,
		"updatedAt":  session.UpdatedAt,
	})
}

// ===== L23: 游戏内聊天 =====

// Chat sends a game chat message to all room subscribers (L23).
// Both players and spectators can send chat messages.
func (h *Handler) Chat(c *gin.Context) {
	var body struct {
		SessionID string `json:"sessionId" binding:"required"`
		Content   string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	if len(body.Content) == 0 || len(body.Content) > 2000 {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("content length must be 1-2000"))
		return
	}

	userID := middleware.GetUserID(c)
	// username 已退役：玩家/旁观者名用显示名（history.lookupDisplayName）
	username := h.lookupDisplayName(userID)

	var session model.MinigameSession
	if err := h.db.First(&session, "id = ? AND is_active = ?", body.SessionID, true).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.MINIGAME_NOT_FOUND, "room not found"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	if !h.checkSpaceMembership(session.SpaceID, userID) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	players := parsePlayers(session.PlayersJSON)
	spectators := parseSpectators(session.SpectatorsJSON)
	if !isPlayerInSession(players, userID) && !isSpectatorInSession(spectators, userID) {
		errors.JSONError(c, errors.New(errors.MINIGAME_NOT_IN_ROOM, "not in this session"))
		return
	}

	// Broadcast chat to room subscribers
	h.broadcastChatToRoom(session.ID, map[string]interface{}{
		"sessionId": session.ID,
		"userId":    userID,
		"username":  username,
		"content":   body.Content,
		"timestamp": time.Now().UTC().Unix(),
	})

	errors.Success(c, gin.H{"message": "chat sent"})
}

// ===== L22: 房间列表 =====

// GetRooms returns active game sessions in the current or specified space.
func (h *Handler) GetRooms(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	gameType := c.Query("gameType")

	userID := middleware.GetUserID(c)
	if !h.checkSpaceMembership(spaceID, userID) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	// 已结束的房间不再出现在「活跃房间」列表（可「再来一局」的窗口期内由房主直接从等待页重开）。
	query := h.db.Where("space_id = ? AND is_active = ? AND status <> ?", spaceID, true, "ended")
	if gameType != "" {
		query = query.Where("game_type = ?", gameType)
	}

	var sessions []model.MinigameSession
	if err := query.Find(&sessions).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	result := make([]map[string]interface{}, 0, len(sessions))
	for _, s := range sessions {
		players := parsePlayers(s.PlayersJSON)
		spectators := parseSpectators(s.SpectatorsJSON)
		gameName := s.GameType
		if info, ok := getGameInfo(s.GameType); ok {
			gameName = info.Name
		}
		result = append(result, map[string]interface{}{
			"sessionId":      s.ID,
			"gameType":       s.GameType,
			"gameName":       gameName,
			"hostId":         s.HostID,
			"status":         s.Status,
			"players":        players,
			"playerCount":    len(players),
			"spectators":     spectators,
			"spectatorCount": len(spectators),
		})
	}

	errors.Success(c, gin.H{
		"spaceId": spaceID,
		"games":   result,
	})
}

// Invite sends a game invitation to a specific user.
func (h *Handler) Invite(c *gin.Context) {
	var body struct {
		SessionID string `json:"sessionId" binding:"required"`
		ToUserID  string `json:"toUserId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest)
		return
	}

	userID := middleware.GetUserID(c)
	// username 已退役：邀请方名用显示名
	username := h.lookupDisplayName(userID)

	var session model.MinigameSession
	if err := h.db.First(&session, "id = ? AND is_active = ?", body.SessionID, true).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			errors.JSONError(c, errors.New(errors.MINIGAME_NOT_FOUND, "room not found"))
			return
		}
		errors.JSONError(c, errors.ErrInternal)
		return
	}

	if !h.checkSpaceMembership(session.SpaceID, userID) {
		errors.JSONError(c, errors.ErrForbidden)
		return
	}

	players := parsePlayers(session.PlayersJSON)
	spectators := parseSpectators(session.SpectatorsJSON)
	if !isPlayerInSession(players, userID) && !isSpectatorInSession(spectators, userID) {
		errors.JSONError(c, errors.New(errors.MINIGAME_NOT_IN_ROOM, "not in this session"))
		return
	}

	gameName := session.GameType
	if info, ok := getGameInfo(session.GameType); ok {
		gameName = info.Name
	}

	if body.ToUserID != "" {
		if h.hub != nil {
			data, _ := json.Marshal(map[string]interface{}{
				"type": "minigame_invite",
				"payload": map[string]interface{}{
					"sessionId": session.ID,
					"gameType":  session.GameType,
					"gameName":  gameName,
					"fromUser":  username,
					"message":   fmt.Sprintf("%s 邀请你加入 %s 房间", username, gameName),
				},
			})
			h.hub.BroadcastToUser(body.ToUserID, data)
		}
	}

	errors.Success(c, gin.H{"message": "invitation sent"})
}

// ===== Room cleanup =====

// StartCleanupLoop starts a background goroutine that removes waiting rooms
// that have had no player join for 10 minutes, and advances timed game phases
// (werewolf judge / doudizhu turn timeout) even when no client is polling.
func (h *Handler) StartCleanupLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.cleanupStaleRooms()
			}
		}
	}()
	// 系统法官/回合超时主动推进：独立于客户端轮询，保证无人操作时阶段仍推进。
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.tickTimedGames()
			}
		}
	}()
}

// tickTimedGames 遍历进行中的会话，调用实现了 TickTimeout 的引擎推进超时阶段。
// 这是系统法官（狼人杀昼夜/投票）与斗地主回合超时的主动驱动，
// 不依赖客户端 GetState 轮询（前端组件可能不在触发轮询的视图）。
func (h *Handler) tickTimedGames() {
	var sessions []model.MinigameSession
	if err := h.db.Where("is_active = ? AND status = ?", true, "playing").Find(&sessions).Error; err != nil {
		return
	}
	for i := range sessions {
		s := &sessions[i]
		engine, ok := h.engines.Get(s.GameType)
		if !ok {
			continue
		}
		ticker, ok := engine.(interface {
			TickTimeout(state map[string]interface{}) (map[string]interface{}, bool)
		})
		if !ok {
			continue
		}
		state := parseState(s.State)
		newState, changed := ticker.TickTimeout(state)
		if !changed {
			continue
		}
		s.State = marshalState(newState)
		s.UpdatedAt = time.Now().UTC()
		if err := h.db.Save(s).Error; err != nil {
			continue
		}
		if winnerID, draw := engine.IsGameOver(newState); winnerID != "" || draw {
			s.Status = "ended"
			h.db.Save(s)
			h.RecordGameEnd(s, winnerID, draw)
		}
		h.broadcastToRoom(s.ID, map[string]interface{}{
			"gameId":    s.ID,
			"eventType": "play",
			"auto":      true,
			"status":    s.Status,
		})
	}
}

func (h *Handler) cleanupStaleRooms() {
	now := time.Now().UTC()
	// 1) waiting 房间：10 分钟内无人加入 → 回收。
	h.reapRooms("waiting", now.Add(-10*time.Minute), "last_joined_at", "no player joined within 10 minutes")
	// 2) ended 房间：结束后 10 分钟内无人「再来一局」→ 回收（避免僵尸房间长期占用）。
	h.reapRooms("ended", now.Add(-10*time.Minute), "updated_at", "room ended and was not restarted")
	// 3) playing 房间：2 小时无任何动作 → 视为已放弃 → 回收。
	h.reapRooms("playing", now.Add(-2*time.Hour), "updated_at", "game abandoned (no action for 2 hours)")
}

// reapRooms deactivates sessions of the given status whose timeColumn is older than cutoff.
func (h *Handler) reapRooms(status string, cutoff time.Time, timeColumn, reason string) {
	var sessions []model.MinigameSession
	query := h.db.Where("is_active = ? AND status = ?", true, status)
	if status == "waiting" {
		query = query.Where("("+timeColumn+" IS NULL OR "+timeColumn+" < ?)", cutoff)
	} else {
		query = query.Where(timeColumn+" < ?", cutoff)
	}
	if err := query.Find(&sessions).Error; err != nil {
		return
	}
	for _, s := range sessions {
		s.IsActive = false
		s.Status = "ended"
		s.UpdatedAt = time.Now().UTC()
		if err := h.db.Save(&s).Error; err != nil {
			continue
		}
		h.broadcastToRoom(s.ID, map[string]interface{}{
			"gameId":    s.ID,
			"eventType": "room_expired",
			"reason":    reason,
		})
	}
}
