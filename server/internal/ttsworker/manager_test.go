// Package ttsworker: manager_test.go
//
// worker 管理器子进程级测试（known_issues N16 修复验证）：
//   - 正常请求-响应（spawn + info 握手 + synth）
//   - 并发请求 id 配对
//   - worker 崩溃 → pending 失败（错误文本族兼容）→ 带退避自动重启
//   - 请求超时 → 降级 + 杀 worker 自愈
//   - spawn 失败 → 退避窗口内拒绝
//   - 模型预检在 spawn 之前拦截（LFS 指针 → 不启动 worker）
//   - 二进制缺失 → NotReadyError（优雅降级，绝不崩主进程）
//   - Close 后拒绝请求
//
// 假 worker 由 TestMain 用 go 工具链从 testdata/fakeenginew 一次性编译，
// 纯 Go 无 cgo，任何平台可跑。
package ttsworker

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeWorkerBin 是 TestMain 构建出的假 worker 可执行文件路径。
var fakeWorkerBin string

func fakeWorkerName() string {
	if runtime.GOOS == "windows" {
		return "fake-tts-worker.exe"
	}
	return "fake-tts-worker"
}

func TestMain(m *testing.M) {
	code := func() int {
		tmp, err := os.MkdirTemp("", "ttsworker-fake-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "mktemp: %v\n", err)
			return 1
		}
		defer os.RemoveAll(tmp)

		goBin := filepath.Join(runtime.GOROOT(), "bin", "go"+exeSuffix())
		if _, err := os.Stat(goBin); err != nil {
			goBin, err = exec.LookPath("go")
			if err != nil {
				fmt.Fprintln(os.Stderr, "cannot locate go toolchain for fake worker build")
				return 1
			}
		}
		bin := filepath.Join(tmp, fakeWorkerName())
		cmd := exec.Command(goBin, "build", "-o", bin, ".")
		cmd.Dir = "testdata" + string(os.PathSeparator) + "fakeenginew"
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build fake worker: %v\n%s", err, out)
			return 1
		}
		fakeWorkerBin = bin
		return m.Run()
	}()
	os.Exit(code)
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// testConfig 返回加速版管理器配置（短超时/短退避，快测）。
func testConfig() Config {
	return Config{
		WorkerBin:      fakeWorkerBin,
		RequestTimeout: 2 * time.Second,
		ReadyTimeout:   5 * time.Second,
		BackoffBase:    50 * time.Millisecond,
		BackoffMax:     200 * time.Millisecond,
		Logger:         log.New(io.Discard, "", 0),
		Stderr:         io.Discard,
	}
}

// makeValidModelDir 生成可通过预检的模型目录（Truncate 稀疏补齐，不真写 1MB）。
func makeValidModelDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "model.onnx"))
	if err != nil {
		t.Fatalf("create model.onnx: %v", err)
	}
	if _, err := f.WriteString("ONNX-FAKE-HEADER-FOR-WORKER-TEST"); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := f.Truncate(DefaultMinModelBytes + 64); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return dir
}

// makeLFSModelDir 生成 134B LFS 指针模型目录（N16 缺陷现场复刻）。
func makeLFSModelDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	content := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b04473b62e5e5205d5a3ce34\n" +
		"size 165228864\n"
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte(content), 0o644); err != nil {
		t.Fatalf("write pointer: %v", err)
	}
	return dir
}

// asNotReady 是 errors.As(*NotReadyError) 的测试 helper。
func asNotReady(err error, target **NotReadyError) bool {
	return errors.As(err, target)
}

// waitFor 在 timeout 内轮询条件，未满足则 Fatal。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, what)
}

func TestManager_EchoLifecycle(t *testing.T) {
	m := NewManager(testConfig())
	defer m.Close()
	dir := makeValidModelDir(t)

	if err := m.Initialize(dir); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := m.SpeakerCount(); got != 1 {
		t.Fatalf("SpeakerCount = %d, want 1", got)
	}
	if got := m.SampleRate(); got != 16000 {
		t.Fatalf("SampleRate = %d, want 16000", got)
	}
	ready, model, errStr := m.Status()
	if !ready || model != dir || errStr != "" {
		t.Fatalf("Status = (%v, %q, %q), want ready", ready, model, errStr)
	}

	wav := filepath.Join(t.TempDir(), "out.wav")
	sr, err := m.Synthesize("你好", wav, 1.0, 0)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if sr != 16000 {
		t.Fatalf("synth sample rate = %d, want 16000", sr)
	}
	data, err := os.ReadFile(wav)
	if err != nil || string(data) != "你好" {
		t.Fatalf("worker must write synthesized file (content=text), got %q err=%v", data, err)
	}

	// 幂等：重复 Initialize 不重建
	if err := m.Initialize(dir); err != nil {
		t.Fatalf("re-Initialize must be idempotent: %v", err)
	}
}

