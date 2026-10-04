// Package ttsworker: manager.go
//
// TTS worker 子进程管理器（known_issues N16 修复）。
//
// 核心保证：无论 worker 崩溃、请求超时、返回错误，主进程绝不 abort——所有
// 异常对上层表现为 TTS 不可用（NotReadyError，归类 BOT_TTS_NOT_CONFIGURED）
// 或可重试的合成失败，与旧实现「engine not initialized」同语义。
//
// 职责：
//   - 模型预检（ValidateTTSModel，spawn 之前拦截 LFS 指针/损坏文件）
//   - worker 二进制发现（RRT_TTS_WORKER_BIN 环境变量优先，其次主二进制同目录）
//   - spawn worker（--model-dir argv 传入）+ info 握手确认引擎就绪
//   - 按请求 id 关联响应（pending map + 每 id 独立 chan）
//   - 崩溃检测（waitLoop）与带指数退避的自动重启（1s 起，封顶 60s）
//   - 单请求超时（默认 30s），超时杀 worker 触发自愈重启
//
// 生命周期（文字版）：
//
//	主进程                                worker 子进程
//	──────                                ─────────────
//	Initialize(modelDir)
//	  ├─ 预检模型（缺失/指针/过小 → TTS 禁用，不 spawn）
//	  ├─ spawn(tts-worker --model-dir X) ──→ 加载 sherpa 模型
//	  │    （加载失败 → worker 退出非 0 ←── stderr 日志）
//	  ├─ 发 {"op":"info"} ────────────────→ 回 sampleRate/numSpeakers
//	  └─ ready（available=true）
//	Synthesize(text, wavPath)
//	  ├─ 进程活着？死 → 退避内/预检失败则报 NotReadyError，否则同步重启
//	  ├─ 写请求行，等待按 id 配对的响应行（30s 超时）
//	  │                                     合成 WAV → 写 wavPath → 回 ok
//	  ├─ 超时 → 杀 worker + 报可重试错误 ──→ （被杀退出）
//	  └─ waitLoop 检测到意外退出 → 失败全部 pending 请求 + 后台退避重启
//	Close() / 服务关停
//	  └─ 关 stdin（EOF）─────────────────→ 优雅退出 exit 0
package ttsworker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// EnvWorkerBin 是指定 worker 二进制路径的环境变量（优先于同目录自动发现）。
const EnvWorkerBin = "RRT_TTS_WORKER_BIN"

// NotReadyError 表示 TTS worker 当前不可用（模型缺失/无效、二进制缺失、
// spawn 失败、崩溃重启中、已关闭）。
//
// 错误文本保证包含 "model not found" 或 "engine not initialized" 家族子串，
// 与 tts_job_manager.classifyTTSError 的字符串归类兼容（归类为
// BOT_TTS_NOT_CONFIGURED，retryable=false）。
type NotReadyError struct {
	Reason string
	// canRetryAfterRespawn 为 true 时表示失败源于「进程不在」，同步拉起后
	// 值得重试一次；模型/二进制类环境问题为 false，重试无意义。
	canRetryAfterRespawn bool
}

// Error 实现 error 接口。
func (e *NotReadyError) Error() string { return e.Reason }

// Config 是管理器配置；零值字段使用生产默认。
type Config struct {
	// WorkerBin 是 worker 可执行文件路径；为空时按 FindWorkerBin 规则懒发现。
	WorkerBin string
	// RequestTimeout 是单次合成请求超时。默认 30s。
	RequestTimeout time.Duration
	// ReadyTimeout 是 spawn + info 握手的超时（含模型加载）。默认 90s
	//（真实模型约 160MB，冷加载在低配 VPS 上可能需要数十秒）。
	ReadyTimeout time.Duration
	// BackoffBase / BackoffMax 是自动重启的指数退避区间。默认 1s / 60s。
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// Logger 记录管理器内部事件；为空时用标准 log。
	Logger *log.Logger
	// Stderr 是 worker stderr 的直通目标；为空时直通 os.Stderr（生产环境下
	// worker 的 sherpa/C++ 日志与主进程日志同落 journal，便于排查）。
	Stderr io.Writer
}

