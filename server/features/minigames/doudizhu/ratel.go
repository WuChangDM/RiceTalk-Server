// Package doudizhu 斗地主规则（移植自 ratel-online，MIT License）。
// 本文件移植 ratel-online/core/model/poker.go 的牌与牌型模型、比较逻辑，
// 以及 ratel-online/core/util/poker 的牌型识别核心（ParseFaces）。
// 仅提取斗地主所需的规则纯函数，去除对 ratel database/state/skill/bot 的依赖。
//
// 牌模型：Key 1=A,2..13=K,14=小王,15=大王；Val 为比较用的点数权重。
// Faces 为识别出的牌型，含 Type/Score/Main/Extra，用于合法性校验与大小比较。
package doudizhu

import (
	"math/rand"
	"sort"
	"time"
)

// ===== FacesType 牌型常量（移植自 ratel consts，保留斗地主相关项）=====
type ratelFacesType int

const (
	ratelFacesBomb           ratelFacesType = 1 // 炸弹（含火箭）
	ratelFacesSingle         ratelFacesType = 2 // 单牌
	ratelFacesDouble         ratelFacesType = 3 // 对子
	ratelFacesTriple         ratelFacesType = 4 // 三张
	ratelFacesUnion3         ratelFacesType = 5 // 三带一
	ratelFacesUnion4         ratelFacesType = 6 // 四带二
	ratelFacesStraight       ratelFacesType = 7 // 顺子/连对/飞机（裸连）
	ratelFacesUnion3Straight ratelFacesType = 8 // 飞机带翅膀
)

// ===== 牌模型（移植自 ratel model.Poker）=====
type ratelPoker struct {
	Key  int  `json:"key"`  // 1=A ... 13=K, 14=小王, 15=大王
	Val  int  `json:"val"`  // 比较点数权重
	Desc string `json:"desc"` // 显示名
	Oaa  bool `json:"oaa"`  // 是否癞子牌
}

type ratelPokers []ratelPoker

func (pokers ratelPokers) setOaa(oaa ...int) {
	for i := range pokers {
		if ratelContains(oaa, pokers[i].Key) {
			pokers[i].Oaa = true
		}
	}
}

func (pokers ratelPokers) sortByValue() {
	sort.Slice(pokers, func(i, j int) bool { return pokers[i].Val < pokers[j].Val })
}

// ===== Faces 牌型（移植自 ratel model.Faces）=====
type ratelFaces struct {
	Keys   []int
	Values []int
	Score  int64
	Type   ratelFacesType
	Main   int
	Extra  int
	HasOaa bool
}

// Compare 报告 f 是否能压过 lastFaces（移植自 ratel model.Faces.Compare）。
func (f ratelFaces) Compare(lastFaces ratelFaces) bool {
	if f.Type == ratelFacesBomb {
		return f.Score > lastFaces.Score
	}
	if f.Type != lastFaces.Type {
		return false
	}
	return f.Score > lastFaces.Score && f.Main == lastFaces.Main && f.Extra == lastFaces.Extra
}

// ===== LandlordRules 斗地主规则（移植自 ratel rule/rule.go）=====
type ratelRules struct{}

func (r ratelRules) Value(key int) int {
	if key == 1 {
		return 12 // A
	} else if key == 2 {
		return 13 // 2
	} else if key > 13 {
		return key // 14/15 王
	}
	return key - 2 // 3..K -> 1..11
}

func (r ratelRules) IsStraight(faces []int, count int) bool {
	if faces[len(faces)-1]-faces[0] != len(faces)-1 {
		return false
	}
	if faces[len(faces)-1] > 12 {
		return false
	}
	if count == 1 {
		return len(faces) >= 5
	} else if count == 2 {
		return len(faces) >= 3
	} else if count > 2 {
		return len(faces) >= 2
	}
	return false
}

func (r ratelRules) StraightBoundary() (int, int) {
	return 1, 12
}

