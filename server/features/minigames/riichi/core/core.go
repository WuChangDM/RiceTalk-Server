// Package core 是日本麻将（立直麻将）的规则适配层。
//
// 本包是 features/minigames 下**唯一**允许 import 第三方规则库
// github.com/okki-0417/mahjong v0.1.0 的位置；上层 riichi/engine.go 与
// handler 只通过本包导出的纯 Go 类型访问规则能力，便于：
//   - 未来替换规则内核（DES-20261030-01 §2 风控：适配层隔离）
//   - 对局状态可脱离库回放（actions 是唯一事实源）
//
// 设计要点（DES §4/§6）：
//   - 座位 = PlayersJSON 下标（0..3），整场顺序固定
//   - 牌山由 (seed, handIndex) 确定性生成，回放不依赖随机源
//   - 每一手（kyoku）的动作序列单独存储；一手结束后按库的升段规则推进
//     东/局/本场/供托并自动发下一手
//   - 不做 chombo/多家和了（头跳由库保证）
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"

	"github.com/okki-0417/mahjong/kyoku"
	"github.com/okki-0417/mahjong/ruleset"
	"github.com/okki-0417/mahjong/tile"
)

// Seats 是日麻固定人数（东/南/西/北 四家）。
const Seats = kyoku.Seats

// 对局类型（场制）。
const (
	MatchEast  = "east"  // 东风战：东 1-4 局
	MatchSouth = "south" // 半庄战：东 1-4 局 + 南 1-4 局
)

// 适配层错误。engine 会把它们映射为 internal/util 的通用错误。
var (
	ErrFinished      = errors.New("riichi: 对局已结束")
	ErrInvalidAction = errors.New("riichi: 非法或不合法的动作")
	ErrBadState      = errors.New("riichi: 对局状态损坏")
)

// 局风字符串（与库 tile.Wind.String() 一致）。
const (
	WindEast  = "east"
	WindSouth = "south"
)

// Config 是对局配置。
type Config struct {
	MatchType     string `json:"matchType"`     // east（默认）| south
	StartingScore int    `json:"startingScore"` // 默认 25000
	RedFives      bool   `json:"redFives"`      // 是否使用赤五（默认 true）
}

// Normalize 返回补齐默认值后的配置。
func (c Config) Normalize() Config {
	out := c
	if out.MatchType != MatchSouth {
		out.MatchType = MatchEast
	}
	if out.StartingScore <= 0 {
		out.StartingScore = 25000
	}
	return out
}

func (c Config) ruleSet() (ruleset.RuleSet, error) {
	rs, err := ruleset.Default().WithStartingScore(c.StartingScore)
	if err != nil {
		return ruleset.RuleSet{}, err
	}
	return rs, nil
}

// Position 是一手的起始信息（东/局/本场/供托/亲家/点数）。
type Position struct {
	RoundWind    string     `json:"roundWind"`
	Kyoku        int        `json:"kyoku"` // 1..4
	Honba        int        `json:"honba"`
	RiichiSticks int        `json:"riichiSticks"`
	DealerSeat   int        `json:"dealerSeat"`
	Scores       [Seats]int `json:"scores"`
}

// RecordedAction 是持久化在 state 里的动作记录（脱离库也可读）。
type RecordedAction struct {
	Kind   string   `json:"kind"`
	Seat   int      `json:"seat"`
	Tiles  []string `json:"tiles,omitempty"`
	Called string   `json:"called,omitempty"`
}

// HandResult 是一手的结算记录。
type HandResult struct {
	RoundWind  string     `json:"roundWind"`
	Kyoku      int        `json:"kyoku"`
	Honba      int        `json:"honba"`
	DealerSeat int        `json:"dealerSeat"`
	Kind       string     `json:"kind"` // tsumo/ron/ryukyoku/...
	Winner     int        `json:"winner"`
	Loser      int        `json:"loser"`
	Han        int        `json:"han,omitempty"`
	Fu         int        `json:"fu,omitempty"`
	Yakuman    int        `json:"yakuman,omitempty"`
	Total      int        `json:"total,omitempty"`
	Yaku       []string   `json:"yaku,omitempty"`
	Deltas     [Seats]int `json:"deltas"`
	Scores     [Seats]int `json:"scores"`
}

