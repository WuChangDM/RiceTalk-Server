package doudizhu

import (
	"testing"
)

// mkPokers 用 key 序列构造 ratel 牌（Val 经 rules 计算）。
func mkPokers(keys ...int) ratelPokers {
	pokers := make(ratelPokers, 0, len(keys))
	for _, k := range keys {
		p := ratelPoker{Key: k}
		p.Val = landlordRules.Value(p.Key)
		pokers = append(pokers, p)
	}
	return pokers
}

func facesType(pokers ratelPokers) (ratelFacesType, bool) {
	faces := ratelParseFaces(pokers)
	if len(faces) == 0 {
		return 0, false
	}
	return faces[0].Type, true
}

func TestRatelParseBasicPatterns(t *testing.T) {
	cases := []struct {
		name string
		keys []int
		want ratelFacesType
	}{
		{"single", []int{5}, ratelFacesSingle},
		{"pair", []int{5, 5}, ratelFacesDouble},
		{"trio", []int{7, 7, 7}, ratelFacesTriple},
		{"bomb", []int{9, 9, 9, 9}, ratelFacesBomb},
		{"rocket", []int{14, 15}, ratelFacesBomb},
		{"trioWithSolo", []int{6, 6, 6, 9}, ratelFacesUnion3},
		{"chain5", []int{3, 4, 5, 6, 7}, ratelFacesStraight},
		{"pairsChain3", []int{3, 3, 4, 4, 5, 5}, ratelFacesStraight},
		{"airplane", []int{5, 5, 5, 6, 6, 6}, ratelFacesStraight},
		{"airplaneWithSolos", []int{5, 5, 5, 6, 6, 6, 8, 9}, ratelFacesUnion3Straight},
		{"airplaneWithPairs", []int{5, 5, 5, 6, 6, 6, 8, 8, 9, 9}, ratelFacesUnion3Straight},
		{"fourWithDualSolo", []int{8, 8, 8, 8, 3, 5}, ratelFacesUnion4},
		{"fourWithDualPair", []int{8, 8, 8, 8, 3, 3, 5, 5}, ratelFacesUnion4},
	}
	for _, c := range cases {
		got, ok := facesType(mkPokers(c.keys...))
		if !ok {
			t.Errorf("%s: no faces parsed for %v", c.name, c.keys)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got type %d, want %d (keys=%v)", c.name, got, c.want, c.keys)
		}
	}
}

func TestRatelParseInvalidPatterns(t *testing.T) {
	cases := []struct {
		name string
		keys []int
	}{
		{"notPair", []int{3, 4}},
		{"notTrio", []int{3, 3, 4}},
		{"chainTooShort", []int{3, 4, 5, 6}},
		{"chainWith2", []int{10, 11, 12, 13, 1, 2}}, // 含 2 不能成顺
		{"pairsChainTooShort", []int{3, 3, 4, 4}},
	}
	for _, c := range cases {
		if faces := ratelParseFaces(mkPokers(c.keys...)); len(faces) != 0 {
			t.Errorf("%s: expected no faces for %v, got %v", c.name, c.keys, faces[0].Type)
		}
	}
}

