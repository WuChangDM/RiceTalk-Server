package livekitmgr

import (
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ridgericetalk/internal/config"
	"ridgericetalk/tests/testutil"
)

// N14：LiveKit 监督器启动宽限窗单测。
// 背景：NAT/虚拟机下 LiveKit 冷启动需约 6s（STUN 外部 IP 校验后才 bind 7880），
// 旧 watchdog 每 5s 对端口 TCP dial，失败即 kill 重启，进程每次都死于绑定时序，
// 10 次重试全部耗尽后 giving up。本文件用假时钟驱动 watchdog，锁定宽限窗的
// 五个行为面：宽限期内不杀不计、宽限期满恢复杀+重启、宽限期内进程真崩立即
// 重启、健康恢复重置计数、10 次上限仍生效。

// fakeClock 可操控的假时钟：advance 推进虚拟时间并投放探活 tick，
// fireAfter 解除 watchdog 退避等待（clock.After）的阻塞。
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickCh  chan time.Time
	afterCh chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		tickCh:  make(chan time.Time, 8),
		afterCh: make(chan time.Time, 8),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTicker(time.Duration) clockTicker {
	return &fakeTicker{c: c}
}

func (c *fakeClock) After(time.Duration) <-chan time.Time { return c.afterCh }

// advance 推进虚拟时间并向 watchdog 投放一个探活 tick。
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	c.tickCh <- c.now
}

// fireAfter 解除一次退避等待（对应 watchdog 中 clock.After(backoff)）。
func (c *fakeClock) fireAfter() { c.afterCh <- c.now }

type fakeTicker struct{ c *fakeClock }

func (t *fakeTicker) C() <-chan time.Time { return t.c.tickCh }
func (t *fakeTicker) Stop()               {}

// waitForCond 轮询等待条件成立，超时 3s 则失败。
func waitForCond(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for condition: %s", msg)
}

// managerRestartCount 持锁读取 restartCount（watchdog 内部同样持锁写，
// 保证测试与 watchdog goroutine 之间无数据竞争）。
func managerRestartCount(m *Manager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restartCount
}

// quickExitCmd 返回一个立即退出的子进程，用于模拟宽限期内进程崩溃。
func quickExitCmd() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "exit", "0")
	}
	return exec.Command("true")
}

// startAliveCmd 启动一个存活约 60s 的子进程，用于模拟宽限期内「进程活着
// 但端口尚未 bind」的冷启动状态。
func startAliveCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "ping", "-n", "60", "127.0.0.1")
	} else {
		cmd = exec.Command("sleep", "60")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start alive cmd: %v", err)
	}
	return cmd
}

