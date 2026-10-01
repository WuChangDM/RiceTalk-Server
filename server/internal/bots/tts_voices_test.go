package bots

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
)

// withTTSStubs 临时替换 TTS 引擎探测桩（ttsSpeakerCountFn/ttsStatusFn），
// 测试结束自动恢复。cgo 构建下无法在单测中构造真实 sherpa 引擎实例，
// 因此通过桩模拟单/多说话人两种引擎形态。
func withTTSStubs(t *testing.T, ready bool, numSpeakers int32, fn func()) {
	t.Helper()
	origCount, origStatus := ttsSpeakerCountFn, ttsStatusFn
	t.Cleanup(func() {
		ttsSpeakerCountFn, ttsStatusFn = origCount, origStatus
	})
	ttsSpeakerCountFn = func() int32 { return numSpeakers }
	ttsStatusFn = func() (bool, string, string) {
		if ready {
			return true, "/models/tts/vits-melo-tts-zh_en", ""
		}
		return false, "", "TTS model not found; TTS disabled"
	}
	fn()
}

func TestBuildTTSVoiceList(t *testing.T) {
	// 引擎不可用/未初始化：空清单
	if got := buildTTSVoiceList(0); len(got) != 0 {
		t.Fatalf("n=0: expected empty list, got %v", got)
	}
	if got := buildTTSVoiceList(-1); len(got) != 0 {
		t.Fatalf("n=-1: expected empty list, got %v", got)
	}

	// 单说话人（当前 MeloTTS zh_en）：唯一「默认音色」，id 固定 0
	one := buildTTSVoiceList(1)
	if len(one) != 1 {
		t.Fatalf("n=1: expected 1 voice, got %d", len(one))
	}
	if one[0].ID != "0" || one[0].Name != "默认音色" {
		t.Fatalf("n=1: unexpected voice %+v", one[0])
	}

	// 多说话人：逐个「音色 N」，id 为 0..n-1
	four := buildTTSVoiceList(4)
	if len(four) != 4 {
		t.Fatalf("n=4: expected 4 voices, got %d", len(four))
	}
	for i, v := range four {
		wantID := strconv.Itoa(i)
		if v.ID != wantID {
			t.Fatalf("voice[%d].ID = %q, want %q", i, v.ID, wantID)
		}
		if v.Name == "" {
			t.Fatalf("voice[%d].Name empty", i)
		}
	}
}

func TestResolveVoiceToSid(t *testing.T) {
	cases := []struct {
		voice       string
		numSpeakers int
		want        int32
	}{
		// 单说话人引擎：一律忽略 voice，恒 0
		{"", 1, 0},
		{"zh-CN-XiaoxiaoNeural", 1, 0},
		{"2", 1, 0},
		// 引擎不可用（0）：同样恒 0，不 panic
		{"1", 0, 0},
		// 多说话人引擎：合法 id 映射
		{"", 4, 0},
		{"0", 4, 0},
		{"3", 4, 3},
		{" 2 ", 4, 2},
		// 多说话人引擎：非法/越界回退默认 0（voice 为可选参数，不应阻断合成）
		{"zh-CN-XiaoxiaoNeural", 4, 0},
		{"-1", 4, 0},
		{"4", 4, 0},
		{"99", 4, 0},
		{"abc", 4, 0},
	}
	for _, tc := range cases {
		if got := resolveVoiceToSid(tc.voice, tc.numSpeakers); got != tc.want {
			t.Errorf("resolveVoiceToSid(%q, %d) = %d, want %d", tc.voice, tc.numSpeakers, got, tc.want)
		}
	}
}

func TestServiceGetTTSVoices(t *testing.T) {
	// 单说话人引擎（当前真实形态）
	withTTSStubs(t, true, 1, func() {
		s := &Service{}
		info := s.GetTTSVoices()
		if !info.Available || info.NumSpeakers != 1 || len(info.Voices) != 1 {
			t.Fatalf("single-speaker: unexpected info %+v", info)
		}
		if info.Model != "vits-melo-tts-zh_en" || info.Engine != "sherpa-onnx" {
			t.Fatalf("single-speaker: unexpected model/engine %q/%q", info.Model, info.Engine)
		}
	})

	// 多说话人引擎（未来换模型形态）
	withTTSStubs(t, true, 4, func() {
		s := &Service{}
		info := s.GetTTSVoices()
		if info.NumSpeakers != 4 || len(info.Voices) != 4 {
			t.Fatalf("multi-speaker: unexpected info %+v", info)
		}
		if info.Voices[3].ID != "3" {
			t.Fatalf("multi-speaker: voices[3].ID = %q, want 3", info.Voices[3].ID)
		}
	})

	// 引擎不可用：available=false、空清单、numSpeakers 归零
	withTTSStubs(t, false, 0, func() {
		s := &Service{}
		info := s.GetTTSVoices()
		if info.Available || info.NumSpeakers != 0 || len(info.Voices) != 0 {
			t.Fatalf("unavailable: unexpected info %+v", info)
		}
	})
}

func TestGetTTSVoicesHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 多说话人桩：验证响应体契约（voices 数组、numSpeakers、available）
	withTTSStubs(t, true, 3, func() {
		h := &Handler{service: &Service{}}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/api/v1/bots/tts/voices", nil)
		h.GetTTSVoices(c)

		if w.Code != 200 {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Code string `json:"code"`
			Data struct {
				Available   bool       `json:"available"`
				Engine      string     `json:"engine"`
				NumSpeakers int        `json:"numSpeakers"`
				Voices      []TTSVoice `json:"voices"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v; body=%s", err, w.Body.String())
		}
		if resp.Code != "OK" || !resp.Data.Available || resp.Data.NumSpeakers != 3 || len(resp.Data.Voices) != 3 {
			t.Fatalf("unexpected response %+v", resp)
		}
	})

	// 不可用桩：available=false、空数组（JSON 应为 [] 而非 null）
	withTTSStubs(t, false, 0, func() {
		h := &Handler{service: &Service{}}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/api/v1/bots/tts/voices", nil)
		h.GetTTSVoices(c)

		body := w.Body.String()
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, body)
		}
		if !containsVoicesEmptyArray(body) {
			t.Fatalf("expected voices to serialize as [], got: %s", body)
		}
	})
}

// containsVoicesEmptyArray 粗查响应中 voices 字段序列化为空数组。
func containsVoicesEmptyArray(body string) bool {
	var resp struct {
		Data struct {
			Voices []TTSVoice `json:"voices"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return false
	}
	return resp.Data.Voices != nil && len(resp.Data.Voices) == 0
}