// Snapshot 是写入 game state 的全部持久化字段。
type Snapshot struct {
	Seed       int64            `json:"seed"`
	Config     Config           `json:"config"`
	Position   Position         `json:"position"`
	HandIndex  int              `json:"handIndex"`
	Actions    []RecordedAction `json:"actions"`
	History    []HandResult     `json:"history"`
	Finished   bool             `json:"finished"`
	WinnerSeat int              `json:"winnerSeat"` // -1 表示无唯一头名
}

// Match 是一场对局（东风战/半庄）的全部状态。
type Match struct {
	cfg        Config
	rules      ruleset.RuleSet
	seed       int64
	pos        Position
	handIndex  int
	actions    []RecordedAction
	history    []HandResult
	finished   bool
	winnerSeat int
	cur        *kyoku.Kyoku
}

// NewMatch 开一场新对局：亲家 0 号座位、东 1 局起手。
// MaxSeed 是能安全通过 JSON number（float64，尾数 53 位）往返的 seed 上界（不含）。
//
// 生产路径会把 session.State 存为 DB 文本、再 json.Unmarshal 成 map[string]interface{}，
// 其中所有数字都变成 float64；>= 2^53 的整数会被舍入，牌山因此与写入时不一致，
// 表现为「偶发 invalid game action」。所以 seed 必须限制在 [0, MaxSeed) 内。
const MaxSeed int64 = 1 << 53

// validSeed 报告 seed 是否在 JSON number 安全范围内。
func validSeed(seed int64) bool { return seed >= 0 && seed < MaxSeed }

func NewMatch(seed int64, cfg Config) (*Match, error) {
	if !validSeed(seed) {
		return nil, fmt.Errorf("%w: 牌山 seed 必须在 [0, 2^53) 内（实际 %d），否则经 JSON 往返会精度丢失", ErrBadState, seed)
	}
	cfg = cfg.Normalize()
	rs, err := cfg.ruleSet()
	if err != nil {
		return nil, err
	}
	m := &Match{
		cfg:        cfg,
		rules:      rs,
		seed:       seed,
		winnerSeat: -1,
		pos: Position{
			RoundWind:  WindEast,
			Kyoku:      1,
			DealerSeat: 0,
		},
	}
	for i := range m.pos.Scores {
		m.pos.Scores[i] = cfg.StartingScore
	}
	if err := m.deal(); err != nil {
		return nil, err
	}
	return m, nil
}

// Restore 从持久化快照恢复对局（用于每次 action 前重建）。
func Restore(s Snapshot) (*Match, error) {
	// 注意：这里只拒绝负数。历史 state 可能存着 >2^53 的 seed（旧版本写入），
	// 只要其值恰能被 float64 精确表示，仍可还原出原始牌山；新对局由
	// NewMatch 限制在 <MaxSeed，避免产生新的精度问题。
	if s.Seed < 0 {
		return nil, fmt.Errorf("%w: 快照 seed 非法（%d）", ErrBadState, s.Seed)
	}
	cfg := s.Config.Normalize()
	rs, err := cfg.ruleSet()
	if err != nil {
		return nil, err
	}
	m := &Match{
		cfg:        cfg,
		rules:      rs,
		seed:       s.Seed,
		pos:        s.Position,
		handIndex:  s.HandIndex,
		actions:    append([]RecordedAction(nil), s.Actions...),
		history:    append([]HandResult(nil), s.History...),
		finished:   s.Finished,
		winnerSeat: s.WinnerSeat,
	}
	if m.winnerSeat == 0 && !s.Finished {
		m.winnerSeat = -1
	}
	if m.pos.Kyoku == 0 {
		m.pos.Kyoku = 1
	}
	if m.pos.RoundWind == "" {
		m.pos.RoundWind = WindEast
	}
	if err := m.deal(); err != nil {
		return nil, err
	}
	return m, nil
}

// wallFor 确定性地生成第 index 手的牌山：同一 (seed, index) 恒得同一牌山。
func wallFor(seed int64, index int, red bool) kyoku.Wall {
	r := rand.New(rand.NewPCG(uint64(seed), uint64(index)+1))
	return kyoku.ShuffledWall(r, red)
}