// callResult 是 pending 请求的一次投递：要么带响应，要么带进程死亡信号。
type callResult struct {
	resp    *Response
	procErr error // 非 nil 表示 worker 进程退出，本请求未得到响应
}

// Manager 管理单个 TTS worker 子进程的全生命周期。
type Manager struct {
	cfg     Config
	logger  *log.Logger
	stderrW io.Writer
	spawnMu sync.Mutex // 串行化 spawn/restart 路径

	mu          sync.Mutex
	closed      bool
	quit        chan struct{} // Close 时关闭，用于打断退避 sleep
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	procAlive   bool
	killing     bool   // 主动杀进程标志（onExit 据此区分意外崩溃）
	spawnAt     time.Time // 当前进程的启动时刻（成熟度判定）
	pending     map[string]chan callResult
	reqSeq      uint64 // 请求 id 序列（atomic）
	modelDir    string
	available   bool
	initErr     error
	sampleRate  int32
	numSpeakers int32

	spawnFails    int       // 连续 spawn/握手失败次数（退避依据）
	lastSpawnFail time.Time // 最近一次 spawn 失败时间
}

// NewManager 创建管理器。模型初始化通过 Initialize 显式进行。
func NewManager(cfg Config) *Manager {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 90 * time.Second
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 1 * time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 60 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	return &Manager{
		cfg:     cfg,
		logger:  cfg.Logger,
		stderrW: &syncWriter{w: cfg.Stderr},
		quit:    make(chan struct{}),
		pending: make(map[string]chan callResult),
	}
}

// syncWriter 是并发安全的 io.Writer 包装（worker stderr 直通用）。
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Write 实现 io.Writer。
func (b *syncWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.w.Write(p)
}

// FindWorkerBin 按「环境变量 → 主二进制同目录」顺序发现 worker 二进制。
// 找不到返回 *NotReadyError（保持 TTS 优雅降级语义）。
func FindWorkerBin() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvWorkerBin)); v != "" {
		if _, err := os.Stat(v); err != nil {
			return "", &NotReadyError{Reason: fmt.Sprintf(
				"tts worker binary not found at %s (%s=%s); TTS disabled, engine not initialized", v, EnvWorkerBin, v)}
		}
		return v, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", &NotReadyError{Reason: fmt.Sprintf(
			"tts worker binary not found (cannot resolve executable dir: %v); TTS disabled, engine not initialized", err)}
	}
	name := "tts-worker"
	if runtime.GOOS == "windows" {
		name = "tts-worker.exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(p); err != nil {
		return "", &NotReadyError{Reason: fmt.Sprintf(
			"tts worker binary not found at %s; TTS disabled, engine not initialized", p)}
	}
	return p, nil
}

// Initialize 校验模型并同步拉起 worker（spawn + info 握手）。
// 幂等：worker 已就绪时无操作。任何失败把管理器置为不可用（NotReadyError）。
// 该方法可能阻塞至多 ReadyTimeout（模型加载耗时），调用方应在后台 goroutine
// 中执行启动期初始化（与旧 InitTTS 的使用方式一致）。
func (m *Manager) Initialize(modelDir string) error {
	// 预检在拿 spawnMu 之前做快速失败（预检是纯文件检查，无需串行）。
	if err := ValidateTTSModel(modelDir, DefaultMinModelBytes); err != nil {
		m.mu.Lock()
		m.available = false
		m.modelDir = modelDir
		m.initErr = err
		m.mu.Unlock()
		m.logger.Printf("[tts] model precheck failed: %v", err)
		return err
	}

	m.spawnMu.Lock()
	defer m.spawnMu.Unlock()

	m.mu.Lock()
	if m.closed {
		err := &NotReadyError{Reason: "tts worker manager closed; engine not initialized"}
		m.initErr = err
		m.mu.Unlock()
		return err
	}
	if m.procAlive && m.available {
		m.mu.Unlock()
		return nil // 已就绪，幂等返回
	}
	m.mu.Unlock()

	// Reset 场景（换模型目录）：先停掉旧 worker。
	m.stopWorker()

	// 显式 Initialize 清零退避计数（管理动作不受 backoff 约束）。
	m.mu.Lock()
	m.spawnFails = 0
	m.mu.Unlock()

	return m.trySpawnLocked(modelDir)
}

