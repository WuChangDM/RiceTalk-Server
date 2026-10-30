// Package xiangqi 实现中国象棋权威规则引擎（服务端自研，不依赖第三方库）。
//
// 规则覆盖：
//   - 棋盘 9×10（列 x=0..8，行 y=0..9；y=0 为黑方底线，y=9 为红方底线，红方先行）
//   - 走法：车直线、马日字（蹩马腿）、相/象田字（塞象眼+不过河）、仕/士九宫斜一步、
//     帅/将九宫直一步、炮平移直线+隔子打、兵/卒过河前只进不横、过河后可横不可退
//   - 将帅照面：两王同列且中间无子视为非法位置关系（等价于被"将军"，走后照面即非法）
//   - 合法性：走后己方将/帅不能被将军（含照面）
//   - 胜负：一方无合法走法即负——被将军时无解为"绝杀"（checkmate），
//     未被将军时无解为"困毙"（stalemate），两者均判负（中国象棋无逼和）
//
// state 权威字段（与 chess 包同构）：
//   - board: 10 行 × 9 列棋盘（[][]*piece，type/color 两字段；断线重连/观战据此重建对局）
//   - currentPlayer / players / winner / draw / status
//   - check: 当前行棋方是否正被将军（客户端据此做红光脉冲提示）
//   - lastMove: 最近一步 {from:{x,y}, to:{x,y}}（客户端做起讫点橙框标记）
//   - endReason: 终局原因 "checkmate"（绝杀）| "stalemate"（困毙），未结束为空
//   - moveCount: 已走步数
//
// action 协议（与 chess 包同构）：
//   - 仅支持 "move_made"，payload 为 {"from":{"x":..,"y":..},"to":{"x":..,"y":..}}
package xiangqi

import (
	"encoding/json"

	"ridgericetalk/features/minigames/internal/util"
)

// 棋盘尺寸：9 列 × 10 行。
const (
	boardCols = 9
	boardRows = 10
)

// 棋子颜色。
const (
	red   = "red"
	black = "black"
)

// D4（task-65）最小和棋规则：60 回合无吃子（120 半步）。
// 长将/长捉等复杂棋规不在本轮范围（报告已列未覆盖项）。
const noCaptureHalfMoves = 120

// hasAttackingPieces 判断某方是否还有进攻子力（车/马/炮/兵）。
// 只剩将帅/士/象视为「无进攻子力」，双方都如此即判和。
func hasAttackingPieces(board [][]*piece, color string) bool {
	for y := 0; y < boardRows; y++ {
		for x := 0; x < boardCols; x++ {
			p := board[y][x]
			if p == nil || p.Color != color {
				continue
			}
			switch p.Type {
			case "rook", "knight", "cannon", "pawn":
				return true
			}
		}
	}
	return false
}