// deal 按当前 pos/actions 重建当前手。
func (m *Match) deal() error {
	return m.dealWithWall(wallFor(m.seed, m.handIndex, m.cfg.RedFives))
}

func (m *Match) dealWithWall(w kyoku.Wall) error {
	acts, err := m.kyokuActions()
	if err != nil {
		return err
	}
	wind, err := parseWind(m.pos.RoundWind)
	if err != nil {
		return err
	}
	scores := m.pos.Scores
	k, err := kyoku.Deal(kyoku.Setup{
		Wall:         &w,
		DealerSeat:   m.pos.DealerSeat,
		RoundWind:    wind,
		KyokuNumber:  m.pos.Kyoku,
		Scores:       &scores,
		Honba:        m.pos.Honba,
		RiichiSticks: m.pos.RiichiSticks,
		RuleSet:      m.rules,
		Actions:      acts,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadState, err)
	}
	m.cur = k
	return nil
}

// kyokuActions 把持久化动作还原为库的 Action 序列。
func (m *Match) kyokuActions() ([]kyoku.Action, error) {
	if len(m.actions) == 0 {
		return nil, nil
	}
	out := make([]kyoku.Action, 0, len(m.actions))
	for i, rec := range m.actions {
		a, err := buildAction(rec.Kind, rec.Seat, rec.Tiles, rec.Called)
		if err != nil {
			return nil, fmt.Errorf("%w: 第 %d 个记录动作 %q: %v", ErrBadState, i, rec.Kind, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// Apply 校验并应用一个动作；动作非法时返回 ErrInvalidAction。
func (m *Match) Apply(seat int, kind string, tiles []string, called string) error {
	if m.finished {
		return ErrFinished
	}
	if m.cur == nil {
		return ErrBadState
	}
	a, err := buildAction(kind, seat, tiles, called)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAction, err)
	}
	return m.applyKyoku(a)
}

// applyKyoku 应用库动作（core 测试与 engine 共用）。
func (m *Match) applyKyoku(a kyoku.Action) error {
	if m.finished {
		return ErrFinished
	}
	if m.cur == nil || m.cur.IsFinished() {
		return ErrInvalidAction
	}
	next, err := m.cur.Take(a)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAction, err)
	}
	m.cur = next
	m.actions = append(m.actions, RecordedAction{
		Kind:   a.Kind().String(),
		Seat:   a.Seat(),
		Tiles:  tile.Labels(a.Tiles()),
		Called: calledLabel(a),
	})
	if next.IsFinished() {
		m.settle()
	}
	return nil
}

func calledLabel(a kyoku.Action) string {
	if c, ok := a.CalledTile(); ok {
		return c.String()
	}
	return ""
}

// settle 结算已结束的一手：记账、推进东/局/本场/供托，必要时发下一手。
func (m *Match) settle() {
	res, ok := m.cur.Result()
	if !ok {
		return
	}
	hr := HandResult{
		RoundWind:  m.pos.RoundWind,
		Kyoku:      m.pos.Kyoku,
		Honba:      m.pos.Honba,
		DealerSeat: m.pos.DealerSeat,
		Kind:       res.Kind().String(),
		Winner:     -1,
		Loser:      -1,
		Deltas:     res.Deltas(),
		Scores:     res.Scores(),
	}
	if w, ok := res.Winner(); ok {
		hr.Winner = w
	}
	if l, ok := res.Loser(); ok {
		hr.Loser = l
	}
	if sc, ok := res.Score(); ok {
		hr.Han = sc.Han()
		hr.Fu = sc.Fu()
		hr.Yakuman = sc.YakumanCount()
		hr.Total = sc.Total()
		for _, y := range sc.Yakus() {
			hr.Yaku = append(hr.Yaku, y.Name)
		}
	}
	m.history = append(m.history, hr)
	m.pos.Scores = hr.Scores

	continues := res.DealerContinues()
	if m.matchOver(hr.Scores, continues) {
		m.finished = true
		m.winnerSeat = bestSeat(hr.Scores)
		return
	}

	honba := res.NextHonba()
	sticks := res.CarriedRiichiSticks()
	wind, num, dealer := m.pos.RoundWind, m.pos.Kyoku, m.pos.DealerSeat
	if !continues {
		dealer = (dealer + 1) % Seats
		if num < 4 {
			num++
		} else {
			wind = nextWind(wind)
			num = 1
		}
	}
	m.pos = Position{
		RoundWind:    wind,
		Kyoku:        num,
		Honba:        honba,
		RiichiSticks: sticks,
		DealerSeat:   dealer,
		Scores:       hr.Scores,
	}
	m.handIndex++
	m.actions = nil
	_ = m.deal()
}

