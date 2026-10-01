package livekitmgr

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ridgericetalk/internal/config"
	"ridgericetalk/tests/testutil"
)

// TestHealthCheckSuccessWhenPortListening covers the success branch of
// healthCheck by starting a local TCP listener on an ephemeral port.
func TestHealthCheckSuccessWhenPortListening(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	// Start a TCP listener on an ephemeral port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	m.cfg = &config.Config{LiveKitPort: port}

	if !m.healthCheck() {
		t.Errorf("expected healthCheck to return true when port is listening")
	}
}

// TestStopWithRunningCmd covers the Stop path where cmd is non-nil and has a
// running Process. We use /bin/sleep (or platform equivalent) so the
// process stays alive long enough for Stop to signal it.
func TestStopWithRunningCmd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on Unix-style process signaling")
	}

	sleepBin := "/bin/sleep"
	if _, err := os.Stat(sleepBin); err != nil {
		// Fallback to /usr/bin/sleep on some systems.
		sleepBin = "/usr/bin/sleep"
		if _, err := os.Stat(sleepBin); err != nil {
			t.Skipf("sleep binary not available")
		}
	}

	log := testutil.TestLogger()
	m := New(log)
	tmpDir := t.TempDir()
	m.cfg = &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: sleepBin,
		Env:               "development",
	}
	m.binaryPath = sleepBin
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	// startProcess will run `sleep 30 --config <path>` which won't actually
	// do anything useful, but the process will stay alive long enough for
	// Stop to signal it.
	if err := m.startProcess(); err != nil {
		t.Fatalf("startProcess failed: %v", err)
	}

	m.started = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// Mimic watchdog: exit immediately when stopCh is closed.
		<-m.stopCh
	}()

	// Stop should signal the process and wait for it to exit.
	m.Stop()
	if m.started {
		t.Errorf("manager should not be started after Stop")
	}
}

// TestStartFullFlowWithTrueBinary covers the full Start → Stop flow using
// /usr/bin/true as a stand-in for the LiveKit binary. The watchdog will start,
// /usr/bin/true exits immediately, but the flow should not panic.
func TestStartFullFlowWithTrueBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on /usr/bin/true existing")
	}

	binaryPath := "/usr/bin/true"
	if _, err := os.Stat(binaryPath); err != nil {
		t.Skipf("/usr/bin/true not available: %v", err)
	}

	log := testutil.TestLogger()
	m := New(log)

	origVal := os.Getenv("RRT_LIVEKIT_AUTOSTART")
	defer os.Setenv("RRT_LIVEKIT_AUTOSTART", origVal)
	os.Unsetenv("RRT_LIVEKIT_AUTOSTART")

	tmpDir := t.TempDir()
	cfg := &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: binaryPath,
		Env:               "development",
	}

	if err := m.Start(cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !m.started {
		t.Errorf("expected manager to be started")
	}

	// Give the watchdog a moment to run a health check.
	time.Sleep(100 * time.Millisecond)

	m.Stop()
	if m.started {
		t.Errorf("manager should not be started after Stop")
	}
}

// TestGenerateConfigWithCustomPort verifies generateConfig writes the
// configured port when non-zero.
func TestGenerateConfigWithCustomPort(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)
	tmpDir := t.TempDir()
	m.cfg = &config.Config{
		LiveKitPort:      9999,
		LiveKitAPIKey:    "customkey",
		LiveKitAPISecret: "customsecret",
		LocalDataPath:    tmpDir,
	}
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	if err := m.generateConfig(); err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "port: 9999") {
		t.Errorf("expected port: 9999, got: %s", content)
	}
	if !strings.Contains(content, "customkey") {
		t.Errorf("expected customkey in config, got: %s", content)
	}
}

// TestStartWithAutostartTrueEnvVar covers the case where RRT_LIVEKIT_AUTOSTART
// is explicitly set to "true" — Start should proceed normally (not return early).
func TestStartWithAutostartTrueEnvVar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on Unix-style PATH manipulation")
	}

	log := testutil.TestLogger()
	m := New(log)

	origVal := os.Getenv("RRT_LIVEKIT_AUTOSTART")
	defer os.Setenv("RRT_LIVEKIT_AUTOSTART", origVal)
	os.Setenv("RRT_LIVEKIT_AUTOSTART", "true")

	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", tmpDir)

	cfg := &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: "", // force discovery, will find nothing
	}

	// Missing binary should be non-fatal (returns nil).
	if err := m.Start(cfg); err != nil {
		t.Errorf("Start should not error with missing binary, got: %v", err)
	}
	if m.started {
		t.Errorf("manager should not be started when binary is missing")
	}
}

// TestStartUsesExplicitBinaryPath verifies that when LiveKitBinaryPath is set,
// discoverBinary is NOT called (the explicit path takes precedence).
func TestStartUsesExplicitBinaryPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on Unix-style non-existent path behavior")
	}

	log := testutil.TestLogger()
	m := New(log)

	origVal := os.Getenv("RRT_LIVEKIT_AUTOSTART")
	defer os.Setenv("RRT_LIVEKIT_AUTOSTART", origVal)
	os.Unsetenv("RRT_LIVEKIT_AUTOSTART")

	tmpDir := t.TempDir()
	cfg := &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: "/nonexistent/explicit/path/livekit-server",
		Env:               "development",
	}

	err := m.Start(cfg)
	if err == nil {
		t.Fatal("expected Start to fail with non-existent explicit binary path")
	}
	// The binary path should have been set to the explicit value.
	if m.binaryPath != "/nonexistent/explicit/path/livekit-server" {
		t.Errorf("expected binaryPath to be the explicit value, got %q", m.binaryPath)
	}
}

// TestPrefixWriterMultiByte verifies prefixWriter handles multi-byte UTF-8
// input correctly.
func TestPrefixWriterMultiByte(t *testing.T) {
	log := testutil.TestLogger()
	w := &prefixWriter{prefix: "[livekit] ", log: log}

	input := "中文测试\n日本語\n"
	n, err := w.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != len(input) {
		t.Errorf("expected n=%d, got %d", len(input), n)
	}
}

// TestStopIdempotent covers calling Stop twice — the second call should be a
// safe no-op because started is already false.
func TestStopIdempotent(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	// First Stop on unstarted manager.
	m.Stop()
	if m.started {
		t.Errorf("manager should not be started after first Stop")
	}

	// Second Stop should also be safe.
	m.Stop()
	if m.started {
		t.Errorf("manager should not be started after second Stop")
	}
}
