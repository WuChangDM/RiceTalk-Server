// Package ttsworker: protocol_test.go
//
// 协议编解码纯函数测试。
package ttsworker

import (
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	req := Request{ID: "req-1", Op: OpSynth, Text: "你好，世界", WavPath: "/tmp/out.wav", Speed: 1.25, Sid: 3}
	line, err := EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !strings.HasSuffix(string(line), "\n") {
		t.Fatalf("encoded request must end with newline, got %q", line)
	}
	got, err := DecodeRequest(line[:len(line)-1])
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got.ID != req.ID || got.Op != req.Op || got.Text != req.Text ||
		got.WavPath != req.WavPath || got.Speed != req.Speed || got.Sid != req.Sid {
		t.Fatalf("round trip mismatch: %+v vs %+v", got, req)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	resp := Response{ID: "req-2", OK: true, WavPath: "/tmp/a.wav", SampleRate: 16000, NumSpeakers: 1}
	line, err := EncodeResponse(resp)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	got, err := DecodeResponse(line[:len(line)-1])
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got.ID != resp.ID || got.OK != resp.OK || got.WavPath != resp.WavPath ||
		got.SampleRate != resp.SampleRate || got.NumSpeakers != resp.NumSpeakers {
		t.Fatalf("round trip mismatch: %+v vs %+v", got, resp)
	}
}

func TestDecodeRequest_BadJSON(t *testing.T) {
	if _, err := DecodeRequest([]byte("{not json")); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestDecodeRequest_MissingID(t *testing.T) {
	if _, err := DecodeRequest([]byte(`{"text":"hi"}`)); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestDecodeResponse_BadJSON(t *testing.T) {
	if _, err := DecodeResponse([]byte(`nope`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestDecodeResponse_MissingID(t *testing.T) {
	if _, err := DecodeResponse([]byte(`{"ok":true}`)); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestEncodeRequest_SynthShorthand(t *testing.T) {
	// 协议简式兼容：{"id":"...","text":"..."}（op 缺省即 synth）
	line, err := EncodeRequest(Request{ID: "r", Text: "hi"})
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !strings.Contains(string(line), `"id":"r"`) || !strings.Contains(string(line), `"text":"hi"`) {
		t.Fatalf("unexpected encoding: %s", line)
	}
}
