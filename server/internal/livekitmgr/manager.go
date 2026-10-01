package livekitmgr

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/logger"
	"ridgericetalk/internal/thirdparty"
)

const (
	maxRestartAttempts  = 10
	healthCheckInterval = 5 * time.Second
	initialBackoff      = 2 * time.Second
	maxBackoff          = 60 * time.Second
	stopWaitTimeout     = 5 * time.Second

	// N14 启动宽限窗：每次子进程启动后的探活宽限期。
	// NAT/虚拟机环境下 LiveKit 冷启动实测需约 6s（完成 STUN 外部 IP 校验后
	// 才会 bind 7880 信令端口），而探活间隔仅 5s——若无宽限窗，watchdog 会在
	// 端口绑定前就判死并 kill 重启，进程每次都死于同一绑定时序，10 次重试
	// 全部耗尽后 giving up（journalctl 实录连续 10 次 "process unhealthy,
	// restarting"，语音功能全灭且不自愈）。取 30s 为实测 6s 的 5 倍余量，
	// 同时不至于明显拖慢真实故障的恢复节奏。
	startupGraceWindow = 30 * time.Second
)

// Manager manages the LiveKit server subprocess lifecycle.
type Manager struct {
	cfg          *config.Config
	log          *logger.Logger
	binaryPath   string
	configPath   string
	cmd          *exec.Cmd
	mu           sync.Mutex
	stopCh       chan struct{}
	wg           sync.WaitGroup
	restartCount int
	started      bool

	// N14 启动宽限窗：graceDeadline 之前探活失败不 kill、不计 restartCount。
	// 每次 startProcess 成功后重置；watchdog 单 goroutine 读写，经 mu 保护。
	graceDeadline time.Time
	// exited 由 reap goroutine 在子进程退出后关闭，供宽限窗内非阻塞存活探测。
	// 每次 startProcessReal 成功后重建，与 m.cmd 配对（读取须持 mu）。
	exited chan struct{}

	// 以下为单测注入点（生产路径为 nil / realClock，行为与注入前一致）：
	// clock 时间源、healthCheckFn 探活函数、startProcessFn 启动函数。
	clock          clock
	healthCheckFn  func() bool
	startProcessFn func() error
}

// New creates a new LiveKit manager.
func New(log *logger.Logger) *Manager {
	return &Manager{
		log:    log,
		stopCh: make(chan struct{}),
		clock:  realClock{},
	}
}

// Start discovers the binary, generates config, and starts LiveKit with a watchdog.
func (m *Manager) Start(cfg *config.Config) error {
	m.cfg = cfg

	// Allow explicit opt-out, unless embedded dependency management is enabled.
	if os.Getenv("RRT_LIVEKIT_AUTOSTART") == "false" && !cfg.EmbeddedDeps {
		m.log.Info("[livekit] auto-start disabled via RRT_LIVEKIT_AUTOSTART=false")
		return nil
	}
	if cfg.EmbeddedDeps {
		m.log.Info("[livekit] embedded deps enabled, will start LiveKit if available")
	}

	binary := cfg.LiveKitBinaryPath
	if binary == "" {
		binary = m.discoverBinary()
	}
	if binary == "" && cfg.EmbeddedDeps {
		m.log.Info("[livekit] embedded deps enabled, attempting to download LiveKit")
		versions, err := thirdparty.LoadVersions(config.EmbeddedVersionsTOML)
		if err != nil {
			m.log.Warn("[livekit] failed to load versions manifest", "error", err)
		} else {
			binPath, err := thirdparty.EnsureLiveKit(versions, cfg.ThirdPartyDir, func(msg string, args ...any) {
				m.log.Info(fmt.Sprintf(msg, args...))
			})
			if err != nil {
				m.log.Warn("[livekit] failed to download LiveKit", "error", err)
			} else {
				binary = binPath
			}
		}
	}
	if binary == "" {
		m.log.Warn("[livekit] binary not found, voice features may be unavailable")
		return nil // non-fatal: user may run LiveKit externally
	}
	m.binaryPath = binary

	// Determine config path
	m.configPath = filepath.Join(cfg.LocalDataPath, "livekit.yaml")
	if err := os.MkdirAll(cfg.LocalDataPath, 0755); err != nil {
		return fmt.Errorf("create livekit config dir: %w", err)
	}

	if err := m.generateConfig(); err != nil {
		return fmt.Errorf("generate livekit config: %w", err)
	}

	if err := m.startProcess(); err != nil {
		return fmt.Errorf("start livekit: %w", err)
	}

	m.started = true
	m.restartCount = 0

	// Start watchdog
	m.wg.Add(1)
	go m.watchdog()

	// Quick initial health check
	time.Sleep(500 * time.Millisecond)
	if m.healthCheck() {
		m.log.Info("[livekit] health check passed, ready", "url", cfg.LiveKitURL)
	}

	return nil
}