// killTestCmd 清理测试中启动的子进程。
func killTestCmd(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// newGraceTestManager 构造注入假时钟的 manager（探活恒失败、启动可计数）。
// 返回 manager、假时钟、启动调用计数、启动完成信号。
// 注意：每次 startProcess 调用都会向 started 投递一个信号，测试在初始
// m.startProcess() 之后必须 <-started 排水，使信号流与调用一一对应。
func newGraceTestManager(t *testing.T) (*Manager, *fakeClock, *int32, chan struct{}) {
	t.Helper()
	log := testutil.TestLogger()
	fc := newFakeClock()
	m := New(log)
	m.cfg = &config.Config{LiveKitPort: 59999}
	m.clock = fc

	started := make(chan struct{}, 16)
	var startCalls int32
	m.healthCheckFn = func() bool { return false }
	m.startProcessFn = func() error {
		atomic.AddInt32(&startCalls, 1)
		started <- struct{}{}
		return nil
	}
	return m, fc, &startCalls, started
}

// TestWatchdogGraceWindowSkipsRestartWhileProcessAlive 宽限期内端口探活
// 失败但进程存活 → 不杀不计：不触发重启、不累计 restartCount、watchdog 不放弃。
func TestWatchdogGraceWindowSkipsRestartWhileProcessAlive(t *testing.T) {
	m, fc, startCalls, _ := newGraceTestManager(t)

	var hcCalls int32
	m.healthCheckFn = func() bool {
		atomic.AddInt32(&hcCalls, 1)
		return false // 模拟冷启动期间 7880 尚未 bind
	}
	// 模拟「进程活着」：挂上真存活进程与未关闭的 exited 通道（与生产
	// startProcessReal 的 reap 语义一致——进程未退出则通道不关闭）。
	var alive *exec.Cmd
	m.startProcessFn = func() error {
		atomic.AddInt32(startCalls, 1)
		alive = startAliveCmd(t)
		m.mu.Lock()
		m.cmd = alive
		m.exited = make(chan struct{})
		m.mu.Unlock()
		return nil
	}
	defer func() { killTestCmd(alive) }()

	if err := m.startProcess(); err != nil { // 初始启动（武装宽限窗）
		t.Fatalf("startProcess: %v", err)
	}

	m.wg.Add(1)
	watchDone := make(chan struct{})
	go func() {
		m.watchdog()
		close(watchDone)
	}()

	// 宽限窗内推进 3 个探活周期（15s < 30s），全部应被跳过
	for i := 1; i <= 3; i++ {
		fc.advance(healthCheckInterval)
		waitForCond(t, func() bool { return atomic.LoadInt32(&hcCalls) >= int32(i) }, "health check tick")
	}
	time.Sleep(20 * time.Millisecond) // 让宽限跳过分支执行完

	if got := atomic.LoadInt32(startCalls); got != 1 {
		t.Errorf("宽限期内不应重启进程: startCalls = %d, want 1（仅初始启动）", got)
	}
	if got := managerRestartCount(m); got != 0 {
		t.Errorf("宽限期内不应累计 restartCount: got %d, want 0", got)
	}
	select {
	case <-watchDone:
		t.Fatal("watchdog 不应在宽限期内放弃")
	default:
	}

	close(m.stopCh)
	m.wg.Wait()
}

// TestWatchdogRestartsAfterGraceWindowExpires 宽限期满探活仍失败 →
// 恢复既有行为：kill + 计数 + 退避后重启。
func TestWatchdogRestartsAfterGraceWindowExpires(t *testing.T) {
	m, fc, startCalls, started := newGraceTestManager(t)

	if err := m.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	<-started // 排掉初始启动信号，使信号流与调用一一对应

	m.wg.Add(1)
	go m.watchdog()

	// 推进越过宽限窗（35s > 30s），探活仍失败 → 应重启
	fc.advance(35 * time.Second)
	waitForCond(t, func() bool { return managerRestartCount(m) == 1 }, "restartCount==1 after grace window")
	fc.fireAfter()
	<-started

	if got := atomic.LoadInt32(startCalls); got != 2 {
		t.Errorf("宽限期满失败应触发一次重启: startCalls = %d, want 2", got)
	}
	if got := managerRestartCount(m); got != 1 {
		t.Errorf("restartCount = %d, want 1", got)
	}

	close(m.stopCh)
	m.wg.Wait()
}

// TestWatchdogRestartsImmediatelyWhenProcessDiesDuringGrace 宽限期内进程
// 真崩（已退出）→ 立即走重启路径，不必等宽限窗结束。
func TestWatchdogRestartsImmediatelyWhenProcessDiesDuringGrace(t *testing.T) {
	log := testutil.TestLogger()
	fc := newFakeClock()
	m := New(log)
	m.cfg = &config.Config{LiveKitPort: 59999}
	m.clock = fc

	// 注入的 startProcess 启动一个立即退出的真进程，并挂上与生产一致的
	// reap goroutine，验证 exited 通道存活探测真实生效。
	var startCalls int32
	m.healthCheckFn = func() bool { return false }
	m.startProcessFn = func() error {
		atomic.AddInt32(&startCalls, 1)
		cmd := quickExitCmd()
		if err := cmd.Start(); err != nil {
			return err
		}
		m.mu.Lock()
		m.cmd = cmd
		m.exited = make(chan struct{})
		m.mu.Unlock()
		go func() {
			_ = cmd.Wait()
			close(m.exited)
		}()
		return nil
	}

	if err := m.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	// 等首次进程退出（reap 完成）→ 宽限期内进程已死
	waitForCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		select {
		case <-m.exited:
			return true
		default:
			return false
		}
	}, "first process should exit during grace window")

	m.wg.Add(1)
	go m.watchdog()

	// 宽限窗内（仅推进 5s）探活失败 + 进程已死 → 应立即重启而非空等 30s
	fc.advance(healthCheckInterval)
	waitForCond(t, func() bool { return managerRestartCount(m) == 1 }, "restartCount==1 on dead process during grace")
	fc.fireAfter()
	waitForCond(t, func() bool { return atomic.LoadInt32(&startCalls) == 2 }, "second startProcess")

	if got := atomic.LoadInt32(&startCalls); got != 2 {
		t.Errorf("宽限期内进程崩溃应立即重启: startCalls = %d, want 2", got)
	}
	if got := managerRestartCount(m); got != 1 {
		t.Errorf("restartCount = %d, want 1", got)
	}

	close(m.stopCh)
	m.wg.Wait()
}