// ===== 牌型识别（移植自 ratel util/poker/poker.go ParseFaces）=====
var landlordRules = ratelRules{}

// ratelParseFaces 识别一组牌的所有合法牌型（通常 0 或 1 个）。
func ratelParseFaces(pokers ratelPokers) []ratelFaces {
	rules := landlordRules
	hasOaa := false
	for _, p := range pokers {
		if p.Oaa {
			hasOaa = true
			break
		}
	}
	list := ratelParseFacesCore(pokers, rules)
	if len(list) == 0 {
		return list
	}
	mapping := map[int]int{}
	for i := 1; i <= 15; i++ {
		mapping[rules.Value(i)] = i
	}
	for i, faces := range list {
		keys := make([]int, 0)
		for _, v := range faces.Values {
			keys = append(keys, mapping[v])
		}
		list[i].Keys = keys
		list[i].HasOaa = hasOaa
		if hasOaa && faces.Type == ratelFacesBomb {
			// 癞子炸弹分数略降（ratel 约定）
			list[i].Score -= 300
		}
	}
	return list
}

func ratelParseFacesCore(pokers ratelPokers, rules ratelRules) []ratelFaces {
	if len(pokers) == 0 {
		return nil
	}
	sc, xc, score := 0, 0, int64(0)
	stats := map[int]int{}
	group := map[int][]int{}
	counts := make([]int, 0)
	values := make([]int, 0)
	for _, poker := range pokers {
		if poker.Key < 0 || poker.Key > 15 {
			return nil
		}
		poker.Val = rules.Value(poker.Key)
		score += int64(poker.Val)
		values = append(values, poker.Val)
		stats[poker.Val]++
		if poker.Key == 14 {
			sc++
		} else if poker.Key == 15 {
			xc++
		}
	}
	for v, c := range stats {
		group[c] = append(group[c], v)
	}
	for c := range group {
		counts = append(counts, c)
		sort.Ints(group[c])
	}
	sort.Ints(counts)
	for i := 0; i < len(counts)/2; i++ {
		counts[i], counts[len(counts)-i-1] = counts[len(counts)-i-1], counts[i]
	}
	list := make([]ratelFaces, 0)
	if sc+xc == len(pokers) && sc+xc > 1 {
		// 火箭（双王）
		list = append(list, ratelFaces{Values: values, Score: int64(sc*14+xc*15)*2 + int64(len(pokers)*2*1000), Type: ratelFacesBomb})
	} else if counts[0] == 1 {
		if len(group[counts[0]]) == 1 {
			list = append(list, ratelFaces{Values: values, Score: score, Type: ratelFacesSingle})
		} else if rules.IsStraight(group[counts[0]], counts[0]) {
			list = append(list, ratelFaces{Values: values, Score: score, Main: len(group[counts[0]]), Type: ratelFacesStraight})
		}
	} else if counts[0] == 2 && len(counts) == 1 {
		if len(group[counts[0]]) == 1 {
			list = append(list, ratelFaces{Values: values, Score: score, Type: ratelFacesDouble})
		} else if rules.IsStraight(group[counts[0]], counts[0]) {
			list = append(list, ratelFaces{Values: values, Score: score, Main: len(group[counts[0]]), Type: ratelFacesStraight})
		}
	} else if counts[0] >= 3 {
		if len(counts) == 1 && len(group[counts[0]]) == 1 {
			if counts[0] == 3 {
				list = append(list, ratelFaces{Values: values, Score: score, Type: ratelFacesTriple})
			} else {
				list = append(list, ratelFaces{Values: values, Score: int64(group[counts[0]][0]*counts[0]) + int64(len(pokers)*1000), Type: ratelFacesBomb})
			}
		} else if len(counts) == 1 && rules.IsStraight(group[counts[0]], counts[0]) {
			if counts[0] == 3 {
				list = append(list, ratelFaces{Values: values, Score: score, Main: len(group[counts[0]]), Type: ratelFacesStraight})
			} else if counts[0] == 4 {
				values = make([]int, 0)
				for _, v := range group[counts[0]] {
					values = ratelAppendN(values, v, 3)
				}
				for _, v := range group[counts[0]] {
					values = append(values, v)
				}
				list = append(list, ratelFaces{Values: values, Score: score / 4 * 3, Main: len(group[counts[0]]), Extra: 1, Type: ratelFacesUnion3Straight})
			} else if counts[0] == 5 {
				values = make([]int, 0)
				for _, v := range group[counts[0]] {
					values = ratelAppendN(values, v, 3)
				}
				for _, v := range group[counts[0]] {
					values = ratelAppendN(values, v, 2)
				}
				list = append(list, ratelFaces{Values: values, Score: score / 5 * 3, Main: len(group[counts[0]]), Extra: 1, Type: ratelFacesUnion3Straight})
			}
		} else if len(group[3]) > 0 {
			list = ratelParseUnionOrStraight(group, rules)
		} else if counts[0] == 4 && len(counts) == 2 && counts[1] <= 2 {
			if len(group[counts[0]]) == 1 && ((counts[1] == 2 && len(group[counts[1]]) <= 2) || (counts[1] == 1 && len(group[counts[1]]) == 2)) {
				extra := counts[1]
				if extra == 2 && len(group[extra]) == 1 {
					extra = 1
				}
				list = append(list, ratelFaces{Values: values, Score: int64(group[counts[0]][0] * counts[0]), Main: len(group[counts[0]]), Extra: extra, Type: ratelFacesUnion4})
			}
		}
		if counts[0] == 4 && len(counts) == 1 && len(group[counts[0]]) == 2 {
			values = make([]int, 0)
			values = ratelAppendN(values, group[counts[0]][1], counts[0])
			values = ratelAppendN(values, group[counts[0]][0], counts[0])
			list = append(list, ratelFaces{Values: values, Score: int64(group[counts[0]][1] * counts[0]), Main: 1, Extra: 1, Type: ratelFacesUnion4})
		}
	}
	return list
}

