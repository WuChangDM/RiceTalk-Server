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

func TestNewManager(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)
	if m == nil {
		t.Fatalf("New returned nil")
	}
	if m.started {
		t.Errorf("new manager should not be started")
	}
	if m.stopCh == nil {
		t.Errorf("stopCh should be initialized")
	}
}

func TestManagerStopWithoutStart(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)
	// Stop on an unstarted manager should be a safe no-op
	m.Stop()
	if m.started {
		t.Errorf("manager should remain unstarted after Stop")
	}
}

func TestDiscoverBinaryReturnsEmptyWhenNotFound(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	// Run in a clean temporary working directory with no livekit binary
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	// Clear PATH to prevent finding a system-installed livekit-server
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", tmpDir)

	result := m.discoverBinary()
	if result != "" {
		t.Errorf("expected empty binary path in clean dir, got %q", result)
	}
}

func TestGenerateConfig(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	tmpDir := t.TempDir()
	m.cfg = &config.Config{
		LiveKitPort:      7880,
		LiveKitAPIKey:    "testkey",
		LiveKitAPISecret: "testsecret",
		LocalDataPath:    tmpDir,
	}
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	// FIX-2026-0731-01: useExternalIP 默认 true（公网部署），测试中显式关闭
	// 以保证 use_external_ip: false 断言在所有环境下稳定
	t.Setenv("RRT_LIVEKIT_USE_EXTERNAL_IP", "false")

	if err := m.generateConfig(); err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		t.Fatalf("failed to read generated config: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "port: 7880") {
		t.Errorf("config missing port: 7880, got: %s", content)
	}
	if !strings.Contains(content, "testkey") {
		t.Errorf("config missing API key testkey, got: %s", content)
	}
	if !strings.Contains(content, "testsecret") {
		t.Errorf("config missing API secret testsecret, got: %s", content)
	}

	// ISSUE-082: generated config must configure the rtc section so that the
	// advertised media UDP port (7882) is actually listened on, and TCP 7881
	// remains as fallback.
	for _, want := range []string{"rtc:", "udp_port: 7882", "tcp_port: 7881", "use_ice_lite: true", "use_external_ip: false"} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %q, got: %s", want, content)
		}
	}

	// FIX-2026-0731-01 P1: room.enabled_codecs 必须显式声明，VP9 在首位
	// 避免运行时配置丢失导致 codec 协商回退到 VP8 软编码
	for _, want := range []string{"room:", "enabled_codecs:", "video/vp9", "video/vp8", "video/h264"} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %q, got: %s", want, content)
		}
	}
	// VP9 必须在 VP8 之前（codec 协商优先级）
	vp9Idx := strings.Index(content, "video/vp9")
	vp8Idx := strings.Index(content, "video/vp8")
	if vp9Idx < 0 || vp8Idx < 0 || vp9Idx > vp8Idx {
		t.Errorf("VP9 must appear before VP8 in enabled_codecs, got: %s", content)
	}
}

func TestGenerateConfigNodeIPEnvOverride(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	tmpDir := t.TempDir()
	m.cfg = &config.Config{
		LiveKitAPIKey:    "k",
		LiveKitAPISecret: "s",
		LocalDataPath:    tmpDir,
	}
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	t.Setenv("RRT_LIVEKIT_NODE_IP", "10.9.8.7")
	if err := m.generateConfig(); err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}
	data, _ := os.ReadFile(m.configPath)
	if !strings.Contains(string(data), "node_ip: 10.9.8.7") {
		t.Errorf("expected node_ip from env override, got: %s", string(data))
	}
}