// TestWatchdogHealthRecoveryResetsRestartCount 探活恢复健康 →
// restartCount 归零（原行为不回退）。
func TestWatchdogHealthRecoveryResetsRestartCount(t *testing.T) {
	log := testutil.TestLogger()
	fc := newFakeClock()
	m := New(log)
	m.cfg = &config.Config{LiveKitPort: 59999}
	m.clock = fc

	var hcState, hcCalls int32 // hcState: 0=不可达, 1=恢复
	started := make(chan struct{}, 16)
	m.healthCheckFn = func() bool {
		atomic.AddInt32(&hcCalls, 1)
		return atomic.LoadInt32(&hcState) == 1
	}
	m.startProcessFn = func() error {
		started <- struct{}{}
		return nil
	}

	if err := m.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	<-started // 排掉初始启动信号

	m.wg.Add(1)
	go m.watchdog()

	// 推进越过宽限窗，连续两次失败重启
	fc.advance(35 * time.Second)
	waitForCond(t, func() bool { return managerRestartCount(m) == 1 }, "restart #1")
	fc.fireAfter()
	<-started
	fc.advance(healthCheckInterval)
	waitForCond(t, func() bool { return managerRestartCount(m) == 2 }, "restart #2")
	fc.fireAfter()
	<-started

	// 探活恢复健康 → restartCount 归零
	atomic.StoreInt32(&hcState, 1)
	fc.advance(healthCheckInterval)
	waitForCond(t, func() bool { return atomic.LoadInt32(&hcCalls) >= 3 }, "recovery health check")
	time.Sleep(20 * time.Millisecond) // 让恢复分支执行完

	if got := managerRestartCount(m); got != 0 {
		t.Errorf("健康恢复后 restartCount = %d, want 0", got)
	}

	close(m.stopCh)
	m.wg.Wait()
}

// TestWatchdogGivesUpAfterMaxRestartAttempts 宽限窗外连续失败 →
// 10 次重启上限仍生效，watchdog 放弃退出。
func TestWatchdogGivesUpAfterMaxRestartAttempts(t *testing.T) {
	m, fc, startCalls, started := newGraceTestManager(t)

	if err := m.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	<-started // 排掉初始启动信号

	m.wg.Add(1)
	watchDone := make(chan struct{})
	go func() {
		m.watchdog()
		close(watchDone)
	}()

	fc.advance(35 * time.Second) // 越过宽限窗
	// 前 10 次失败各触发一次重启
	for i := 1; i <= maxRestartAttempts; i++ {
		waitForCond(t, func() bool { return managerRestartCount(m) == i }, fmt.Sprintf("restart #%d", i))
		fc.fireAfter()
		<-started
		if i < maxRestartAttempts {
			fc.advance(healthCheckInterval)
		}
	}

	// 第 11 次失败 → 超过上限，watchdog 放弃退出
	fc.advance(healthCheckInterval)
	select {
	case <-watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog 应在超过 maxRestartAttempts 后放弃退出")
	}
	if got := atomic.LoadInt32(startCalls); got != int32(maxRestartAttempts)+1 {
		t.Errorf("总启动次数 = %d, want %d（初始 1 次 + %d 次重启）", got, maxRestartAttempts+1, maxRestartAttempts)
	}

	close(m.stopCh)
	// watchdog 已退出，Wait 应立即返回
	m.wg.Wait()
}

// TestManagerInGraceWindow — A11（DES-20261001-01 §12.2）导出访问器语义：
// admin 告警评估器经 InGraceWindow() 判断「宽限窗内跳过不告警」。
// graceDeadline 为包私有字段，这里（同包测试）直接注入验证读写与过期语义。
func TestManagerInGraceWindow(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)
	fc := newFakeClock()
	m.clock = fc

	if m.InGraceWindow() {
		t.Fatal("零值 graceDeadline 不应被视为宽限窗内")
	}

	// 注入未来 30s 的宽限截止：窗内。
	m.mu.Lock()
	m.graceDeadline = fc.Now().Add(startupGraceWindow)
	m.mu.Unlock()
	if !m.InGraceWindow() {
		t.Fatal("graceDeadline 在未来时应报告宽限窗内")
	}

	// 虚拟时间越过截止：窗外。
	fc.mu.Lock()
	fc.now = fc.now.Add(startupGraceWindow + time.Second)
	fc.mu.Unlock()
	if m.InGraceWindow() {
		t.Fatal("graceDeadline 已过期时不应报告宽限窗内")
	}
}

// TestManagerRunningAccessor — A11 评估器用于区分「托管进程已死」的访问器。
func TestManagerRunningAccessor(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)
	if m.Running() {
		t.Fatal("未 Start 的 manager 不应报告 Running")
	}

	// started=true 但进程未启动（cmd=nil）→ Running=false（等待自动重启）。
	m.mu.Lock()
	m.started = true
	m.mu.Unlock()
	if m.Running() {
		t.Fatal("无子进程时不应报告 Running")
	}
	m.mu.Lock()
	m.started = false
	m.mu.Unlock()
}
