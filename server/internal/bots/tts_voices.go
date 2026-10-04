package bots

import (
	"log"
	"path/filepath"
	"strconv"
	"strings"
)

// N8（审查-2026-0930）：TTS 音色真实化。
//
// 设计原则：UI 只展示引擎真实提供的音色，不虚标。
// - 音色清单来自引擎运行时探测（sherpa-onnx OfflineTts.NumSpeakers()，读取
//   ONNX 模型元数据），无任何硬编码音色名清单。
// - 当前打包模型 vits-melo-tts-zh_en 为单说话人（模型 README 明确：仅一个
//   中文女声），voices 恒返回 1 项；未来换多说话人模型时无需改代码——
//   数量、voice→sid 映射自动跟随引擎。
// - sherpa-onnx 不暴露 speaker 名称元数据，多说话人时以「音色 N」命名，
//   不虚构 Azure 风格名称。
//
// 客户端契约：GET /api/v1/bots/tts/voices（见 handler.GetTTSVoices）。

// TTSVoice 引擎真实提供的一个音色。ID 为 sherpa speaker id 的字符串形式。
type TTSVoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TTSVoicesInfo TTS 音色能力发现响应体。
type TTSVoicesInfo struct {
	Available   bool       `json:"available"`   // 引擎是否就绪（模型缺失时 false）
	Engine      string     `json:"engine"`      // 引擎标识，恒为 "sherpa-onnx"
	Model       string     `json:"model"`       // 模型目录名（如 vits-melo-tts-zh_en）
	NumSpeakers int        `json:"numSpeakers"` // 引擎报告的说话人数
	Voices      []TTSVoice `json:"voices"`      // 音色清单（引擎不可用时为空数组）
}

// ttsSpeakerCountFn / ttsStatusFn：包级函数变量，供测试注入单/多说话人桩
//（cgo 构建下无法在单测中构造真实 sherpa 引擎实例）。
var (
	ttsSpeakerCountFn = TTSSpeakerCount
	ttsStatusFn       = TTSStatus
)

// buildTTSVoiceList 由真实说话人数生成音色清单（纯函数）。
// n<=0（引擎不可用/未初始化）返回空切片；n>=1 返回 n 项。
func buildTTSVoiceList(n int) []TTSVoice {
	if n <= 0 {
		return []TTSVoice{}
	}
	voices := make([]TTSVoice, 0, n)
	for i := 0; i < n; i++ {
		name := "音色 " + strconv.Itoa(i+1)
		if n == 1 {
			// 单说话人模型（当前 MeloTTS zh_en）：唯一音色即引擎默认音色
			name = "默认音色"
		}
		voices = append(voices, TTSVoice{ID: strconv.Itoa(i), Name: name})
	}
	return voices
}

// resolveVoiceToSid 把客户端 voice 参数映射到引擎 speaker id（纯函数）。
//
// - 引擎单说话人（或不可用）：恒返回 0，voice 参数被忽略（调用方负责记日志）。
// - 引擎多说话人：voice 为十进制 speaker id 字符串；空值/非数字/越界一律
//   回退 0（引擎默认音色），不报错——voice 是可选参数，非法值不应阻断合成。
func resolveVoiceToSid(voice string, numSpeakers int) int32 {
	if numSpeakers <= 1 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(voice))
	if err != nil || n < 0 || n >= numSpeakers {
		return 0
	}
	return int32(n)
}

// GetTTSVoices 返回当前引擎真实音色能力（供 handler 组装响应）。
// 数据全部来自引擎运行时探测；引擎未初始化/模型缺失时 Available=false。
func (s *Service) GetTTSVoices() TTSVoicesInfo {
	ready, modelDir, _ := ttsStatusFn()
	numSpeakers := int(ttsSpeakerCountFn())
	info := TTSVoicesInfo{
		Available:   ready,
		Engine:      "sherpa-onnx",
		Model:       filepath.Base(modelDir),
		NumSpeakers: numSpeakers,
		Voices:      buildTTSVoiceList(numSpeakers),
	}
	if !ready {
		// 引擎不可用时说话人数无意义，强制为 0 与空清单，避免半真半假
		info.NumSpeakers = 0
		info.Voices = []TTSVoice{}
	}
	return info
}

// logIgnoredVoice 单说话人引擎收到非空 voice 参数时记录一条明确日志
//（N8 要求：不支持时在日志中如实说明，而非静默丢弃）。
func logIgnoredVoice(voice string, jobID string) {
	log.Printf("[tts] voice parameter %q ignored for job %s: current engine exposes a single voice (MeloTTS)", voice, jobID)
}