// Stop gracefully shuts down the LiveKit subprocess.
func (m *Manager) Stop() {
	if !m.started {
		return
	}
	m.log.Info("[livekit] stopping subprocess...")
	close(m.stopCh)

	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		// Try graceful shutdown first
		if runtime.GOOS == "windows" {
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(os.Interrupt)
		}

		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()

		select {
		case <-done:
			m.log.Info("[livekit] subprocess exited gracefully")
		case <-time.After(stopWaitTimeout):
			_ = cmd.Process.Kill()
			m.log.Warn("[livekit] subprocess force killed after timeout")
		}
	}

	m.wg.Wait()
	m.started = false
}

// InGraceWindow reports whether the startup grace window (N14) is currently
// active, i.e. the last startProcess succeeded less than startupGraceWindow
// ago. While inside the window a failed port probe is EXPECTED (LiveKit may
// take several seconds to bind 7880 after STUN checks), so consumers such as
// the admin alert evaluator (DES-20261001-01 §12.2) must not raise alerts.
// Mutex-guarded: the watchdog goroutine writes graceDeadline under m.mu.
func (m *Manager) InGraceWindow() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.graceDeadline.IsZero() {
		return false
	}
	return m.clock.Now().Before(m.graceDeadline)
}

// Running reports whether the manager considers LiveKit up: it was started
// (Start succeeded and Stop has not run) AND the subprocess has not exited.
// Used by the admin alert evaluator to distinguish "binary not managed here"
// (started=false, evaluator falls back to a plain TCP probe) from "managed
// subprocess died" (restart pending). Mutex-guarded like the watchdog path.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started && m.processAliveLocked()
}

// discoverBinary searches for the LiveKit executable in common locations.
func (m *Manager) discoverBinary() string {
	exeName := "livekit-server"
	if runtime.GOOS == "windows" {
		exeName = "livekit-server.exe"
	}

	// Candidate directories relative to working directory
	candidates := []string{
		"./livekit/" + exeName,
		"../livekit/" + exeName,
		"../../livekit/" + exeName,
		"./" + exeName,
		"./bin/" + exeName,
	}

	for _, p := range candidates {
		if abs, err := filepath.Abs(p); err == nil {
			if info, err := os.Stat(abs); err == nil && !info.IsDir() {
				return abs
			}
		}
	}

	// Check PATH
	if path, err := exec.LookPath(exeName); err == nil {
		return path
	}

	return ""
}

