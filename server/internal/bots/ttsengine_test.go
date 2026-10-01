// Package bots: ttsengine_test.go
//
// N16 修复的兼容性测试：TTS 门面（InitTTS/SynthesizeText/ResetTTS）在
// 「模型缺失/LFS 指针」场景下的错误语义必须与旧实现同族——
// classifyTTSError（tts_job_manager.go）按字符串把这类错误归为
// BOT_TTS_NOT_CONFIGURED（retryable=false）。此契约被本测试固化。
package bots

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreerrors "ridgericetalk/core/errors"
)

// writeTTSModelFixture 在 dir 写一个给定内容的 model.onnx。
func writeTTSModelFixture(t *testing.T, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(filepath.Dir(t.TempDir())), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

func TestInitTTS_MissingModelKeepsLegacyText(t *testing.T) {
	dir := t.TempDir()
	err := InitTTS(dir)
	if err == nil {
		t.Fatal("InitTTS must fail for missing model")
	}
	// 与旧 ttsengine_cgo.go 降级文案逐字同族
	if !strings.Contains(err.Error(), "TTS model not found; TTS disabled") {
		t.Fatalf("legacy degradation text must be preserved, got: %v", err)
	}
	ready, _, errMsg := TTSStatus()
	if ready || !strings.Contains(errMsg, "model not found") {
		t.Fatalf("TTSStatus must report not-ready with family text, got (%v, %q)", ready, errMsg)
	}
	if TTSSpeakerCount() != 0 {
		t.Fatal("unavailable engine must report 0 speakers")
	}
}

func TestInitTTS_LFSPointerModelRejected(t *testing.T) {
	// N16 缺陷现场：134 字节 Git LFS 指针占位文件
	pointer := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b04473b62e5e5205d5a3ce34\n" +
		"size 165228864\n"
	dir := writeTTSModelFixture(t, pointer)
	err := InitTTS(dir)
	if err == nil {
		t.Fatal("LFS pointer model must be rejected (N16 root cause)")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("error must stay in 'model not found' family for classifyTTSError, got: %v", err)
	}
}

func TestSynthesizeText_NotConfiguredErrorCode(t *testing.T) {
	// 前置：确保引擎处于「模型缺失」不可用态（不依赖 worker 二进制）
	dir := t.TempDir()
	_ = InitTTS(dir)

	sr, err := SynthesizeText("hi", filepath.Join(t.TempDir(), "o.wav"), 1.0, 0)
	if err == nil {
		t.Fatal("SynthesizeText must fail when engine unavailable")
	}
	if sr != 0 {
		t.Fatalf("sample rate must be 0 on failure, got %d", sr)
	}
	// 旧实现行为：不可用错误以 BOT_TTS_NOT_CONFIGURED 错误码返回（HTTP 503）
	appErr, ok := err.(*coreerrors.AppError)
	if !ok {
		t.Fatalf("expected *coreerrors.AppError, got %T: %v", err, err)
	}
	if appErr.Code != coreerrors.BOT_TTS_NOT_CONFIGURED {
		t.Fatalf("code = %s, want BOT_TTS_NOT_CONFIGURED (legacy semantics)", appErr.Code)
	}
}

func TestClassifyTTSError_FamilyCompatWithWorkerErrors(t *testing.T) {
	// tts_job_manager.classifyTTSError 的字符串归类对 worker 管理器错误文本
	// 的兼容性（双保险：除错误码外，纯文本路径也必须归 NOT_CONFIGURED）。
	cases := []string{
		"TTS model not found; TTS disabled (modelDir=/x)",
		"TTS model not found or invalid (model.onnx is a Git LFS pointer placeholder, not the real binary); TTS disabled (modelDir=/x)",
		"tts worker binary not found at /x; TTS disabled, engine not initialized",
		"tts worker is not running; engine not initialized",
		"tts worker exited unexpectedly; engine not initialized",
		"tts worker restarting (backoff 200ms after 3 failed spawn attempts); engine not initialized",
		"tts worker manager closed; engine not initialized",
	}
	for _, msg := range cases {
		got := classifyTTSError("synthesize", &fakeErr{msg})
		if got.Code != TTSErrNotConfigured {
			t.Fatalf("classifyTTSError(%q) = %s, want BOT_TTS_NOT_CONFIGURED", msg, got.Code)
		}
		if got.Retryable {
			t.Fatalf("classifyTTSError(%q) must not be retryable", msg)
		}
	}
	// 合成类业务错误（worker 透传）仍应归 SYNTHESIS_FAILED / retryable
	got := classifyTTSError("synthesize", &fakeErr{"sherpa-onnx: GenerateWithConfig returned nil"})
	if got.Code != TTSErrSynthesisFailed || !got.Retryable {
		t.Fatalf("synthesis business error must stay retryable SYNTHESIS_FAILED, got %+v", got)
	}
}

// fakeErr 是固定文本的测试错误。
type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
