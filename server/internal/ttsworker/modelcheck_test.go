// Package ttsworker: modelcheck_test.go
//
// 模型预检测试（N16 核心：LFS 指针/损坏文件必须被拒绝，错误文本与
// classifyTTSError 的 "model not found" 归类族保持兼容）。
package ttsworker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lfsPointerFixture 是与真实缺陷现场一致的 134 字节 Git LFS 指针文本
//（known_issues N16：vits-melo-tts-zh_en/model.onnx 落地为指针文件）。
const lfsPointerFixture = "version https://git-lfs.github.com/spec/v1\n" +
	"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b04473b62e5e5205d5a3ce34\n" +
	"size 165228864\n"

func TestLFSPointerFixtureIs134Bytes(t *testing.T) {
	// 固化缺陷现场的体积特征，防止 fixture 漂移
	if got := len(lfsPointerFixture); got != 134 {
		t.Fatalf("LFS pointer fixture must be 134 bytes, got %d", got)
	}
}

func writeModelFile(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), content, 0o644); err != nil {
		t.Fatalf("write model.onnx: %v", err)
	}
	return dir
}

func TestValidateTTSModel_Missing(t *testing.T) {
	dir := t.TempDir()
	err := ValidateTTSModel(dir, DefaultMinModelBytes)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	// 与旧 ttsengine_cgo.go 降级文案逐字同族（classifyTTSError 兼容契约）
	if !strings.Contains(err.Error(), "TTS model not found; TTS disabled") {
		t.Fatalf("error text must keep legacy family, got: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("error should mention modelDir, got: %v", err)
	}
}

func TestValidateTTSModel_LFSPointerRejected(t *testing.T) {
	dir := writeModelFile(t, []byte(lfsPointerFixture))
	err := ValidateTTSModel(dir, DefaultMinModelBytes)
	if err == nil {
		t.Fatal("LFS pointer file must be rejected")
	}
	msg := err.Error()
	if !strings.Contains(msg, "model not found") {
		t.Fatalf("error text must contain 'model not found' for classifyTTSError compat, got: %s", msg)
	}
	if !strings.Contains(msg, "LFS") {
		t.Fatalf("error text should explain LFS pointer reason, got: %s", msg)
	}
}

func TestValidateTTSModel_TooSmallRejected(t *testing.T) {
	// 512B 的伪 onnx 头：低于注入阈值 1KB → 拒绝
	dir := writeModelFile(t, []byte(strings.Repeat("ONNX", 128)))
	if err := ValidateTTSModel(dir, 1024); err == nil {
		t.Fatal("file below minBytes must be rejected")
	} else if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("error text must keep family, got: %v", err)
	}
	// 同一文件，注入更小阈值 → 通过（阈值可注入，判定函数本身可测）
	if err := ValidateTTSModel(dir, 256); err != nil {
		t.Fatalf("same file should pass with smaller threshold, got: %v", err)
	}
}

func TestValidateTTSModel_TooSmallDefaultThreshold(t *testing.T) {
	// 512B 文件 + 阈值 0（取默认 1MB）→ 拒绝（无需生成 1MB 文件即可测默认阈值路径）
	dir := writeModelFile(t, []byte(strings.Repeat("ONNX", 128)))
	if err := ValidateTTSModel(dir, 0); err == nil {
		t.Fatal("small file must be rejected under default 1MB threshold")
	}
}

func TestValidateTTSModel_Valid(t *testing.T) {
	// 正常 onnx 头 + 略超阈值的体积（Truncate 稀疏补齐，不实际写 1MB）
	dir := writeModelFile(t, []byte("ONNX-FAKE-HEADER"))
	f, err := os.OpenFile(filepath.Join(dir, "model.onnx"), os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := f.Truncate(1024 + 64); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.Close()
	if err := ValidateTTSModel(dir, 1024); err != nil {
		t.Fatalf("valid model should pass, got: %v", err)
	}
}

func TestValidateTTSModel_LFSContentWithLargeSize(t *testing.T) {
	// 内容是指针但体积超阈值（如拼接填充过的损坏文件）：内容检测兜底拒绝
	content := lfsPointerFixture + strings.Repeat("\x00", 2048)
	dir := writeModelFile(t, []byte(content))
	if err := ValidateTTSModel(dir, 1024); err == nil {
		t.Fatal("LFS pointer content must be rejected regardless of size")
	}
}

func TestValidateTTSModel_ErrorIsNotReady(t *testing.T) {
	dir := writeModelFile(t, []byte(lfsPointerFixture))
	err := ValidateTTSModel(dir, DefaultMinModelBytes)
	var nre *NotReadyError
	if !asNotReady(err, &nre) {
		t.Fatalf("validation failure must be *NotReadyError, got %T", err)
	}
}