// generateConfig writes a LiveKit config that matches our API key/secret.
//
// ISSUE-082: previously this wrote only the top-level `port` (WS 信令) and
// left rtc.* unset. LiveKit 的 rtc.tcp_port 默认为 7881，而 rtc.udp_port
// 默认 0（不监听 UDP），导致 server-info 宣告的 mediaUdpPort=7882 无对应
// 监听。这里显式配置 rtc 段（udp 7882 / tcp 7881 / node_ip 自动检测），使
// 监听与 server-state.json 的外部端口宣告一致，恢复 WebRTC 标准 UDP 媒体
// 路径（TCP 7881 保留为降级）。
//
// ISSUE-082 caveat: watchdog 触发子进程重启时 startProcess() 会复用此文件
// 而不再重写，如需修改必须重启后端服务才能生效。
//
// NOTE: 端口当前与默认宣告值绑定；若管理员在管理后台修改外部端口，此文件
// 不会自动跟随（livekit.yaml 渲染链路同样只在首次启动时渲染）。
func (m *Manager) generateConfig() error {
	// 优先从环境变量读取端口（deploy-baremetal.sh 通过 .env.production 注入），
	// 避免 livekit.yaml 被硬编码的 7880/7881/7882 覆盖。
	port := m.cfg.LiveKitPort
	if port == 0 {
		port = 7880
	}

	// TCP/UDP 端口：优先读环境变量，回退到默认值 7881/7882。
	// 部署脚本通过 RRT_LIVEKIT_TCP_PORT / RRT_LIVEKIT_UDP_PORT 注入用户指定的端口。
	udpPort := 7882
	tcpPort := 7881
	if v := os.Getenv("RRT_LIVEKIT_TCP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			tcpPort = p
		}
	}
	if v := os.Getenv("RRT_LIVEKIT_UDP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			udpPort = p
		}
	}

	// 公网部署需要 use_external_ip=true 以支持 STUN 公网 IP 检测；
	// 纯内网部署可通过 RRT_LIVEKIT_USE_EXTERNAL_IP=false 关闭。
	useExternalIP := true
	if v := os.Getenv("RRT_LIVEKIT_USE_EXTERNAL_IP"); v == "false" {
		useExternalIP = false
	}

	// ICE 候选地址：优先 node_ip 环境变量，否则自动检测本机非 loopback IPv4，
	// 避免 use_external_ip STUN 检测在纯内网部署下报出不可达的公网候选。
	nodeIP := os.Getenv("RRT_LIVEKIT_NODE_IP")
	if nodeIP == "" {
		if ip, err := config.DetectNodeIP(); err == nil {
			nodeIP = ip
		}
	}

	content := fmt.Sprintf(`# Auto-generated by RidgeRiceTalk — do not edit manually
port: %d
keys:
  %s: %s
logging:
  level: info
  json: false
# FIX-2026-0731-01 P1: 显式声明 room.enabled_codecs，避免运行时配置丢失
# 导致 codec 协商回退到 VP8 软编码。VP9 优先以匹配客户端 SVC 分层发布。
room:
  enabled_codecs:
    - mime: audio/opus
    - mime: video/vp9
    - mime: video/vp8
    - mime: video/h264
rtc:
  udp_port: %d
  tcp_port: %d
  use_external_ip: %v
  use_ice_lite: true
`, port, m.cfg.LiveKitAPIKey, m.cfg.LiveKitAPISecret, udpPort, tcpPort, useExternalIP)

	if nodeIP != "" {
		content += fmt.Sprintf("  node_ip: %s\n", nodeIP)
	}

	// Webhook：LiveKit 向本服务推送 participant_joined/left 等事件，使
	// voice_participants 表始终与 LiveKit 真实参与者对齐（服务端重启后客户端
	// LiveKit 自动重连不会重新走 HTTP join，DB 记录会丢失导致成员列表塌陷）。
	// 仅当本服务有 HTTP 监听端口时配置；LiveKit 子进程与后端同机，走 127.0.0.1。
	// 注意：LiveKit 的 webhook 段只有 api_key + urls（签名 secret 由顶层 keys
	// 段中该 key 对应的 secret 提供，无需也不能在此重复声明）。
	if m.cfg.Port > 0 {
		content += fmt.Sprintf("webhook:\n  api_key: %s\n  urls:\n    - http://127.0.0.1:%d/api/livekit/webhook\n",
			m.cfg.LiveKitAPIKey, m.cfg.Port)
	}

	return os.WriteFile(m.configPath, []byte(content), 0644)
}

// startProcess 是启动子进程的统一入口：成功后重置 N14 启动宽限截止时间。
// startProcessFn 仅用于单测注入；生产路径走 startProcessReal。
func (m *Manager) startProcess() error {
	var err error
	if m.startProcessFn != nil {
		err = m.startProcessFn()
	} else {
		err = m.startProcessReal()
	}
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.graceDeadline = m.clock.Now().Add(startupGraceWindow)
	m.mu.Unlock()
	m.log.Info("[livekit] startup grace window armed", "window", startupGraceWindow.String())
	return nil
}

// startProcessReal 启动 LiveKit 子进程，并为宽限窗存活探测启动 reap goroutine。
func (m *Manager) startProcessReal() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	args := []string{"--config", m.configPath}
	if m.cfg.Env == "development" {
		// Dev mode: allow localhost without TLS
		args = append(args, "--dev")
	}

	cmd := exec.Command(m.binaryPath, args...)
	cmd.Stdout = &prefixWriter{prefix: "[livekit] ", log: m.log}
	cmd.Stderr = &prefixWriter{prefix: "[livekit:err] ", log: m.log}

	if err := cmd.Start(); err != nil {
		return err
	}

	m.cmd = cmd

	// N14: reap goroutine —— 进程退出后关闭 exited 通道，供宽限窗内的非阻塞
	// 存活探测使用。注意它与 watchdog/Stop 中的 cmd.Process.Wait() 存在收尸
	// 竞争，但双方均忽略 Wait 错误：无论哪一方抢先收尸，「Wait 返回即进程已
	// 退出」这一不变量都成立，与改造前行为一致。
	m.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(m.exited)
	}()

	m.log.Info("[livekit] process started", "pid", cmd.Process.Pid, "config", m.configPath)
	return nil
}