func TestManager_ConcurrentRequestIDPairing(t *testing.T) {
	m := NewManager(testConfig())
	defer m.Close()
	dir := makeValidModelDir(t)
	if err := m.Initialize(dir); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	const n = 20
	tmp := t.TempDir()
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			text := fmt.Sprintf("msg-%02d", i)
			wav := filepath.Join(tmp, fmt.Sprintf("out-%02d.wav", i))
			if _, err := m.Synthesize(text, wav, 1.0, 0); err != nil {
				errs <- err
				return
			}
			data, rerr := os.ReadFile(wav)
			if rerr != nil {
				errs <- rerr
				return
			}
			if string(data) != text {
				errs <- fmt.Errorf("response pairing broken: sent %q got %q", text, data)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent request %d failed: %v", i, err)
		}
	}
}

func TestManager_CrashDetectionAndAutoRestart(t *testing.T) {
	cfg := testConfig()
	m := NewManager(cfg)
	defer m.Close()

	t.Setenv("FAKE_WORKER_MODE", "crash-on-2")
	dir := makeValidModelDir(t)
	if err := m.Initialize(dir); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	tmp := t.TempDir()
	// 第 1 次合成：正常
	if _, err := m.Synthesize("first", filepath.Join(tmp, "1.wav"), 1.0, 0); err != nil {
		t.Fatalf("first synthesize: %v", err)
	}
	// 第 2 次合成：worker 在处理时崩溃（cgo SIGABRT 模拟）→ 在途请求确定性
	// 失败（主进程绝不 abort），错误文本族兼容 classifyTTSError。
	_, err := m.Synthesize("second", filepath.Join(tmp, "2.wav"), 1.0, 0)
	if err == nil {
		t.Fatal("second synthesize must fail after crash")
	}
	if !strings.Contains(err.Error(), "engine not initialized") {
		t.Fatalf("crash error text must keep 'engine not initialized' family, got: %v", err)
	}
	var nre *NotReadyError
	if !errors.As(err, &nre) {
		t.Fatalf("crash error must be *NotReadyError, got %T: %v", err, err)
	}
	// 崩溃触发后台带退避自动重启（backoff 50ms 起），引擎恢复就绪
	waitFor(t, 5*time.Second, "worker auto-restart ready", func() bool {
		ready, _, _ := m.Status()
		return ready
	})
	// 重启后（假 worker 计数随新进程重置）合成恢复正常
	if _, err := m.Synthesize("third", filepath.Join(tmp, "3.wav"), 1.0, 0); err != nil {
		t.Fatalf("synthesize after auto-restart: %v", err)
	}
}

func TestManager_PersistentCrashPendingFailsAndBackoff(t *testing.T) {
	cfg := testConfig()
	m := NewManager(cfg)
	defer m.Close()

	t.Setenv("FAKE_WORKER_MODE", "crash-always")
	dir := makeValidModelDir(t)
	if err := m.Initialize(dir); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	tmp := t.TempDir()
	// 持续崩溃：在途请求失败（procErr → NotReadyError）→ 同步拉起重试 →
	// 新进程同样崩 → 最终失败，重试有界（仅一次）。错误文本族必须兼容
	// classifyTTSError（归入 BOT_TTS_NOT_CONFIGURED）。
	_, err := m.Synthesize("x", filepath.Join(tmp, "o.wav"), 1.0, 0)
	if err == nil {
		t.Fatal("synthesize must fail under persistent crash")
	}
	if !strings.Contains(err.Error(), "engine not initialized") {
		t.Fatalf("persistent-crash error text must keep family, got: %v", err)
	}
	var nre *NotReadyError
	if !errors.As(err, &nre) {
		t.Fatalf("error must be *NotReadyError, got %T: %v", err, err)
	}
	// 紧接着的请求：已处于 spawn 失败退避窗口 → 直接拒绝，不再撞崩溃循环
	_, err = m.Synthesize("y", filepath.Join(tmp, "p.wav"), 1.0, 0)
	if err == nil {
		t.Fatal("request during backoff window must fail")
	}
	if !strings.Contains(err.Error(), "backoff") {
		t.Fatalf("backoff rejection should be visible in error text, got: %v", err)
	}
}