// matchOver 判断整场是否结束：有人被击飞，或场制对应局的最后一局亲家未连庄。
func (m *Match) matchOver(scores [Seats]int, dealerContinues bool) bool {
	for _, s := range scores {
		if s < 0 {
			return true
		}
	}
	if dealerContinues {
		return false
	}
	switch m.cfg.MatchType {
	case MatchSouth:
		return m.pos.RoundWind == WindSouth && m.pos.Kyoku == 4
	default:
		return m.pos.RoundWind == WindEast && m.pos.Kyoku == 4
	}
}

// bestSeat 返回分数最高者的座位；并列时返回 -1（无唯一头名）。
func bestSeat(scores [Seats]int) int {
	best, tie := -1, false
	for s, v := range scores {
		switch {
		case best < 0 || v > scores[best]:
			best, tie = s, false
		case v == scores[best]:
			tie = true
		}
	}
	if tie {
		return -1
	}
	return best
}

func nextWind(w string) string {
	if w == WindEast {
		return WindSouth
	}
	return w
}

func parseWind(w string) (tile.Wind, error) {
	switch w {
	case WindEast, "E":
		return tile.EastWind, nil
	case WindSouth, "S":
		return tile.SouthWind, nil
	case "west", "W":
		return tile.WestWind, nil
	case "north", "N":
		return tile.NorthWind, nil
	}
	return 0, fmt.Errorf("%w: 未知场风 %q", ErrBadState, w)
}

// buildAction 把动作名 + 牌标签还原为库动作。
func buildAction(kind string, seat int, tiles []string, called string) (kyoku.Action, error) {
	if seat < 0 || seat >= Seats {
		return kyoku.Action{}, fmt.Errorf("座位越界: %d", seat)
	}
	parsed, err := tile.ParseAll(tiles)
	if err != nil {
		return kyoku.Action{}, err
	}
	var calledTile tile.Tile
	if called != "" {
		calledTile, err = tile.Parse(called)
		if err != nil {
			return kyoku.Action{}, err
		}
	}
	// 无牌动作不得携带牌参数（服务端不信任客户端多余字段）。
	switch kind {
	case "tsumo", "ron", "pass", "kyushukyuhai", "rinshan", "handoff":
		if len(parsed) != 0 {
			return kyoku.Action{}, fmt.Errorf("%s 不接受牌参数", kind)
		}
	}
	switch kind {
	case "discard":
		if len(parsed) != 1 {
			return kyoku.Action{}, fmt.Errorf("discard 需要 1 张牌")
		}
		return kyoku.NewDiscard(seat, parsed[0]), nil
	case "riichi":
		if len(parsed) != 1 {
			return kyoku.Action{}, fmt.Errorf("riichi 需要 1 张牌")
		}
		return kyoku.NewRiichi(seat, parsed[0]), nil
	case "ankan":
		if len(parsed) != 1 {
			return kyoku.Action{}, fmt.Errorf("ankan 需要 1 张牌")
		}
		return kyoku.NewAnkan(seat, parsed[0]), nil
	case "kakan":
		if len(parsed) != 1 {
			return kyoku.Action{}, fmt.Errorf("kakan 需要 1 张牌")
		}
		return kyoku.NewKakan(seat, parsed[0]), nil
	case "chi":
		if len(parsed) != 2 || !calledTile.IsValid() {
			return kyoku.Action{}, fmt.Errorf("chi 需要 2 张手牌 + called")
		}
		return kyoku.NewChi(seat, calledTile, parsed), nil
	case "pon":
		if len(parsed) != 2 || !calledTile.IsValid() {
			return kyoku.Action{}, fmt.Errorf("pon 需要 2 张手牌 + called")
		}
		return kyoku.NewPon(seat, calledTile, parsed), nil
	case "minkan":
		if !calledTile.IsValid() {
			return kyoku.Action{}, fmt.Errorf("minkan 需要 called")
		}
		return kyoku.NewMinkan(seat, calledTile), nil
	case "tsumo":
		return kyoku.NewTsumo(seat), nil
	case "ron":
		return kyoku.NewRon(seat), nil
	case "pass":
		return kyoku.NewPass(seat), nil
	case "kyushukyuhai":
		return kyoku.NewKyushukyuhai(seat), nil
	case "rinshan":
		return kyoku.NewRinshan(seat), nil
	case "handoff":
		return kyoku.NewHandoff(seat), nil
	}
	return kyoku.Action{}, fmt.Errorf("未知动作: %q", kind)
}