// healthCheck attempts a TCP connection to the LiveKit port.
// healthCheckFn 供单测注入覆盖；生产路径走 defaultHealthCheck。
func (m *Manager) healthCheck() bool {
	if m.healthCheckFn != nil {
		return m.healthCheckFn()
	}
	return m.defaultHealthCheck()
}

// defaultHealthCheck attempts a TCP connection to the LiveKit port.
func (m *Manager) defaultHealthCheck() bool {
	port := m.cfg.LiveKitPort
	if port == 0 {
		port = 7880
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// processAliveLocked 非阻塞判断当前子进程是否仍在运行（N14 宽限窗探测用）。
// 依赖 startProcessReal 启动的 reap goroutine 在进程退出后关闭 exited 通道；
// 必须在持有 m.mu 时调用，保证 cmd 与 exited 通道配对读取。
func (m *Manager) processAliveLocked() bool {
	if m.cmd == nil || m.cmd.Process == nil {
		return false
	}
	if m.exited == nil {
		// 理论不可达（startProcessReal 成功后 exited 必非 nil）；防御异常路径
		// 下把活进程误判为已退出而触发误重启——宁可在宽限窗内多等一个周期。
		return true
	}
	select {
	case <-m.exited:
		return false
	default:
		return true
	}
}

// watchdog monitors the LiveKit process and restarts it if needed.
func (m *Manager) watchdog() {
	defer m.wg.Done()

	ticker := m.clock.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	backoff := initialBackoff

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C():
		}

		if m.healthCheck() {
			// Healthy: reset backoff and restart count
			if backoff != initialBackoff {
				m.log.Info("[livekit] health check recovered")
			}
			backoff = initialBackoff
			m.mu.Lock()
			m.restartCount = 0
			m.mu.Unlock()
			continue
		}

		// N14 启动宽限窗：探活失败不一定是故障。NAT/虚拟机下 LiveKit 冷启动
		// 需约 6s（STUN 外部 IP 校验后才 bind 7880）。宽限窗内只探测进程是否
		// 存活——活着则跳过本轮（不 kill、不计 restartCount），已退出则立即
		// 走重启路径，不空等宽限结束。宽限窗结束后恢复原有的 kill+退避重启。
		m.mu.Lock()
		inGrace := m.clock.Now().Before(m.graceDeadline)
		alive := m.processAliveLocked()
		m.mu.Unlock()

		if inGrace && alive {
			m.log.Info("[livekit] startup grace window active, port not ready yet, skip restart check")
			continue
		}

		// Unhealthy（宽限期已过，或宽限期内进程已退出）
		m.mu.Lock()
		cmd := m.cmd
		m.mu.Unlock()

		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}

		m.mu.Lock()
		m.restartCount++
		attempts := m.restartCount
		giveUp := m.restartCount > maxRestartAttempts
		m.mu.Unlock()

		if giveUp {
			m.log.Error("[livekit] max restart attempts reached, giving up", "attempts", maxRestartAttempts)
			return
		}

		m.log.Warn("[livekit] process unhealthy, restarting",
			"attempt", attempts,
			"backoff", backoff.String(),
		)

		select {
		case <-m.stopCh:
			return
		case <-m.clock.After(backoff):
		}

		if err := m.startProcess(); err != nil {
			m.log.Error("[livekit] restart failed", "error", err)
		}

		// Exponential backoff capped at maxBackoff
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// clock 抽象时间源，使 watchdog 的节奏（探活间隔、退避等待、宽限窗截止）在
// 单测中可被假时钟驱动，避免真实睡眠带来的测试脆弱性。生产路径使用 realClock。
type clock interface {
	Now() time.Time
	NewTicker(d time.Duration) clockTicker
	After(d time.Duration) <-chan time.Time
}

// clockTicker 抽象 time.Ticker，配合 clock 使用。
type clockTicker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) NewTicker(d time.Duration) clockTicker  { return &realTicker{t: time.NewTicker(d)} }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type realTicker struct{ t *time.Ticker }

func (t *realTicker) C() <-chan time.Time { return t.t.C }
func (t *realTicker) Stop()               { t.t.Stop() }

// prefixWriter prefixes log lines from the subprocess.
type prefixWriter struct {
	prefix string
	log    *logger.Logger
}

func (w *prefixWriter) Write(p []byte) (n int, err error) {
	lines := strings.Split(string(p), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			w.log.Info(w.prefix + line)
		}
	}
	return len(p), nil
}