func TestManager_RequestTimeoutDegradesAndSelfHeals(t *testing.T) {
	cfg := testConfig()
	m := NewManager(cfg)
	defer m.Close()

	t.Setenv("FAKE_WORKER_MODE", "sleep")
	dir := makeValidModelDir(t)
	if err := m.Initialize(dir); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	tmp := t.TempDir()
	start := time.Now()
	_, err := m.Synthesize("slow", filepath.Join(tmp, "x.wav"), 1.0, 0)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("synthesize must time out")
	}
	if elapsed > 4*time.Second {
		t.Fatalf("timeout took too long: %s (want ~2s request timeout)", elapsed)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("timeout error text unexpected: %v", err)
	}
	// 超时已杀 worker：自动重启应让引擎回到就绪态（自愈）
	waitFor(t, 5*time.Second, "worker self-heal ready after timeout kill", func() bool {
		ready, _, _ := m.Status()
		return ready
	})
}

func TestManager_SpawnBackoffWindow(t *testing.T) {
	m := NewManager(testConfig())
	defer m.Close()

	t.Setenv("FAKE_WORKER_MODE", "exit-immediately")
	dir := makeValidModelDir(t)
	// Initialize：spawn 即死 → NotReadyError
	err := m.Initialize(dir)
	if err == nil {
		t.Fatal("Initialize must fail when worker exits immediately")
	}
	if !strings.Contains(err.Error(), "engine not initialized") {
		t.Fatalf("spawn failure text must keep family, got: %v", err)
	}
	// 紧接着的请求：慢路径 ensureRunning 应落在退避窗口内被拒绝
	_, err = m.Synthesize("x", filepath.Join(t.TempDir(), "o.wav"), 1.0, 0)
	if err == nil {
		t.Fatal("synthesize must fail while worker keeps dying")
	}
	if !strings.Contains(err.Error(), "engine not initialized") {
		t.Fatalf("error text must keep family, got: %v", err)
	}
}

func TestManager_LFSPointerModelNeverSpawnsWorker(t *testing.T) {
	m := NewManager(testConfig())
	defer m.Close()
	dir := makeLFSModelDir(t)
	err := m.Initialize(dir)
	if err == nil {
		t.Fatal("LFS pointer model must be rejected before spawn")
	}
	msg := err.Error()
	if !strings.Contains(msg, "model not found") || !strings.Contains(msg, "LFS") {
		t.Fatalf("error text must keep 'model not found' family + explain LFS, got: %s", msg)
	}
	// 未 spawn 任何进程；Synthesize 走同一不可用语义
	_, serr := m.Synthesize("x", filepath.Join(t.TempDir(), "o.wav"), 1.0, 0)
	if serr == nil || !strings.Contains(serr.Error(), "model not found") {
		t.Fatalf("synthesize with invalid model must return same family, got: %v", serr)
	}
}

func TestManager_WorkerBinMissing(t *testing.T) {
	cfg := testConfig()
	cfg.WorkerBin = filepath.Join(t.TempDir(), "definitely-not-exist")
	m := NewManager(cfg)
	defer m.Close()
	dir := makeValidModelDir(t)
	err := m.Initialize(dir)
	if err == nil {
		t.Fatal("missing worker binary must fail Initialize")
	}
	if !strings.Contains(err.Error(), "engine not initialized") {
		t.Fatalf("binary-missing error text must keep family, got: %v", err)
	}
}

func TestManager_CloseRejectsRequests(t *testing.T) {
	m := NewManager(testConfig())
	dir := makeValidModelDir(t)
	if err := m.Initialize(dir); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	m.Close()
	m.Close() // 幂等
	_, err := m.Synthesize("x", filepath.Join(t.TempDir(), "o.wav"), 1.0, 0)
	if err == nil || !strings.Contains(err.Error(), "engine not initialized") {
		t.Fatalf("closed manager must reject with family text, got: %v", err)
	}
	ready, _, _ := m.Status()
	if ready {
		t.Fatal("closed manager must report not ready")
	}
}

func TestManager_BackoffDurationPure(t *testing.T) {
	cfg := Config{BackoffBase: time.Second, BackoffMax: 60 * time.Second}
	cases := []struct {
		fails int
		want  time.Duration
	}{
		{0, time.Second},
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{7, 60 * time.Second},  // 封顶
		{20, 60 * time.Second}, // 封顶
	}
	for _, c := range cases {
		if got := backoffDurationFor(c.fails, cfg); got != c.want {
			t.Fatalf("backoffDurationFor(%d) = %s, want %s", c.fails, got, c.want)
		}
	}
}
