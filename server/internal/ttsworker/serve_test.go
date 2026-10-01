// Package ttsworker: serve_test.go
//
// worker 主循环测试（桩引擎，不经子进程）。
package ttsworker

import (
	"bytes"
	"errors"
	"log"
	"io"
	"strings"
	"testing"
)

// loopEngine 是 serve 测试用的确定性桩引擎。
type loopEngine struct {
	infoErr error
	synErr  error
}

func (e *loopEngine) Info() (int32, int32, error) {
	if e.infoErr != nil {
		return 0, 0, e.infoErr
	}
	return 16000, 1, nil
}

func (e *loopEngine) Synthesize(text, wavPath string, speed float64, sid int32) (int32, error) {
	if e.synErr != nil {
		return 0, e.synErr
	}
	return 16000, nil
}

func (e *loopEngine) Close() {}

// runServe 跑一轮 serve，返回全部响应行。
func runServe(t *testing.T, eng Engine, input string) []string {
	t.Helper()
	var out bytes.Buffer
	err := ServeEngine(eng, strings.NewReader(input), &out, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("ServeEngine: %v", err)
	}
	lines := []string{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestServeEngine_InfoAndSynth(t *testing.T) {
	input := "{\"id\":\"a\",\"op\":\"info\"}\n" +
		"{\"id\":\"b\",\"text\":\"你好\",\"wavPath\":\"/tmp/x.wav\",\"speed\":1.0}\n"
	lines := runServe(t, &loopEngine{}, input)
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses, got %d: %v", len(lines), lines)
	}
	r1, err := DecodeResponse([]byte(lines[0]))
	if err != nil || r1.ID != "a" || !r1.OK || r1.SampleRate != 16000 || r1.NumSpeakers != 1 {
		t.Fatalf("info response mismatch: %v %v", r1, err)
	}
	r2, err := DecodeResponse([]byte(lines[1]))
	if err != nil || r2.ID != "b" || !r2.OK || r2.WavPath != "/tmp/x.wav" || r2.SampleRate != 16000 {
		t.Fatalf("synth response mismatch: %v %v", r2, err)
	}
}

func TestServeEngine_SynthFailureReturnsError(t *testing.T) {
	input := "{\"id\":\"a\",\"text\":\"hi\",\"wavPath\":\"/tmp/x.wav\"}\n"
	eng := &loopEngine{synErr: errors.New("sherpa-onnx: Save failed")}
	lines := runServe(t, eng, input)
	if len(lines) != 1 {
		t.Fatalf("expected 1 response, got %d", len(lines))
	}
	r, err := DecodeResponse([]byte(lines[0]))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.OK || !strings.Contains(r.Error, "Save failed") {
		t.Fatalf("expected ok=false with engine error, got %+v", r)
	}
}

func TestServeEngine_BadRequestKeepsLoopAlive(t *testing.T) {
	// 非法行不应终止循环：坏行回 ok:false（id 为空——无 pending 可投递，
	// 管理器端按 malformed 日志丢弃），后续合法请求正常处理
	input := "not-a-json\n{\"id\":\"ok1\",\"text\":\"hi\",\"wavPath\":\"/tmp/x.wav\"}\n"
	lines := runServe(t, &loopEngine{}, input)
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses (bad + good), got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"ok":false`) {
		t.Fatalf("bad request must produce ok=false, got %s", lines[0])
	}
	r1, err := DecodeResponse([]byte(lines[1]))
	if err != nil || r1.ID != "ok1" || !r1.OK {
		t.Fatalf("loop must keep serving after bad request: %v %v", r1, err)
	}
}

func TestServeEngine_UnknownOp(t *testing.T) {
	input := "{\"id\":\"a\",\"op\":\"frobnicate\"}\n"
	lines := runServe(t, &loopEngine{}, input)
	r, err := DecodeResponse([]byte(lines[0]))
	if err != nil || r.OK || !strings.Contains(r.Error, "unknown op") {
		t.Fatalf("unknown op must fail gracefully: %v %v", r, err)
	}
}

func TestServeEngine_EmptyInputGracefulEOF(t *testing.T) {
	var out bytes.Buffer
	if err := ServeEngine(&loopEngine{}, strings.NewReader(""), &out, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("EOF must be graceful, got: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("no output expected, got %q", out.String())
	}
}

func TestServeEngine_SynthMissingWavPath(t *testing.T) {
	input := "{\"id\":\"a\",\"text\":\"hi\"}\n"
	lines := runServe(t, &loopEngine{}, input)
	r, err := DecodeResponse([]byte(lines[0]))
	if err != nil || r.OK || !strings.Contains(r.Error, "wavPath") {
		t.Fatalf("missing wavPath must fail with reason: %v %v", r, err)
	}
}