func ratelParseUnionOrStraight(group map[int][]int, rules ratelRules) []ratelFaces {
	list := make([]ratelFaces, 0)
	extras := make([]int, 0)
	mains := make([]int, 0)
	for k, arr := range group {
		for _, v := range arr {
			if k > 3 {
				extras = ratelAppendN(extras, v, k-3)
				mains = append(mains, v)
			} else if k == 3 {
				mains = append(mains, v)
			} else if k < 3 {
				extras = ratelAppendN(extras, v, k)
			}
		}
	}
	sort.Ints(mains)
	valid := map[int]int{}
	sta, pre := mains[0], mains[0]
	for i := 1; i < len(mains); i++ {
		if mains[i] > pre+1 {
			valid[sta] = pre
			sta = mains[i]
		}
		pre = mains[i]
	}
	valid[sta] = mains[len(mains)-1]

	target := 0
	for k, v := range valid {
		if target == 0 {
			target = k
			continue
		}
		if v-k > valid[target]-target || (v-k == valid[target]-target && k > target) {
			target = k
		}
	}

	for _, v := range mains {
		if v < target || v > valid[target] {
			extras = ratelAppendN(extras, v, 3)
		}
	}

	ml, mr := target, valid[target]
	ll, lr := rules.StraightBoundary()
	main := mr - ml + 1
	for extra := 1; extra <= 2; extra++ {
		tmpExtras := make([]int, len(extras))
		copy(tmpExtras, extras)
		access, vl, vr, tmpExtras := ratelIsValidUnionStraight(main, extra, tmpExtras, ml, mr, ll, lr)
		if access {
			values := make([]int, 0)
			score := 0
			main = vr - vl + 1
			for i := vl; i <= vr; i++ {
				values = ratelAppendN(values, i, 3)
				score += 3 * i
			}
			sort.Ints(tmpExtras)
			for _, v := range tmpExtras {
				values = append(values, v)
			}
			faces := ratelFaces{Main: main, Extra: extra, Score: int64(score), Values: values}
			if main == 1 {
				faces.Type = ratelFacesUnion3
			} else {
				faces.Type = ratelFacesUnion3Straight
			}
			list = append(list, faces)
		}
	}
	return list
}