// Synthesize 合成文本到 wavPath（主进程指定路径，供后续 ffmpeg 消费），
// 返回引擎采样率。worker 不可用时返回 *NotReadyError。
func (m *Manager) Synthesize(text, wavPath string, speed float64, sid int32) (int32, error) {
	resp, err := m.call(OpSynth, Request{
		Text:    text,
		WavPath: wavPath,
		Speed:   speed,
		Sid:     sid,
	})
	if err != nil {
		return 0, err
	}
	return resp.SampleRate, nil
}

// SpeakerCount 返回就绪引擎的说话人数；不可用返回 0。
func (m *Manager) SpeakerCount() int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return 0
	}
	return m.numSpeakers
}

// SampleRate 返回就绪引擎的采样率；不可用返回 0。
func (m *Manager) SampleRate() int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return 0
	}
	return m.sampleRate
}

// Status 返回 (ready, modelDir, errText)，与旧 TTSStatus 语义一致。
func (m *Manager) Status() (bool, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.available {
		return true, m.modelDir, ""
	}
	if m.initErr != nil {
		return false, m.modelDir, m.initErr.Error()
	}
	return false, m.modelDir, "not initialized"
}

// Reset 销毁当前 worker 并用 modelDir 重新初始化（管理后台重试初始化 API 用）。
// 同步返回首个错误。
func (m *Manager) Reset(modelDir string) error {
	return m.Initialize(modelDir)
}

// Close 优雅关停：关闭 quit、杀掉 worker（waitLoop 收尾）。
// 幂等；Close 后所有请求返回 NotReadyError。
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.quit)
	m.mu.Unlock()

	m.stopWorker()
	m.logger.Printf("[tts] worker manager closed")
}

// ---- 内部实现 ----

// call 发送一条请求并等待按 id 配对的响应。
func (m *Manager) call(op string, req Request) (*Response, error) {
	// 快路径：进程活着直接发。
	resp, err := m.callOnce(op, req)
	if err == nil {
		return resp, nil
	}
	// 慢路径：仅当「进程不在」时同步拉起后重试一次。
	var nre *NotReadyError
	if !errors.As(err, &nre) || !nre.canRetryAfterRespawn {
		return nil, err
	}
	if err := m.ensureRunning(); err != nil {
		return nil, err
	}
	return m.callOnce(op, req)
}

// callOnce 单次「注册 pending → 写 stdin → 等响应/超时」。
func (m *Manager) callOnce(op string, req Request) (*Response, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, &NotReadyError{Reason: "tts worker manager closed; engine not initialized"}
	}
	if !m.procAlive {
		m.mu.Unlock()
		return nil, &NotReadyError{
			Reason:               "tts worker is not running; engine not initialized",
			canRetryAfterRespawn: true,
		}
	}
	id := fmt.Sprintf("req-%d", atomic.AddUint64(&m.reqSeq, 1))
	req.ID = id
	req.Op = op
	ch := make(chan callResult, 1)
	m.pending[id] = ch
	line, encErr := EncodeRequest(req)
	if encErr != nil {
		delete(m.pending, id)
		m.mu.Unlock()
		return nil, fmt.Errorf("tts worker: encode request: %w", encErr)
	}
	if _, werr := m.stdin.Write(line); werr != nil {
		delete(m.pending, id)
		proc := m.cmd
		m.mu.Unlock()
		// 管道写失败说明进程已死：主动收尾并报 NotReadyError。
		if proc != nil {
			_ = proc.Process.Kill()
		}
		return nil, &NotReadyError{
			Reason:               fmt.Sprintf("tts worker pipe write failed (%v); engine not initialized", werr),
			canRetryAfterRespawn: true,
		}
	}
	m.mu.Unlock()

	timeout := m.cfg.RequestTimeout
	if op == OpInfo {
		timeout = m.cfg.ReadyTimeout
	}
	select {
	case r := <-ch:
		if r.procErr != nil {
			return nil, &NotReadyError{
				Reason:               "tts worker exited unexpectedly; engine not initialized",
				canRetryAfterRespawn: true,
			}
		}
		if !r.resp.OK {
			return nil, fmt.Errorf("tts worker: %s", r.resp.Error)
		}
		return r.resp, nil
	case <-time.After(timeout):
		m.mu.Lock()
		delete(m.pending, id)
		proc := m.cmd
		m.mu.Unlock()
		// 超时即认定 worker 卡死：杀掉触发 waitLoop → 带退避自动重启自愈。
		// 半截 WAV 由调用方删除与既有 24h TTL 清理机制兜底。
		if proc != nil {
			m.logger.Printf("[tts] request %s timeout after %s, killing worker for recovery", id, timeout)
			_ = proc.Process.Kill()
		}
		return nil, fmt.Errorf("tts worker request timeout after %s; synthesis aborted, worker restarting", timeout)
	}
}