// intFromState 把 state 里的数值（float64/int/json.Number）安全转 int。
func intFromState(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// piece 棋子：type 为类型、color 为阵营。
type piece struct {
	Type  string `json:"type"`  // king advisor bishop knight rook cannon pawn
	Color string `json:"color"` // red / black
}

// pt 棋盘坐标点。
type pt struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// coord action payload 中的坐标（JSON 键 x/y）。
type coord struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Engine 实现中国象棋权威规则。
type Engine struct{}

// New 创建一个中国象棋引擎实例。
func New() *Engine {
	return &Engine{}
}

// Init 返回初始权威 state：players[0] 执红先行，players[1] 执黑。
func (e *Engine) Init(players []map[string]interface{}) (map[string]interface{}, error) {
	ids := util.PlayerUserIDs(players)
	state := make(map[string]interface{})
	state["status"] = "playing"
	state["players"] = ids
	state["currentPlayer"] = ids[0] // 红方先行
	state["winner"] = nil
	state["draw"] = false
	state["board"] = initialBoard()
	state["check"] = false
	state["lastMove"] = nil
	state["endReason"] = ""
	state["moveCount"] = 0
	return state, nil
}

// ValidateMove 校验并执行一步走子，返回更新后的权威 state。
func (e *Engine) ValidateMove(state map[string]interface{}, userID string, action string, payload json.RawMessage) (map[string]interface{}, error) {
	if action != "move_made" {
		return nil, util.ErrInvalidAction
	}
	currentPlayer, _ := state["currentPlayer"].(string)
	if currentPlayer != "" && currentPlayer != userID {
		return nil, util.ErrNotYourTurn
	}
	// 已分出胜负的对局不再接受走子。
	if w, _ := state["winner"].(string); w != "" {
		return nil, util.ErrInvalidAction
	}
	players := util.GetStringSlice(state, "players")
	moverColor := colorOfPlayer(players, userID)
	if moverColor == "" {
		return nil, util.ErrInvalidAction
	}

	var p struct {
		From coord `json:"from"`
		To   coord `json:"to"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, util.ErrInvalidAction
	}
	board := boardFromState(state)
	if !inBoard(p.From.X, p.From.Y) || !inBoard(p.To.X, p.To.Y) {
		return nil, util.ErrInvalidAction
	}
	mover := board[p.From.Y][p.From.X]
	if mover == nil || mover.Color != moverColor {
		return nil, util.ErrInvalidAction
	}
	// 走法形状合法性（车马炮兵将士象各自步法）。
	if !containsPt(pseudoMoves(board, p.From.X, p.From.Y), pt{X: p.To.X, Y: p.To.Y}) {
		return nil, util.ErrInvalidAction
	}
	// 走后己方将/帅不能被将军（含将帅照面）。
	next := applyMove(board, pt{X: p.From.X, Y: p.From.Y}, pt{X: p.To.X, Y: p.To.Y})
	if isKingInCheck(next, moverColor) {
		return nil, util.ErrInvalidAction
	}

	// 判定对手是否无棋可走：无合法走法即负（绝杀/困毙），中国象棋无逼和。
	opponentColor := oppositeColor(moverColor)
	gameOver := len(legalMovesForColor(next, opponentColor)) == 0

	winner := ""
	endReason := ""
	if gameOver {
		winner = userID
		if isKingInCheck(next, opponentColor) {
			endReason = "checkmate" // 绝杀
		} else {
			endReason = "stalemate" // 困毙
		}
	}

	// D4：和棋判定（仅在未绝杀/困毙时生效，避免把胜负误判成和棋）。
	captured := board[p.To.Y][p.To.X] != nil
	noCapture := intFromState(state["noCaptureMoves"])
	if captured {
		noCapture = 0
	} else {
		noCapture++
	}
	draw := false
	if !gameOver {
		switch {
		case noCapture >= noCaptureHalfMoves:
			draw, endReason = true, "draw_no_capture"
		case !hasAttackingPieces(next, red) && !hasAttackingPieces(next, black):
			draw, endReason = true, "draw_no_attacking_pieces"
		}
	}

	nextPlayer := ""
	if !gameOver && !draw {
		nextPlayer = util.NextPlayer(players, userID)
	}

	newState := util.ShallowCopy(state)
	newState["board"] = next
	newState["currentPlayer"] = nextPlayer
	newState["winner"] = winner
	newState["draw"] = draw
	newState["noCaptureMoves"] = noCapture
	newState["check"] = !gameOver && !draw && isKingInCheck(next, opponentColor)
	newState["lastMove"] = map[string]interface{}{
		"from": map[string]int{"x": p.From.X, "y": p.From.Y},
		"to":   map[string]int{"x": p.To.X, "y": p.To.Y},
	}
	newState["endReason"] = endReason
	if mc, ok := state["moveCount"].(float64); ok {
		newState["moveCount"] = mc + 1
	} else if mc, ok := state["moveCount"].(int); ok {
		newState["moveCount"] = mc + 1
	} else {
		newState["moveCount"] = 1
	}
	return newState, nil
}

// IsGameOver 报告对局是否已结束。
func (e *Engine) IsGameOver(state map[string]interface{}) (winnerID string, draw bool) {
	if d, ok := state["draw"].(bool); ok && d {
		return "", true
	}
	if w, ok := state["winner"].(string); ok && w != "" {
		return w, false
	}
	return "", false
}

// ===== 棋盘几何 =====

// inBoard 判断坐标是否在棋盘内。
func inBoard(x, y int) bool {
	return x >= 0 && x < boardCols && y >= 0 && y < boardRows
}

// inPalace 判断 (x,y) 是否在 color 方九宫内（3×3）。
func inPalace(x, y int, color string) bool {
	if x < 3 || x > 5 {
		return false
	}
	if color == red {
		return y >= 7 && y <= 9
	}
	return y >= 0 && y <= 2
}

// ownHalf 判断 (x,y) 是否在 color 方半场（未过河一侧）。红方下半场 y>=5，黑方上半场 y<=4。
func ownHalf(x, y int, color string) bool {
	if color == red {
		return y >= 5
	}
	return y <= 4
}

// crossedRiver 判断 (x,y) 是否已过河（相对 color 的进攻方向）。红过河 y<=4，黑过河 y>=5。
func crossedRiver(x, y int, color string) bool {
	if color == red {
		return y <= 4
	}
	return y >= 5
}

func oppositeColor(c string) string {
	if c == red {
		return black
	}
	return red
}

// colorOfPlayer 返回玩家执的颜色：players[0] 红、players[1] 黑；非对局玩家返回 ""。
func colorOfPlayer(players []string, userID string) string {
	for i, id := range players {
		if id == userID {
			if i == 0 {
				return red
			}
			return black
		}
	}
	return ""
}

// ===== 走法生成 =====

var dirs4 = [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}

var knightMoves = [][4]int{
	// {dx, dy, 腿dx, 腿dy}：马走日，先直后斜，直的那步是"马腿"
	{1, 2, 0, 1}, {-1, 2, 0, 1}, {1, -2, 0, -1}, {-1, -2, 0, -1},
	{2, 1, 1, 0}, {2, -1, 1, 0}, {-2, 1, -1, 0}, {-2, -1, -1, 0},
}

var bishopMoves = [][2]int{{2, 2}, {2, -2}, {-2, 2}, {-2, -2}}

var advisorMoves = [][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}}

// pseudoMoves 生成 (x,y) 处棋子的所有伪合法走法（不考虑走后被将军）。
func pseudoMoves(b [][]*piece, x, y int) []pt {
	p := b[y][x]
	if p == nil {
		return nil
	}
	out := make([]pt, 0, 17)
	try := func(nx, ny int) {
		if !inBoard(nx, ny) {
			return
		}
		if t := b[ny][nx]; t != nil && t.Color == p.Color {
			return // 不能吃己方棋子
		}
		out = append(out, pt{X: nx, Y: ny})
	}

	switch p.Type {
	case "king": // 帅/将：九宫内直走一步
		for _, d := range dirs4 {
			nx, ny := x+d[0], y+d[1]
			if inBoard(nx, ny) && inPalace(nx, ny, p.Color) {
				try(nx, ny)
			}
		}
	case "advisor": // 仕/士：九宫内斜走一步
		for _, d := range advisorMoves {
			nx, ny := x+d[0], y+d[1]
			if inBoard(nx, ny) && inPalace(nx, ny, p.Color) {
				try(nx, ny)
			}
		}
	case "bishop": // 相/象：田字，塞象眼不可走，不过河
		for _, d := range bishopMoves {
			nx, ny := x+d[0], y+d[1]
			if !inBoard(nx, ny) || !ownHalf(nx, ny, p.Color) {
				continue
			}
			if b[y+d[1]/2][x+d[0]/2] != nil {
				continue // 塞象眼
			}
			try(nx, ny)
		}
	case "knight": // 马：日字，蹩马腿不可走
		for _, m := range knightMoves {
			nx, ny := x+m[0], y+m[1]
			if !inBoard(nx, ny) {
				continue
			}
			if b[y+m[3]][x+m[2]] != nil {
				continue // 蹩马腿
			}
			try(nx, ny)
		}
	case "rook": // 车：直线任意步，不可越子
		for _, d := range dirs4 {
			nx, ny := x+d[0], y+d[1]
			for inBoard(nx, ny) {
				t := b[ny][nx]
				if t == nil {
					out = append(out, pt{X: nx, Y: ny})
				} else {
					if t.Color != p.Color {
						out = append(out, pt{X: nx, Y: ny})
					}
					break
				}
				nx += d[0]
				ny += d[1]
			}
		}
	case "cannon": // 炮：平移同车；吃子必须隔恰好一个"炮架"
		for _, d := range dirs4 {
			nx, ny := x+d[0], y+d[1]
			hasScreen := false
			for inBoard(nx, ny) {
				t := b[ny][nx]
				if !hasScreen {
					if t == nil {
						out = append(out, pt{X: nx, Y: ny})
					} else {
						hasScreen = true
					}
				} else if t != nil {
					if t.Color != p.Color {
						out = append(out, pt{X: nx, Y: ny}) // 隔子打
					}
					break
				}
				nx += d[0]
				ny += d[1]
			}
		}
	case "pawn": // 兵/卒：过河前只进一步；过河后可进可横，不可退
		fwd := 1 // 黑方向下（y 增大）
		if p.Color == red {
			fwd = -1 // 红方向上（y 减小）
		}
		try(x, y+fwd)
		if crossedRiver(x, y, p.Color) {
			try(x-1, y)
			try(x+1, y)
		}
	}
	return out
}

// containsPt 判断走法列表是否包含目标点。
func containsPt(list []pt, target pt) bool {
	for _, p := range list {
		if p == target {
			return true
		}
	}
	return false
}

// applyMove 返回执行走子后的新棋盘（不修改原棋盘）。
func applyMove(b [][]*piece, from, to pt) [][]*piece {
	nb := make([][]*piece, boardRows)
	for i := 0; i < boardRows; i++ {
		nb[i] = make([]*piece, boardCols)
		copy(nb[i], b[i])
	}
	nb[to.Y][to.X] = nb[from.Y][from.X]
	nb[from.Y][from.X] = nil
	return nb
}

// findKing 返回 color 方将/帅坐标；找不到返回 (-1,-1)。
func findKing(b [][]*piece, color string) (int, int) {
	for y := 0; y < boardRows; y++ {
		for x := 0; x < boardCols; x++ {
			if p := b[y][x]; p != nil && p.Type == "king" && p.Color == color {
				return x, y
			}
		}
	}
	return -1, -1
}

// isKingInCheck 判断 color 方将/帅是否被将军。
// 将帅照面（同列中间无子）视为非法位置关系，等价于被将军处理。
func isKingInCheck(b [][]*piece, color string) bool {
	kx, ky := findKing(b, color)
	if kx < 0 {
		return false
	}
	// 将帅照面：对方将/帅同列且中间无子。
	ex, ey := findKing(b, oppositeColor(color))
	if ex == kx {
		blocked := false
		step := 1
		if ey > ky {
			step = -1
		}
		// 从对方将/帅向己方将/帅方向逐格检查（两个九宫不相交，方向必然一上一下）。
		for y := ey + step; y != ky; y += step {
			if b[y][ex] != nil {
				blocked = true
				break
			}
		}
		if !blocked {
			return true
		}
	}
	// 其余攻击：任意敌方棋子的伪合法走法命中将/帅所在点。
	enemy := oppositeColor(color)
	for y := 0; y < boardRows; y++ {
		for x := 0; x < boardCols; x++ {
			if p := b[y][x]; p != nil && p.Color == enemy {
				if containsPt(pseudoMoves(b, x, y), pt{X: kx, Y: ky}) {
					return true
				}
			}
		}
	}
	return false
}

// legalMovesForColor 生成 color 方全部合法走法（走后不被将军，含照面约束）。
func legalMovesForColor(b [][]*piece, color string) [][2]pt {
	out := make([][2]pt, 0, 40)
	for y := 0; y < boardRows; y++ {
		for x := 0; x < boardCols; x++ {
			if p := b[y][x]; p == nil || p.Color != color {
				continue
			}
			for _, to := range pseudoMoves(b, x, y) {
				next := applyMove(b, pt{X: x, Y: y}, to)
				if !isKingInCheck(next, color) {
					out = append(out, [2]pt{{X: x, Y: y}, to})
				}
			}
		}
	}
	return out
}

// ===== 棋盘序列化 =====

// initialBoard 返回标准开局棋盘：行 0 = 黑方底线，行 9 = 红方底线。
func initialBoard() [][]*piece {
	back := func(color string) []*piece {
		return []*piece{
			{Type: "rook", Color: color}, {Type: "knight", Color: color}, {Type: "bishop", Color: color},
			{Type: "advisor", Color: color}, {Type: "king", Color: color}, {Type: "advisor", Color: color},
			{Type: "bishop", Color: color}, {Type: "knight", Color: color}, {Type: "rook", Color: color},
		}
	}
	b := make([][]*piece, boardRows)
	b[0] = back(black)
	b[1] = make([]*piece, boardCols)
	b[2] = make([]*piece, boardCols)
	b[2][1] = &piece{Type: "cannon", Color: black}
	b[2][7] = &piece{Type: "cannon", Color: black}
	b[3] = make([]*piece, boardCols)
	for _, x := range []int{0, 2, 4, 6, 8} {
		b[3][x] = &piece{Type: "pawn", Color: black}
	}
	b[4] = make([]*piece, boardCols)
	b[5] = make([]*piece, boardCols)
	b[6] = make([]*piece, boardCols)
	for _, x := range []int{0, 2, 4, 6, 8} {
		b[6][x] = &piece{Type: "pawn", Color: red}
	}
	b[7] = make([]*piece, boardCols)
	b[7][1] = &piece{Type: "cannon", Color: red}
	b[7][7] = &piece{Type: "cannon", Color: red}
	b[8] = make([]*piece, boardCols)
	b[9] = back(red)
	return b
}

// boardFromState 从 state 的权威 board 字段重建棋盘。
// 兼容两种形态：本进程内 Init 刚返回的 [][]*piece（测试直传），
// 以及经 JSON 序列化/反序列化后的 []interface{}（handler 每次请求从 DB 读回）。
func boardFromState(state map[string]interface{}) [][]*piece {
	raw, ok := state["board"]
	if !ok || raw == nil {
		return initialBoard()
	}
	if b, ok := raw.([][]*piece); ok {
		return b
	}
	rows, ok := raw.([]interface{})
	if !ok {
		return initialBoard()
	}
	b := make([][]*piece, 0, boardRows)
	for _, r := range rows {
		cells, _ := r.([]interface{})
		row := make([]*piece, 0, boardCols)
		for _, cell := range cells {
			row = append(row, pieceFromAny(cell))
		}
		for len(row) < boardCols {
			row = append(row, nil)
		}
		b = append(b, row)
	}
	for len(b) < boardRows {
		b = append(b, make([]*piece, boardCols))
	}
	return b
}

// pieceFromAny 把反序列化后的棋子（*piece 或 map）归一化为 *piece。
func pieceFromAny(v interface{}) *piece {
	switch p := v.(type) {
	case *piece:
		return p
	case map[string]interface{}:
		t, _ := p["type"].(string)
		c, _ := p["color"].(string)
		if t == "" || c == "" {
			return nil
		}
		return &piece{Type: t, Color: c}
	}
	return nil
}