func ratelIsValidUnionStraight(main, extra int, extras []int, ml, mr, ll, lr int) (bool, int, int, []int) {
	access := false
	if extra == 2 {
		counts := map[int]int{}
		for _, v := range extras {
			counts[v]++
		}
		single := map[int]bool{}
		for k, v := range counts {
			if v%2 == 1 {
				single[k] = true
			}
		}
		for len(single) > 0 {
			if single[mr] {
				extras = ratelAppendN(extras, mr, 3)
				delete(single, mr)
				mr--
			} else if single[ml] {
				extras = ratelAppendN(extras, ml, 3)
				delete(single, ml)
				ml++
			} else {
				return false, ml, mr, extras
			}
		}
		if len(extras)%2 != 0 {
			return false, ml, mr, extras
		}
		main = mr - ml + 1
		access = main == len(extras)/2
	} else {
		for main > len(extras) && (main-1-len(extras))%3 == 0 {
			if mr > lr {
				extras = ratelAppendN(extras, mr, 3)
				mr--
			} else {
				extras = ratelAppendN(extras, ml, 3)
				ml++
			}
			main = mr - ml + 1
		}
		access = main == len(extras)
	}
	if main > 1 {
		access = access && ml >= ll && mr <= lr
	}
	return access, ml, mr, extras
}

// ===== 癞子替换（移植自 ratel state/game/game.go playing 段）=====

// ratelResolveLaizi 尝试用癞子牌凑出指定 key 序列的出牌。
// selKeys 为玩家意图出的牌的 key 序列（含癞子牌自身 key）。
// hand 为当前手牌。返回替换后的出牌（癞子已变为目标点数）与是否合法。
// 规则（ratel）：手牌中无该点数的普通牌时，可用一张癞子代替（癞子不能代替大小王）。
func ratelResolveLaizi(selKeys []int, hand ratelPokers) (ratelPokers, bool) {
	normalPokers := map[int]ratelPokers{}
	universalPokers := make(ratelPokers, 0)
	for _, v := range hand {
		if v.Oaa {
			universalPokers = append(universalPokers, v)
		} else {
			normalPokers[v.Key] = append(normalPokers[v.Key], v)
		}
	}
	sells := make(ratelPokers, 0)
	for _, key := range selKeys {
		if len(normalPokers[key]) == 0 {
			// 没有该点数普通牌，尝试用癞子代替（不能代替大小王）
			if key == 14 || key == 15 || len(universalPokers) == 0 {
				return nil, false
			}
			u := universalPokers[0]
			u.Key = key
			u.Val = landlordRules.Value(key)
			u.Oaa = true // 保持癞子标记，便于识别
			sells = append(sells, u)
			universalPokers = universalPokers[1:]
		} else {
			normal := normalPokers[key][len(normalPokers[key])-1]
			sells = append(sells, normal)
			normalPokers[key] = normalPokers[key][:len(normalPokers[key])-1]
		}
	}
	return sells, true
}

// ratelRandomLaizi 随机选取 2 个不同的非王牌点作为癞子点（ratel InitGame 做法）。
func ratelRandomLaizi() []int {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	first := r.Intn(13) + 1 // 1..13
	var second int
	for {
		second = r.Intn(13) + 1
		if second != first {
			break
		}
	}
	return []int{first, second}
}

// ===== 辅助函数（移植自 ratel util/arrays）=====
func ratelAppendN(arr []int, num, n int) []int {
	for i := 0; i < n; i++ {
		arr = append(arr, num)
	}
	return arr
}

func ratelContains(arr []int, target int) bool {
	for _, v := range arr {
		if v == target {
			return true
		}
	}
	return false
}