// ensureRunning 同步拉起 worker（供请求慢路径兜底）。
// 退避窗口内直接拒绝；模型预检失败拒绝。
func (m *Manager) ensureRunning() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return &NotReadyError{Reason: "tts worker manager closed; engine not initialized"}
	}
	if m.procAlive {
		m.mu.Unlock()
		return nil
	}
	dir := m.modelDir
	fails := m.spawnFails
	lastFail := m.lastSpawnFail
	m.mu.Unlock()

	if dir == "" {
		return &NotReadyError{Reason: "tts worker has no model directory; engine not initialized, call InitTTS first"}
	}
	if err := ValidateTTSModel(dir, DefaultMinModelBytes); err != nil {
		return err
	}

	m.spawnMu.Lock()
	defer m.spawnMu.Unlock()

	// 双重检查：可能已被自动重启拉起。
	m.mu.Lock()
	if m.procAlive {
		m.mu.Unlock()
		return nil
	}
	if wait := backoffRemaining(fails, lastFail, m.cfg); wait > 0 {
		m.mu.Unlock()
		return &NotReadyError{
			Reason:               fmt.Sprintf("tts worker restarting (backoff %s after %d failed spawn attempts); engine not initialized", wait.Round(time.Millisecond), fails),
			canRetryAfterRespawn: true,
		}
	}
	m.mu.Unlock()

	return m.trySpawnLocked(dir)
}

// trySpawnLocked spawn worker 并做 info 握手（调用方持有 spawnMu）。
// 成功置 available=true 并清零退避计数；失败更新 initErr 并累计退避。
func (m *Manager) trySpawnLocked(modelDir string) error {
	bin := m.cfg.WorkerBin
	if bin == "" {
		var err error
		if bin, err = FindWorkerBin(); err != nil {
			m.markSpawnFailed(modelDir, err)
			return err
		}
	}

	cmd := exec.Command(bin, "--model-dir", modelDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		err = &NotReadyError{Reason: fmt.Sprintf(
			"tts worker stdin pipe failed (%v); engine not initialized", err)}
		m.markSpawnFailed(modelDir, err)
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		err = &NotReadyError{Reason: fmt.Sprintf(
			"tts worker stdout pipe failed (%v); engine not initialized", err)}
		m.markSpawnFailed(modelDir, err)
		return err
	}
	// worker stderr 直通主进程日志容器（sherpa/C++ 侧日志对排查不可替代）。
	cmd.Stderr = m.stderrW

	if err := cmd.Start(); err != nil {
		err = &NotReadyError{Reason: fmt.Sprintf(
			"tts worker failed to start (%v); TTS disabled, engine not initialized", err)}
		m.markSpawnFailed(modelDir, err)
		return err
	}

	m.mu.Lock()
	m.cmd = cmd
	m.stdin = stdin
	m.procAlive = true
	m.killing = false
	m.spawnAt = time.Now()
	m.modelDir = modelDir
	m.pending = make(map[string]chan callResult)
	m.mu.Unlock()

	waitDone := make(chan error, 1)
	go m.readerLoop(stdout)
	go func() {
		werr := cmd.Wait()
		_ = stdin.Close() // 避免 fd 泄漏
		waitDone <- werr
		m.onExit(cmd, werr)
	}()

	// info 握手：确认模型加载成功、引擎可服务。
	resp, err := m.callOnce(OpInfo, Request{})
	if err != nil {
		m.logger.Printf("[tts] worker handshake failed: %v", err)
		_ = cmd.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
		}
		if isNotReady(err) {
			m.markSpawnFailed(modelDir, err)
			return err
		}
		werr := &NotReadyError{Reason: fmt.Sprintf(
			"tts worker failed to become ready: %v; engine not initialized", err)}
		m.markSpawnFailed(modelDir, werr)
		return werr
	}

	m.mu.Lock()
	m.available = true
	m.sampleRate = resp.SampleRate
	m.numSpeakers = resp.NumSpeakers
	m.initErr = nil
	m.spawnFails = 0
	m.lastSpawnFail = time.Time{}
	m.mu.Unlock()
	m.logger.Printf("[tts] worker ready: sampleRate=%d numSpeakers=%d modelDir=%s bin=%s",
		resp.SampleRate, resp.NumSpeakers, modelDir, bin)
	return nil
}

