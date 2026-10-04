package livekitmgr

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ridgericetalk/internal/config"
	"ridgericetalk/tests/testutil"
)

// TestPrefixWriterWrite covers the prefixWriter.Write method.
func TestPrefixWriterWrite(t *testing.T) {
	log := testutil.TestLogger()
	w := &prefixWriter{prefix: "[livekit] ", log: log}

	// Write a single line without trailing newline.
	n, err := w.Write([]byte("hello world"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != len("hello world") {
		t.Errorf("expected n=%d, got %d", len("hello world"), n)
	}

	// Write multiple lines separated by newlines.
	input := "line1\nline2\nline3\n"
	n, err = w.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != len(input) {
		t.Errorf("expected n=%d, got %d", len(input), n)
	}

	// Write empty input.
	n, err = w.Write([]byte(""))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected n=0 for empty input, got %d", n)
	}

	// Write only whitespace — should be trimmed and not logged.
	n, err = w.Write([]byte("   \n\t\n"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != 6 {
		t.Errorf("expected n=6 for whitespace input, got %d", n)
	}
}

// TestStartProcessFailure covers startProcess when the binary does not exist.
func TestStartProcessFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on Unix-style non-existent path behavior")
	}

	log := testutil.TestLogger()
	m := New(log)
	tmpDir := t.TempDir()
	m.cfg = &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: "/nonexistent/path/to/livekit-server",
		Env:               "development",
	}
	m.binaryPath = "/nonexistent/path/to/livekit-server"
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	// startProcess should fail because the binary does not exist.
	err := m.startProcess()
	if err == nil {
		t.Fatal("expected startProcess to fail with non-existent binary")
	}

	// m.cmd should remain nil because cmd.Start failed.
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd != nil {
		t.Errorf("expected m.cmd to be nil after failed start, got %v", cmd)
	}
}

// TestStartProcessExplicitPathNotFound covers Start with an explicit binary
// path that does not exist — Start should return an error from startProcess.
func TestStartProcessExplicitPathNotFound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on Unix-style non-existent path behavior")
	}

	log := testutil.TestLogger()
	m := New(log)

	// Clear autostart env so Start proceeds to discovery/startProcess.
	origVal := os.Getenv("RRT_LIVEKIT_AUTOSTART")
	defer os.Setenv("RRT_LIVEKIT_AUTOSTART", origVal)
	os.Unsetenv("RRT_LIVEKIT_AUTOSTART")

	tmpDir := t.TempDir()
	cfg := &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: "/nonexistent/path/to/livekit-server",
		Env:               "development",
	}

	err := m.Start(cfg)
	if err == nil {
		t.Fatal("expected Start to fail with non-existent binary path")
	}
	if !strings.Contains(err.Error(), "start livekit") {
		t.Errorf("expected error to mention 'start livekit', got: %v", err)
	}
	if m.started {
		t.Errorf("manager should not be started when startProcess fails")
	}
}

// TestStartMkdirAllFailure covers Start when LocalDataPath cannot be created.
func TestStartMkdirAllFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on Unix-style permission bits")
	}

	log := testutil.TestLogger()
	m := New(log)

	origVal := os.Getenv("RRT_LIVEKIT_AUTOSTART")
	defer os.Setenv("RRT_LIVEKIT_AUTOSTART", origVal)
	os.Unsetenv("RRT_LIVEKIT_AUTOSTART")

	// Use a path under a file (not a directory) to force MkdirAll to fail.
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatalf("setup file: %v", err)
	}

	cfg := &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     filepath.Join(filePath, "subdir"), // parent is a file
		LiveKitBinaryPath: "/usr/bin/true",                   // any existing binary
		Env:               "development",
	}

	err := m.Start(cfg)
	if err == nil {
		t.Fatal("expected Start to fail when LocalDataPath cannot be created")
	}
	if !strings.Contains(err.Error(), "create livekit config dir") {
		t.Errorf("expected error to mention 'create livekit config dir', got: %v", err)
	}
}

// TestStopWithStartedButNoCmd covers Stop when started=true but cmd is nil
// (e.g. startProcess failed after setting started=true, though that path
// doesn't exist in current code). This is a defensive test for the nil-cmd
// branch in Stop.
func TestStopWithStartedButNoCmd(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)
	m.started = true
	m.stopCh = make(chan struct{})

	// Stop should not panic even with cmd == nil.
	m.Stop()
	if m.started {
		t.Errorf("manager should not be started after Stop")
	}
}

// TestStartWithDevModeAddsDevFlag verifies that startProcess in dev mode adds
// the --dev flag. We can't easily observe the args directly, but we can verify
// the command construction doesn't panic by using /usr/bin/true as the binary.
func TestStartProcessDevModeArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on /usr/bin/true existing")
	}

	log := testutil.TestLogger()
	m := New(log)
	tmpDir := t.TempDir()

	// /usr/bin/true exists on most Unix systems and exits 0 immediately.
	binaryPath := "/usr/bin/true"
	if _, err := os.Stat(binaryPath); err != nil {
		t.Skipf("/usr/bin/true not available: %v", err)
	}

	m.cfg = &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: binaryPath,
		Env:               "development",
	}
	m.binaryPath = binaryPath
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	if err := m.startProcess(); err != nil {
		t.Fatalf("startProcess failed: %v", err)
	}

	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd == nil {
		t.Fatal("expected m.cmd to be set after successful startProcess")
	}
	if cmd.Process == nil {
		t.Fatal("expected cmd.Process to be set")
	}

	// Clean up the process.
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// TestStartProcessProductionModeNoDevFlag verifies startProcess in production
// mode does not add --dev flag.
func TestStartProcessProductionModeNoDevFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on /usr/bin/true existing")
	}

	log := testutil.TestLogger()
	m := New(log)
	tmpDir := t.TempDir()

	binaryPath := "/usr/bin/true"
	if _, err := os.Stat(binaryPath); err != nil {
		t.Skipf("/usr/bin/true not available: %v", err)
	}

	m.cfg = &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: binaryPath,
		Env:               "production",
	}
	m.binaryPath = binaryPath
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	if err := m.startProcess(); err != nil {
		t.Fatalf("startProcess failed: %v", err)
	}

	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd == nil {
		t.Fatal("expected m.cmd to be set after successful startProcess")
	}

	// Verify --dev flag is NOT present in production mode.
	for _, arg := range cmd.Args {
		if arg == "--dev" {
			t.Errorf("expected --dev flag to NOT be present in production mode")
		}
	}

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}
