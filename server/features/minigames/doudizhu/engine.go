// Package doudizhu 实现斗地主权威规则引擎。
// 支持 3 人对局：发牌、叫地主、按牌型出牌。
// 牌型识别与比较由 ratel.go（移植自 ratel-online）提供，支持经典 + 癞子两种模式。
package doudizhu

import (
	"encoding/json"
	"math/rand"
	"sort"
	"time"

	"ridgericetalk/features/minigames/internal/util"
)

// Engine 实现斗地主权威规则。
type Engine struct{}

// New 创建一个斗地主引擎实例。
func New() *Engine {
	return &Engine{}
}

// PlayTimeout 为斗地主回合超时时长（秒），超时后服务端惰性托管。
const PlayTimeout = 30

type card struct {
	Rank  int    `json:"rank"`  // 3..17, 16=小王, 17=大王
	Suit  string `json:"suit"`
	Value string `json:"value"`
}

func (e *Engine) Init(players []map[string]interface{}) (map[string]interface{}, error) {
	return e.initWithMode(players, "classic")
}

// InitWithMode 以指定规则模式初始化对局（classic | laizi），供建房时按房主选择调用。
func (e *Engine) InitWithMode(players []map[string]interface{}, ruleMode string) (map[string]interface{}, error) {
	return e.initWithMode(players, ruleMode)
}

// initWithMode 以指定规则模式初始化对局（classic | laizi）。
func (e *Engine) initWithMode(players []map[string]interface{}, ruleMode string) (map[string]interface{}, error) {
	ids := util.PlayerUserIDs(players)
	if ruleMode != "laizi" {
		ruleMode = "classic"
	}
	state := make(map[string]interface{})
	state["status"] = "playing"
	state["players"] = ids
	state["currentPlayer"] = ids[0]
	state["winner"] = nil
	state["phase"] = "bid" // bid | play
	state["landlord"] = ""
	state["bids"] = make(map[string]int)
	state["ruleMode"] = ruleMode
	hands, bottom := e.deal(ids)
	state["hands"] = hands
	state["bottomCards"] = bottom
	state["lastPlay"] = nil
	state["lastPlayOwner"] = ""
	state["pendingScore"] = 0
	state["turnStartedAt"] = time.Now().UTC().Unix()
	if ruleMode == "laizi" {
		universals := ratelRandomLaizi() // 癞子点（ratel Key 1..13）
		state["universals"] = universals
	} else {
		state["universals"] = []int{}
	}
	return state, nil
}