// readerLoop 逐行读 worker stdout，按 id 投递到 pending。
func (m *Manager) readerLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				if resp, derr := DecodeResponse([]byte(trimmed)); derr == nil {
					m.deliver(resp.ID, callResult{resp: resp})
				} else {
					m.logger.Printf("[tts] worker sent malformed response: %v (%.120s)", derr, trimmed)
				}
			}
		}
		if err != nil {
			return // 管道关闭 / 进程退出，waitLoop 负责收尾
		}
	}
}

// deliver 非阻塞投递一次结果；chan 已满/不存在（超时已删）则丢弃。
func (m *Manager) deliver(id string, res callResult) {
	m.mu.Lock()
	ch, ok := m.pending[id]
	m.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- res:
	default:
	}
}

// onExit 在 worker 进程退出后被 waitLoop goroutine 调用。
func (m *Manager) onExit(cmd *exec.Cmd, waitErr error) {
	m.mu.Lock()
	if m.cmd != cmd {
		// 旧实例的退出事件（已被 Reset/stopWorker 换代），忽略。
		m.mu.Unlock()
		return
	}
	wasKilling := m.killing
	m.killing = false
	m.cmd = nil
	m.stdin = nil
	m.procAlive = false
	m.available = false
	pending := m.pending
	m.pending = make(map[string]chan callResult)
	closed := m.closed
	dir := m.modelDir
	if waitErr != nil && !wasKilling {
		m.initErr = &NotReadyError{Reason: fmt.Sprintf(
			"tts worker exited unexpectedly (%v); engine not initialized", waitErr)}
		// 意外退出纳入退避计数（与 spawn/握手失败同一退避通道）：
		// 进程未「成熟」（存活不足 matureUptime）就死 → 视为连续失败，指数退避
		// 防止崩溃风暴；成熟后偶发崩溃 → 计数清零，autoRestart 立即快速自愈。
		if uptime := time.Since(m.spawnAt); uptime < matureUptime {
			m.spawnFails++
			m.lastSpawnFail = time.Now()
		} else {
			m.spawnFails = 0
		}
	}
	m.mu.Unlock()

	// 所有在途请求立即失败（进程死亡信号）。
	for _, ch := range pending {
		select {
		case ch <- callResult{procErr: errWorkerDied}:
		default:
		}
	}

	if closed || dir == "" || wasKilling {
		return // 主动关停/未初始化：不自动重启
	}
	m.logger.Printf("[tts] worker exited unexpectedly (err=%v), scheduling auto-restart", waitErr)
	go m.autoRestartLoop(dir)
}

// matureUptime 是「成熟进程」的最短存活时间：成熟后的意外偶发崩溃不累计
// 退避（立即快速自愈）；短命进程的连续崩溃才进入指数退避。
const matureUptime = 60 * time.Second

// errWorkerDied 是进程死亡时投递给 pending 的哨兵错误。
var errWorkerDied = errors.New("worker process died")

// errBackoffBusy 表示 spawn 退避窗口未过（autoRestart 内部信号）。
var errBackoffBusy = errors.New("spawn backoff in progress")

// isNotReady 判断 err 是否为 *NotReadyError。
func isNotReady(err error) bool {
	var nre *NotReadyError
	return errors.As(err, &nre)
}