// Snapshot 导出持久化快照。
func (m *Match) Snapshot() Snapshot {
	s := Snapshot{
		Seed:       m.seed,
		Config:     m.cfg,
		Position:   m.pos,
		HandIndex:  m.handIndex,
		Actions:    append([]RecordedAction(nil), m.actions...),
		History:    append([]HandResult(nil), m.history...),
		Finished:   m.finished,
		WinnerSeat: m.winnerSeat,
	}
	if s.Actions == nil {
		s.Actions = []RecordedAction{}
	}
	if s.History == nil {
		s.History = []HandResult{}
	}
	return s
}

// Finished 报告整场是否结束。
func (m *Match) Finished() bool { return m.finished }

// WinnerSeat 返回头名座位；-1 表示无唯一头名（并列）。
func (m *Match) WinnerSeat() int { return m.winnerSeat }

// Position 返回当前手的起始信息。
func (m *Match) Position() Position { return m.pos }

// HandIndex 返回当前是第几手（0 起）。
func (m *Match) HandIndex() int { return m.handIndex }

// History 返回已完成各手的结算。
func (m *Match) History() []HandResult { return m.history }

// AwaitingSeats 返回当前需要行动的座位（自摸/打牌者，或待鸣牌应答者）。
func (m *Match) AwaitingSeats() []int {
	if m.cur == nil || m.finished {
		return nil
	}
	return m.cur.AwaitingSeats()
}

// HandFinished 报告当前手是否已结算。
func (m *Match) HandFinished() bool { return m.cur == nil || m.cur.IsFinished() }

// LastHandResult 返回最近一手的结算（无则 ok=false）。
func (m *Match) LastHandResult() (HandResult, bool) {
	if len(m.history) == 0 {
		return HandResult{}, false
	}
	return m.history[len(m.history)-1], true
}

// View 返回某座位视角的局面（含自己的手牌、全体牌河/副露/宝牌）。
func (m *Match) View(seat int) map[string]interface{} {
	if m.cur == nil || seat < 0 || seat >= Seats {
		return map[string]interface{}{}
	}
	out := marshalMap(m.cur.SeenBy(seat))
	m.decorateView(out, seat)
	return out
}

// SpectatorView 返回观战视角（不含任何人的手牌）。
func (m *Match) SpectatorView() map[string]interface{} {
	if m.cur == nil {
		return map[string]interface{}{}
	}
	out := marshalMap(m.cur.SeenBy(0))
	delete(out, "closed_tiles")
	delete(out, "drawn")
	delete(out, "legal_actions")
	delete(out, "seat")
	out["spectator"] = true
	m.decorateView(out, -1)
	return out
}

// decorateView 附加整场信息。
func (m *Match) decorateView(view map[string]interface{}, seat int) {
	view["match"] = map[string]interface{}{
		"matchType":    m.cfg.MatchType,
		"roundWind":    m.pos.RoundWind,
		"kyoku":        m.pos.Kyoku,
		"honba":        m.pos.Honba,
		"riichiSticks": m.pos.RiichiSticks,
		"scores":       m.pos.Scores,
		"handIndex":    m.handIndex,
		"finished":     m.finished,
	}
	view["handIndex"] = m.handIndex
	if seat >= 0 {
		view["seat"] = seat
	}
	if m.finished {
		view["winnerSeat"] = m.winnerSeat
	}
	if res, ok := m.LastHandResult(); ok && m.HandFinished() {
		view["handResult"] = res
	}
	view["history"] = m.history
}

func marshalMap(v interface{}) map[string]interface{} {
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]interface{}{}
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]interface{}{}
	}
	return out
}