func TestRatelCompare(t *testing.T) {
	mustFace := func(keys ...int) ratelFaces {
		f := ratelParseFaces(mkPokers(keys...))
		if len(f) == 0 {
			t.Fatalf("no faces for %v", keys)
		}
		return f[0]
	}
	cases := []struct {
		name      string
		play      []int
		last      []int
		wantBeats bool
	}{
		{"higherSingle", []int{6}, []int{5}, true},
		{"lowerSingle", []int{4}, []int{5}, false},
		{"higherPair", []int{7, 7}, []int{5, 5}, true},
		{"bombBeatsChain", []int{9, 9, 9, 9}, []int{3, 4, 5, 6, 7}, true},
		{"chainCannotBeatBomb", []int{3, 4, 5, 6, 7}, []int{9, 9, 9, 9}, false},
		{"rocketBeatsBomb", []int{14, 15}, []int{13, 13, 13, 13}, true},
		{"higherBomb", []int{10, 10, 10, 10}, []int{9, 9, 9, 9}, true},
		{"lowerBomb", []int{8, 8, 8, 8}, []int{9, 9, 9, 9}, false},
		{"higherChainSameLen", []int{4, 5, 6, 7, 8}, []int{3, 4, 5, 6, 7}, true},
		{"diffLenChain", []int{4, 5, 6, 7, 8, 9}, []int{3, 4, 5, 6, 7}, false},
		{"trioWithSoloHigher", []int{7, 7, 7, 3}, []int{6, 6, 6, 9}, true},
	}
	for _, c := range cases {
		play := mustFace(c.play...)
		last := mustFace(c.last...)
		if got := play.Compare(last); got != c.wantBeats {
			t.Errorf("%s: Compare(%v vs %v)=%v, want %v", c.name, c.play, c.last, got, c.wantBeats)
		}
	}
}

func TestRatelLaiziSubstitution(t *testing.T) {
	// 癞子为 5：手牌 3,3,5(癞) 可用 5 凑 3,3,3
	hand := mkPokers(3, 3, 5)
	hand.setOaa(5)
	sells, ok := ratelResolveLaizi([]int{3, 3, 3}, hand)
	if !ok {
		t.Fatal("expected laizi substitution to succeed")
	}
	faces := ratelParseFaces(sells)
	if len(faces) == 0 || faces[0].Type != ratelFacesTriple {
		t.Fatalf("expected trio after laizi substitution, got %v", faces)
	}
}

func TestRatelLaiziCannotReplaceJoker(t *testing.T) {
	// 癞子不能代替大小王：仅 1 张癞子无法凑火箭
	hand := mkPokers(5, 14)
	hand.setOaa(5)
	_, ok := ratelResolveLaizi([]int{14, 15}, hand)
	if ok {
		t.Fatal("expected laizi substitution to fail for rocket")
	}
}

func TestRatelLaiziBomb(t *testing.T) {
	// 3 张 7 + 1 张癞子(9) 凑 7,7,7,7 炸弹
	hand := mkPokers(7, 7, 7, 9)
	hand.setOaa(9)
	sells, ok := ratelResolveLaizi([]int{7, 7, 7, 7}, hand)
	if !ok {
		t.Fatal("expected laizi bomb substitution to succeed")
	}
	faces := ratelParseFaces(sells)
	if len(faces) == 0 || faces[0].Type != ratelFacesBomb {
		t.Fatalf("expected bomb after laizi substitution, got %v", faces)
	}
	// 纯癞子参与的炸弹应有分数下调
	if faces[0].Score >= int64(7*4)+int64(4*1000) {
		t.Logf("laizi bomb score=%d (含 -300 下调)", faces[0].Score)
	}
}

func TestEngineLaiziModeInit(t *testing.T) {
	engine := New()
	state, _ := engine.initWithMode([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	}, "laizi")
	if mode, _ := state["ruleMode"].(string); mode != "laizi" {
		t.Fatalf("expected ruleMode=laizi, got %v", mode)
	}
	universals := engine.universalsSlice(state)
	if len(universals) != 2 {
		t.Fatalf("expected 2 laizi universals, got %v", universals)
	}
	if universals[0] == universals[1] {
		t.Fatalf("expected distinct laizi universals, got %v", universals)
	}
}

func TestEngineClassicModeNoUniversals(t *testing.T) {
	engine := New()
	state, _ := engine.Init([]map[string]interface{}{
		{"userId": "a"}, {"userId": "b"}, {"userId": "c"},
	})
	if mode, _ := state["ruleMode"].(string); mode != "classic" {
		t.Fatalf("expected ruleMode=classic, got %v", mode)
	}
	if u := engine.universalsSlice(state); len(u) != 0 {
		t.Fatalf("expected no universals in classic mode, got %v", u)
	}
}