func (e *Engine) ValidateMove(state map[string]interface{}, userID string, action string, payload json.RawMessage) (map[string]interface{}, error) {
	// 惰性超时托管：轮到当前玩家但已超时，先自动为其执行一着，再处理本次请求。
	state = e.applyLazyTimeout(state)

	phase, _ := state["phase"].(string)
	currentPlayer, _ := state["currentPlayer"].(string)
	if currentPlayer != "" && currentPlayer != userID {
		return nil, util.ErrNotYourTurn
	}

	switch action {
	case "bid":
		if phase != "bid" {
			return nil, util.ErrInvalidAction
		}
		var p struct {
			Score int `json:"score"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, util.ErrInvalidAction
		}
		if p.Score != 0 && p.Score != 1 && p.Score != 2 && p.Score != 3 {
			return nil, util.ErrInvalidAction
		}
		newState := util.ShallowCopy(state)
		bids := e.bidsMap(newState)
		if bids == nil {
			bids = make(map[string]int)
		}
		bids[userID] = p.Score
		newState["bids"] = bids
		if p.Score > 0 {
			newState["landlord"] = userID
			newState["pendingScore"] = p.Score
		}
		players := util.GetStringSlice(newState, "players")
		if len(bids) >= len(players) {
			landlord, _ := newState["landlord"].(string)
			if landlord == "" {
				// 无人叫分，重新发牌重开叫分
				newState["phase"] = "bid"
				newState["bids"] = make(map[string]int)
				newState["landlord"] = ""
				hands, bottom := e.deal(players)
				newState["hands"] = hands
				newState["bottomCards"] = bottom
				newState["currentPlayer"] = players[0]
			} else {
				// 地主收底牌，进入出牌阶段
				hands := e.handsMap(newState)
				bottom := e.bottomCards(newState)
				hands[landlord] = append(hands[landlord], bottom...)
				sort.Slice(hands[landlord], func(i, j int) bool { return hands[landlord][i].Rank < hands[landlord][j].Rank })
				newState["hands"] = hands
				newState["bottomCards"] = []card{}
				newState["phase"] = "play"
				newState["currentPlayer"] = landlord
				newState["lastPlay"] = nil
				newState["lastPlayOwner"] = ""
			}
		} else {
			newState["currentPlayer"] = util.NextPlayer(players, userID)
		}
		newState["turnStartedAt"] = time.Now().UTC().Unix()
		return newState, nil
	case "play":
		if phase != "play" {
			return nil, util.ErrInvalidAction
		}
		var p struct {
			Cards []card `json:"cards"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, util.ErrInvalidAction
		}

		newState := util.ShallowCopy(state)
		hands := e.handsMap(newState)

		// 不出（pass）：仅当上一手非自己所出时允许
		if len(p.Cards) == 0 {
			lastOwner, _ := newState["lastPlayOwner"].(string)
			if lastOwner == "" || lastOwner == userID {
				return nil, util.ErrInvalidAction
			}
			players := util.GetStringSlice(newState, "players")
			newState["currentPlayer"] = util.NextPlayer(players, userID)
			newState["turnStartedAt"] = time.Now().UTC().Unix()
			return newState, nil
		}

		// 校验所选牌是否都在手牌中
		if !e.containsCards(hands[userID], p.Cards) {
			return nil, util.ErrInvalidAction
		}

		// 权威牌型校验：转换为 ratel 牌并判定，处理癞子替换
		sells, ok := e.resolvePlay(p.Cards, hands[userID], newState)
		if !ok {
			return nil, util.ErrInvalidAction
		}
		playFaces := ratelParseFaces(sells)
		if len(playFaces) == 0 {
			return nil, util.ErrInvalidAction
		}

		// 与上一手比较（若非首家）
		lastOwner, _ := newState["lastPlayOwner"].(string)
		if lastOwner != "" && lastOwner != userID {
			lastSells, ok := e.resolvePlay(e.lastPlay(newState), nil, newState)
			if !ok {
				return nil, util.ErrInvalidAction
			}
			lastFaces := ratelParseFaces(lastSells)
			if len(lastFaces) == 0 {
				return nil, util.ErrInvalidAction
			}
			access := false
			for _, f := range playFaces {
				if f.Compare(lastFaces[0]) {
					access = true
					break
				}
			}
			if !access {
				return nil, util.ErrInvalidAction
			}
		}

		newHands := e.removeCards(hands[userID], p.Cards)
		hands[userID] = newHands
		newState["hands"] = hands
		newState["lastPlay"] = p.Cards
		newState["lastPlayOwner"] = userID
		if len(newHands) == 0 {
			newState["winner"] = userID
			newState["turnStartedAt"] = time.Now().UTC().Unix()
			return newState, nil
		}
		players := util.GetStringSlice(newState, "players")
		newState["currentPlayer"] = util.NextPlayer(players, userID)
		newState["turnStartedAt"] = time.Now().UTC().Unix()
		return newState, nil
	default:
		return nil, util.ErrInvalidAction
	}
}

func (e *Engine) IsGameOver(state map[string]interface{}) (winnerID string, draw bool) {
	winner := ""
	if w, ok := state["winner"].(string); ok {
		winner = w
	}
	return winner, false
}

// SpectatorState 返回脱敏后的观战 state（§15.7.5.2）。
// 隐藏三家手牌明细，仅保留各玩家剩余牌数；底牌亮出后公开。
func (e *Engine) SpectatorState(state map[string]interface{}) map[string]interface{} {
	masked := util.ShallowCopy(state)
	hands := e.handsMap(state)
	handCounts := make(map[string]int, len(hands))
	for uid, hand := range hands {
		handCounts[uid] = len(hand)
	}
	delete(masked, "hands")
	masked["handCounts"] = handCounts
	return masked
}

// ===== ratel 适配层 =====

// 项目 card.Rank(3..17) 语义：3..10=3..10, 11=J, 12=Q, 13=K, 14=A, 15=2, 16=小王, 17=大王。
// ratel Key(1..15) 语义：1=A, 2=2, 3..11=3..J, 12=Q, 13=K, 14=小王, 15=大王。
var rankToRatelKey = map[int]int{
	3: 3, 4: 4, 5: 5, 6: 6, 7: 7, 8: 8, 9: 9, 10: 10,
	11: 11, 12: 12, 13: 13, // J/Q/K
	14: 1,  // A
	15: 2,  // 2
	16: 14, // 小王
	17: 15, // 大王
}