// autoRestartLoop 带退避地尝试拉起 worker，至多 maxAutoRestarts 次后放弃
//（下次请求到达时 ensureRunning 兜底拉起），避免永续 goroutine。
func (m *Manager) autoRestartLoop(dir string) {
	const maxAutoRestarts = 3
	for i := 0; i < maxAutoRestarts; i++ {
		select {
		case <-m.quit:
			return
		default:
		}
		m.mu.Lock()
		if m.closed || m.procAlive {
			m.mu.Unlock()
			return
		}
		fails, lastFail := m.spawnFails, m.lastSpawnFail
		m.mu.Unlock()

		if err := ValidateTTSModel(dir, DefaultMinModelBytes); err != nil {
			// 模型缺失/无效属环境问题，重试无意义；等请求路径/管理动作触发。
			m.mu.Lock()
			m.available = false
			m.modelDir = dir
			m.initErr = err
			m.mu.Unlock()
			m.logger.Printf("[tts] auto-restart aborted: %v", err)
			return
		}

		m.spawnMu.Lock()
		err := func() error {
			m.mu.Lock()
			if m.closed || m.procAlive {
				m.mu.Unlock()
				return nil
			}
			if wait := backoffRemaining(fails, lastFail, m.cfg); wait > 0 {
				m.mu.Unlock()
				return errBackoffBusy
			}
			m.mu.Unlock()
			return m.trySpawnLocked(dir)
		}()
		m.spawnMu.Unlock()
		if err == nil {
			return // 拉起成功（或他人已拉起）
		}

		wait := m.backoffDuration()
		m.logger.Printf("[tts] auto-restart attempt %d failed (%v), retrying in %s", i+1, err, wait.Round(time.Millisecond))
		select {
		case <-m.quit:
			return
		case <-time.After(wait):
		}
	}
}

// stopWorker 主动杀掉当前 worker（killing 标志确保 onExit 不触发自动重启）。
func (m *Manager) stopWorker() {
	m.mu.Lock()
	proc := m.cmd
	if proc != nil {
		m.killing = true
	}
	m.mu.Unlock()
	if proc == nil {
		return
	}
	_ = proc.Process.Kill()
	// 等待 waitLoop 收尾（5s 兜底超时，避免极端情况下卡住调用方）。
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		alive := m.procAlive && m.cmd == proc
		m.mu.Unlock()
		if !alive || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// backoffRemaining 计算退避窗口剩余时间；<=0 表示已过窗口。
func backoffRemaining(fails int, lastFail time.Time, cfg Config) time.Duration {
	if fails <= 0 {
		return 0
	}
	wait := backoffDurationFor(fails, cfg) - time.Since(lastFail)
	if wait < 0 {
		return 0
	}
	return wait
}

// backoffDuration 计算当前退避时长（读取状态后委托纯函数）。
func (m *Manager) backoffDuration() time.Duration {
	m.mu.Lock()
	fails := m.spawnFails
	m.mu.Unlock()
	return backoffDurationFor(fails, m.cfg)
}

// backoffDurationFor 计算第 fails 次连续失败后的退避时长：
// base << (fails-1)，封顶 max（纯函数，便于测试）。
func backoffDurationFor(fails int, cfg Config) time.Duration {
	if fails <= 0 {
		return cfg.BackoffBase
	}
	d := cfg.BackoffBase
	for i := 1; i < fails; i++ {
		d *= 2
		if d >= cfg.BackoffMax {
			return cfg.BackoffMax
		}
	}
	if d > cfg.BackoffMax {
		d = cfg.BackoffMax
	}
	return d
}

// markSpawnFailed 记录一次 spawn/握手失败并更新引擎不可用状态。
func (m *Manager) markSpawnFailed(modelDir string, err error) {
	m.mu.Lock()
	m.available = false
	m.modelDir = modelDir
	m.initErr = err
	m.spawnFails++
	m.lastSpawnFail = time.Now()
	fails := m.spawnFails
	m.mu.Unlock()
	m.logger.Printf("[tts] worker spawn failed (attempt %d): %v", fails, err)
}