func TestGenerateConfigWebhook(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	tmpDir := t.TempDir()
	m.cfg = &config.Config{
		Port:             5000,
		LiveKitPort:      7880,
		LiveKitAPIKey:    "testkey",
		LiveKitAPISecret: "testsecret",
		LocalDataPath:    tmpDir,
	}
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	if err := m.generateConfig(); err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	data, _ := os.ReadFile(m.configPath)
	content := string(data)
	// 服务端重启后客户端 LiveKit 自动重连不会重新走 HTTP join，webhook 是 DB
	// 参与者列表与 LiveKit 真实参与者对齐的关键。必须包含指向本服务 API 端口的
	// /api/livekit/webhook 地址。api_key 即顶层 keys 段的 LiveKit key。
	for _, want := range []string{"webhook:", "api_key: testkey", "http://127.0.0.1:5000/api/livekit/webhook"} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %q, got: %s", want, content)
		}
	}
	// LiveKit 的 webhook 段不支持 api_secret 字段（签名 secret 由顶层 keys 段
	// 该 key 对应的 secret 提供），生成配置绝不能写入，否则启动直接报
	// "field api_secret not found in type webhook.WebHookConfig"。
	if strings.Contains(content, "api_secret") {
		t.Errorf("webhook section must not contain api_secret, got: %s", content)
	}
}

func TestGenerateConfigNoWebhookWithoutAPIPort(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	tmpDir := t.TempDir()
	m.cfg = &config.Config{
		LiveKitAPIKey:    "k",
		LiveKitAPISecret: "s",
		LocalDataPath:    tmpDir,
	}
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	if err := m.generateConfig(); err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	data, _ := os.ReadFile(m.configPath)
	if strings.Contains(string(data), "webhook:") {
		t.Errorf("config should not contain webhook when no API port set, got: %s", string(data))
	}
}

func TestGenerateConfigDefaultPort(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	tmpDir := t.TempDir()
	m.cfg = &config.Config{
		// LiveKitPort is zero — should default to 7880
		LiveKitAPIKey:    "k",
		LiveKitAPISecret: "s",
		LocalDataPath:    tmpDir,
	}
	m.configPath = filepath.Join(tmpDir, "livekit.yaml")

	if err := m.generateConfig(); err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	data, _ := os.ReadFile(m.configPath)
	if !strings.Contains(string(data), "port: 7880") {
		t.Errorf("expected default port 7880, got: %s", string(data))
	}
}

func TestHealthCheckReturnsFalseWhenNoProcess(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	// Use an unlikely-to-be-open port
	m.cfg = &config.Config{LiveKitPort: 59999}

	if m.healthCheck() {
		t.Errorf("expected healthCheck to return false when no process is listening")
	}
}

func TestHealthCheckDefaultPort(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	// LiveKitPort = 0 should default to 7880; we only verify the function
	// does not panic and returns a bool.
	m.cfg = &config.Config{LiveKitPort: 0}
	_ = m.healthCheck()
}

func TestStartWithAutoStartDisabled(t *testing.T) {
	log := testutil.TestLogger()
	m := New(log)

	// Set env to disable auto-start
	origVal := os.Getenv("RRT_LIVEKIT_AUTOSTART")
	defer os.Setenv("RRT_LIVEKIT_AUTOSTART", origVal)
	os.Setenv("RRT_LIVEKIT_AUTOSTART", "false")

	cfg := &config.Config{
		LiveKitAPIKey:    "k",
		LiveKitAPISecret: "s",
		LocalDataPath:    t.TempDir(),
	}

	if err := m.Start(cfg); err != nil {
		t.Fatalf("Start with auto-start disabled should not error, got %v", err)
	}
	if m.started {
		t.Errorf("manager should not be started when auto-start is disabled")
	}
}

func TestStartWithMissingBinaryIsNonFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on Unix-style PATH manipulation")
	}

	log := testutil.TestLogger()
	m := New(log)

	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}

	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", tmpDir)

	// Clear the autostart env so Start proceeds to discovery
	origVal := os.Getenv("RRT_LIVEKIT_AUTOSTART")
	defer os.Setenv("RRT_LIVEKIT_AUTOSTART", origVal)
	os.Unsetenv("RRT_LIVEKIT_AUTOSTART")

	cfg := &config.Config{
		LiveKitAPIKey:     "k",
		LiveKitAPISecret:  "s",
		LocalDataPath:     tmpDir,
		LiveKitBinaryPath: "", // force discovery
	}

	// Missing binary should be non-fatal (returns nil, logs warning)
	if err := m.Start(cfg); err != nil {
		t.Errorf("Start with missing binary should be non-fatal, got error: %v", err)
	}
	if m.started {
		t.Errorf("manager should not be started when binary is missing")
	}
}