// cardToRatelKey 把项目 card.Rank(3..17) 转成 ratel Key(1..15)。
func cardToRatelKey(rank int) int {
	if k, ok := rankToRatelKey[rank]; ok {
		return k
	}
	return 0 // 非法（ratelParseFacesCore 会拒绝 Key<1）
}

// toRatelPokers 把项目 card 列表转成 ratel 牌，并标记癞子。
func (e *Engine) toRatelPokers(cards []card, universals []int) ratelPokers {
	pokers := make(ratelPokers, 0, len(cards))
	for _, c := range cards {
		key := cardToRatelKey(c.Rank)
		p := ratelPoker{Key: key, Desc: c.Value}
		p.Val = landlordRules.Value(p.Key) // 显式赋值（ratelParseFacesCore 的 Val 赋值在副本上，不影响切片元素）
		pokers = append(pokers, p)
	}
	if len(universals) > 0 {
		pokers.setOaa(universals...)
	}
	return pokers
}

// resolvePlay 把玩家所选 card 转成用于牌型判定的 ratel 出牌。
// 癞子模式下，先用手牌上下文做癞子替换；hand 为 nil 时（校验上一手）直接转换。
func (e *Engine) resolvePlay(selected []card, hand []card, state map[string]interface{}) (ratelPokers, bool) {
	universals := e.universalsSlice(state)
	if len(universals) == 0 || hand == nil {
		// 经典模式，或不需替换（上一手已定型）：直接转换并标记癞子
		return e.toRatelPokers(selected, universals), true
	}
	// 癞子模式：把所选牌转 key 序列，结合手牌做癞子替换
	selKeys := make([]int, 0, len(selected))
	for _, c := range selected {
		selKeys = append(selKeys, cardToRatelKey(c.Rank))
	}
	ratelHand := e.toRatelPokers(hand, universals)
	return ratelResolveLaizi(selKeys, ratelHand)
}

func (e *Engine) universalsSlice(state map[string]interface{}) []int {
	if arr, ok := state["universals"].([]int); ok {
		return arr
	}
	out := make([]int, 0)
	if raw, ok := state["universals"].([]interface{}); ok {
		for _, v := range raw {
			if n, ok := v.(float64); ok {
				out = append(out, int(n))
			}
		}
	}
	return out
}

// ===== 惰性超时托管 =====

// TickTimeout 供外部（handler GetState）惰性触发超时托管。
// 若当前玩家已超时，自动为其执行一着并返回更新后的 state 与 true；否则返回原 state 与 false。
func (e *Engine) TickTimeout(state map[string]interface{}) (map[string]interface{}, bool) {
	if !e.isTimedOut(state) {
		return state, false
	}
	return e.applyTimeoutAction(state), true
}

// isTimedOut 判断是否已进入出牌阶段且当前玩家超时。
func (e *Engine) isTimedOut(state map[string]interface{}) bool {
	phase, _ := state["phase"].(string)
	if phase != "play" {
		return false
	}
	startedAt, _ := state["turnStartedAt"].(int64)
	if startedAt == 0 {
		if f, ok := state["turnStartedAt"].(float64); ok {
			startedAt = int64(f)
		}
	}
	if startedAt == 0 {
		return false
	}
	current, _ := state["currentPlayer"].(string)
	if current == "" {
		return false
	}
	return time.Now().UTC().Unix()-startedAt > PlayTimeout
}

// applyLazyTimeout 在 ValidateMove 入口调用：若当前玩家超时，先自动为其执行一着。
func (e *Engine) applyLazyTimeout(state map[string]interface{}) map[string]interface{} {
	if !e.isTimedOut(state) {
		return state
	}
	return e.applyTimeoutAction(state)
}

// applyTimeoutAction 为当前超时玩家自动执行一着（不出或出最小牌）。
func (e *Engine) applyTimeoutAction(state map[string]interface{}) map[string]interface{} {
	current, _ := state["currentPlayer"].(string)
	newState := util.ShallowCopy(state)
	lastOwner, _ := newState["lastPlayOwner"].(string)
	players := util.GetStringSlice(newState, "players")

	if lastOwner != "" && lastOwner != current {
		// 可不出：自动 pass
		newState["currentPlayer"] = util.NextPlayer(players, current)
		newState["turnStartedAt"] = time.Now().UTC().Unix()
		return newState
	}

	// 首家/上一手是自己：自动出最小合法牌（单张最小牌）
	hands := e.handsMap(newState)
	hand := hands[current]
	if len(hand) == 0 {
		return state
	}
	minCard := hand[0]
	for _, c := range hand {
		if c.Rank < minCard.Rank {
			minCard = c
		}
	}
	playCards := []card{minCard}
	sells, ok := e.resolvePlay(playCards, hand, newState)
	if !ok || len(ratelParseFaces(sells)) == 0 {
		return state
	}
	hands[current] = e.removeCards(hand, playCards)
	newState["hands"] = hands
	newState["lastPlay"] = playCards
	newState["lastPlayOwner"] = current
	if len(hands[current]) == 0 {
		newState["winner"] = current
	} else {
		newState["currentPlayer"] = util.NextPlayer(players, current)
	}
	newState["turnStartedAt"] = time.Now().UTC().Unix()
	return newState
}

