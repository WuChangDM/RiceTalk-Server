// Package ttsworker: protocol.go
//
// TTS worker 子进程的 stdin/stdout 行分隔 JSON 协议（known_issues N16 修复）。
//
// 背景：sherpa-onnx 的 cgo 调用在模型损坏（如 Git LFS 指针占位文件）时会抛出
// C++ Ort::Exception，cgo 执行中 SIGABRT 不可 recover，导致整个 Go 服务端进程
// 崩溃循环。因此所有 sherpa-onnx / cgo 调用被隔离到独立 worker 进程
// （cmd/tts-worker），主进程通过本协议与之通信，worker 崩溃绝不影响主进程。
//
// 协议格式（stdin/stdout，每行一条 JSON，UTF-8，'\n' 结尾）：
//
//	合成请求（op 缺省即 synth，兼容 {"id":"...","text":"..."} 简式）:
//	  {"id":"req-1","op":"synth","text":"你好","wavPath":"/tmp/out.wav","speed":1.0,"sid":0}
//	信息请求（主进程 spawn 后握手，读取引擎元数据）:
//	  {"id":"req-2","op":"info"}
//	成功响应:
//	  {"id":"req-1","ok":true,"wavPath":"/tmp/out.wav","sampleRate":16000,"numSpeakers":1}
//	失败响应:
//	  {"id":"req-1","ok":false,"error":"sherpa-onnx: ..."}
//
// 生命周期：stdin EOF（主进程关闭管道）= worker 优雅退出（exit 0）；
// 模型加载失败 / 内部崩溃 = worker 退出非 0，由主进程管理器检测并带退避重启。
package ttsworker

import (
	"encoding/json"
	"fmt"
)

// 请求 op 常量。Op 为空字符串按 opSynth 处理（协议简式兼容）。
const (
	OpSynth = "synth"
	OpInfo  = "info"
)

// Request 是主进程 → worker 的一条请求。
type Request struct {
	ID      string  `json:"id"`                // 请求关联 id，响应原样带回
	Op      string  `json:"op,omitempty"`      // "synth"（缺省）| "info"
	Text    string  `json:"text,omitempty"`    // 待合成文本（synth）
	WavPath string  `json:"wavPath,omitempty"` // WAV 输出绝对路径（synth，由主进程指定）
	Speed   float64 `json:"speed,omitempty"`   // 语速倍率（synth，<=0 由 engine 按 1.0 处理）
	Sid     int32   `json:"sid,omitempty"`     // speaker id（synth，多说话人模型用）
}

// Response 是 worker → 主进程的一条响应。
type Response struct {
	ID          string `json:"id"`                    // 对应请求 id
	OK          bool   `json:"ok"`                    // 是否成功
	WavPath     string `json:"wavPath,omitempty"`     // 实际写出的 WAV 路径（synth 成功时）
	SampleRate  int32  `json:"sampleRate,omitempty"`  // 采样率（synth 成功 / info 成功）
	NumSpeakers int32  `json:"numSpeakers,omitempty"` // 说话人数（info 成功）
	Error       string `json:"error,omitempty"`       // 失败原因（ok=false 时）
}

// EncodeRequest 把请求编码为一行 JSON（含结尾 '\n'）。
func EncodeRequest(r Request) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("ttsworker: encode request: %w", err)
	}
	return append(b, '\n'), nil
}

// DecodeRequest 解码一行 JSON 为请求。id 为空视为非法请求。
func DecodeRequest(line []byte) (*Request, error) {
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("ttsworker: decode request: %w", err)
	}
	if r.ID == "" {
		return nil, fmt.Errorf("ttsworker: decode request: missing id")
	}
	return &r, nil
}

// EncodeResponse 把响应编码为一行 JSON（含结尾 '\n'）。
func EncodeResponse(r Response) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("ttsworker: encode response: %w", err)
	}
	return append(b, '\n'), nil
}

// DecodeResponse 解码一行 JSON 为响应。id 为空视为非法响应。
func DecodeResponse(line []byte) (*Response, error) {
	var r Response
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("ttsworker: decode response: %w", err)
	}
	if r.ID == "" {
		return nil, fmt.Errorf("ttsworker: decode response: missing id")
	}
	return &r, nil
}