// ===== 发牌与状态解析 =====

func (e *Engine) deal(players []string) (map[string][]card, []card) {
	deck := e.newDeck()
	rand.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
	hands := make(map[string][]card)
	for i, id := range players {
		start := i * 17
		end := start + 17
		hand := append([]card{}, deck[start:end]...)
		sort.Slice(hand, func(i, j int) bool { return hand[i].Rank < hand[j].Rank })
		hands[id] = hand
	}
	bottom := append([]card{}, deck[51:]...)
	sort.Slice(bottom, func(i, j int) bool { return bottom[i].Rank < bottom[j].Rank })
	return hands, bottom
}

func (e *Engine) newDeck() []card {
	suits := []string{"♠", "♥", "♣", "♦"}
	values := []string{"3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K", "A", "2"}
	deck := make([]card, 0, 54)
	for rank, value := range values {
		for _, suit := range suits {
			deck = append(deck, card{Rank: rank + 3, Suit: suit, Value: value})
		}
	}
	deck = append(deck, card{Rank: 16, Suit: "joker", Value: "小王"})
	deck = append(deck, card{Rank: 17, Suit: "joker", Value: "大王"})
	return deck
}

func (e *Engine) bidsMap(state map[string]interface{}) map[string]int {
	if m, ok := state["bids"].(map[string]int); ok {
		return m
	}
	if raw, ok := state["bids"].(map[string]interface{}); ok {
		m := make(map[string]int, len(raw))
		for k, v := range raw {
			if n, ok := v.(float64); ok {
				m[k] = int(n)
			} else if n, ok := v.(int); ok {
				m[k] = n
			}
		}
		return m
	}
	return make(map[string]int)
}

func (e *Engine) handsMap(state map[string]interface{}) map[string][]card {
	if m, ok := state["hands"].(map[string][]card); ok {
		return m
	}
	out := make(map[string][]card)
	if raw, ok := state["hands"].(map[string]interface{}); ok {
		for userID, v := range raw {
			out[userID] = parseCardsInterface(v)
		}
	}
	return out
}

func (e *Engine) bottomCards(state map[string]interface{}) []card {
	return parseCardsInterface(state["bottomCards"])
}

func (e *Engine) lastPlay(state map[string]interface{}) []card {
	return parseCardsInterface(state["lastPlay"])
}

func parseCardsInterface(v interface{}) []card {
	if v == nil {
		return nil
	}
	if cards, ok := v.([]card); ok {
		return cards
	}
	if arr, ok := v.([]interface{}); ok {
		cards := make([]card, 0, len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]interface{}); ok {
				c := card{}
				if r, ok := m["rank"].(float64); ok {
					c.Rank = int(r)
				} else if r, ok := m["rank"].(int); ok {
					c.Rank = r
				}
				if s, ok := m["suit"].(string); ok {
					c.Suit = s
				}
				if s, ok := m["value"].(string); ok {
					c.Value = s
				}
				cards = append(cards, c)
			}
		}
		return cards
	}
	return nil
}

func (e *Engine) containsCards(hand, selected []card) bool {
	available := make(map[int]int)
	for _, c := range hand {
		available[c.Rank]++
	}
	for _, c := range selected {
		if available[c.Rank] <= 0 {
			return false
		}
		available[c.Rank]--
	}
	return true
}

func (e *Engine) removeCards(hand, selected []card) []card {
	counts := make(map[int]int)
	for _, c := range hand {
		counts[c.Rank]++
	}
	for _, c := range selected {
		counts[c.Rank]--
	}
	result := make([]card, 0, len(hand)-len(selected))
	for _, c := range hand {
		if counts[c.Rank] > 0 {
			result = append(result, c)
			counts[c.Rank]--
		}
	}
	return result
}
