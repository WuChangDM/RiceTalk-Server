package serverstate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"ridgericetalk/internal/config"
)

func TestDefaultNetworkConfig(t *testing.T) {
	nc := DefaultNetworkConfig()
	if nc.ExternalHTTPPort != 443 {
		t.Fatalf("expected HTTP port 443, got %d", nc.ExternalHTTPPort)
	}
	if nc.ExternalMediaUDPPort != 7882 {
		t.Fatalf("expected UDP port 7882, got %d", nc.ExternalMediaUDPPort)
	}
	if nc.ExternalAdminPort != 9090 {
		t.Fatalf("expected admin port 9090, got %d", nc.ExternalAdminPort)
	}
	if nc.ExternalLiveKitTCPPort != 7881 {
		t.Fatalf("expected LiveKit TCP port 7881, got %d", nc.ExternalLiveKitTCPPort)
	}
	if nc.WebVoiceEnabled {
		t.Fatal("expected WebVoiceEnabled to be false by default")
	}
}

func TestDefaultNetworkConfig_EnvOverride(t *testing.T) {
	t.Setenv("EXTERNAL_ADMIN_PORT", "19090")
	t.Setenv("EXTERNAL_LIVEKIT_TCP_PORT", "17881")
	nc := DefaultNetworkConfig()
	if nc.ExternalAdminPort != 19090 {
		t.Fatalf("expected env-overridden admin port 19090, got %d", nc.ExternalAdminPort)
	}
	if nc.ExternalLiveKitTCPPort != 17881 {
		t.Fatalf("expected env-overridden LiveKit TCP port 17881, got %d", nc.ExternalLiveKitTCPPort)
	}
}

func TestNetworkURLs(t *testing.T) {
	nc := NetworkConfig{
		ExternalHost:          "voice.example.com",
		ExternalHTTPPort:      443,
		ExternalLiveKitWSPort: 443,
		ExternalMediaUDPPort:  7882,
		UseHTTPS:              true,
	}
	if nc.ServerURL() != "https://voice.example.com" {
		t.Fatalf("unexpected server URL: %s", nc.ServerURL())
	}
	if nc.LiveKitURL() != "wss://voice.example.com/livekit" {
		t.Fatalf("unexpected livekit URL: %s", nc.LiveKitURL())
	}

	nc.ExternalHTTPPort = 8080
	if nc.ServerURL() != "https://voice.example.com:8080" {
		t.Fatalf("unexpected server URL with port: %s", nc.ServerURL())
	}
}

func TestManagerLifecycle(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.LocalDataPath = tmp

	m, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if m.CurrentState() != StateUninitialized {
		t.Fatalf("expected uninitialized, got %s", m.CurrentState())
	}

	network := NetworkConfig{
		ExternalHost:           "example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
		UseHTTPS:               true,
		WebVoiceEnabled:        true,
		ClientAccessEnabled:    true,
	}
	if err := m.CompleteBootstrap(network); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if m.CurrentState() != StateClientAccessReady {
		t.Fatalf("expected client access ready, got %s", m.CurrentState())
	}

	// Reload from disk.
	m2, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	if m2.CurrentState() != StateClientAccessReady {
		t.Fatalf("expected persisted state, got %s", m2.CurrentState())
	}
	if m2.Network().ExternalHost != "example.com" {
		t.Fatalf("unexpected host: %s", m2.Network().ExternalHost)
	}
}

func TestManagerWebVoiceToggle(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.LocalDataPath = tmp

	m, _ := NewManager(cfg)
	network := NetworkConfig{
		ExternalHost:           "example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
		WebVoiceEnabled:        false,
		ClientAccessEnabled:    true,
	}
	if err := m.CompleteBootstrap(network); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if m.CurrentState() != StateWebVoiceLocked {
		t.Fatalf("expected locked, got %s", m.CurrentState())
	}

	network.WebVoiceEnabled = true
	if err := m.UpdateNetwork(network); err != nil {
		t.Fatalf("update network: %v", err)
	}
	if m.CurrentState() != StateClientAccessReady {
		t.Fatalf("expected ready, got %s", m.CurrentState())
	}
}

func TestBootstrapRequiresHost(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.LocalDataPath = tmp

	m, _ := NewManager(cfg)
	if err := m.CompleteBootstrap(NetworkConfig{}); err == nil {
		t.Fatal("expected error for empty host")
	}
}

func TestValidateNetwork(t *testing.T) {
	if err := validateNetwork(NetworkConfig{ExternalHost: "x", ExternalHTTPPort: 0}); err == nil {
		t.Fatal("expected error for invalid port")
	}
	if err := validateNetwork(NetworkConfig{ExternalHost: "x", ExternalHTTPPort: 443, ExternalLiveKitWSPort: 443, ExternalMediaUDPPort: 7882, ExternalAdminPort: 9090, ExternalLiveKitTCPPort: 7881}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// New fields: ExternalAdminPort out of range (0 / 70000) must fail.
	if err := validateNetwork(NetworkConfig{ExternalHost: "x", ExternalHTTPPort: 443, ExternalLiveKitWSPort: 443, ExternalMediaUDPPort: 7882, ExternalAdminPort: 0, ExternalLiveKitTCPPort: 7881}); err == nil {
		t.Fatal("expected error for invalid external admin port 0")
	}
	if err := validateNetwork(NetworkConfig{ExternalHost: "x", ExternalHTTPPort: 443, ExternalLiveKitWSPort: 443, ExternalMediaUDPPort: 7882, ExternalAdminPort: 70000, ExternalLiveKitTCPPort: 7881}); err == nil {
		t.Fatal("expected error for invalid external admin port 70000")
	}
	// New fields: ExternalLiveKitTCPPort out of range (0 / 70000) must fail.
	if err := validateNetwork(NetworkConfig{ExternalHost: "x", ExternalHTTPPort: 443, ExternalLiveKitWSPort: 443, ExternalMediaUDPPort: 7882, ExternalAdminPort: 9090, ExternalLiveKitTCPPort: 0}); err == nil {
		t.Fatal("expected error for invalid external LiveKit TCP port 0")
	}
	if err := validateNetwork(NetworkConfig{ExternalHost: "x", ExternalHTTPPort: 443, ExternalLiveKitWSPort: 443, ExternalMediaUDPPort: 7882, ExternalAdminPort: 9090, ExternalLiveKitTCPPort: 70000}); err == nil {
		t.Fatal("expected error for invalid external LiveKit TCP port 70000")
	}
}

func TestBuildInfoResponse(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.LocalDataPath = tmp
	cfg.ServerName = "Test Server"

	m, _ := NewManager(cfg)
	m.CompleteBootstrap(NetworkConfig{
		ExternalHost:           "example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
		UseHTTPS:               true,
		WebVoiceEnabled:        true,
	})
	info := m.BuildInfoResponse("1.0.0")
	if info.Name != "Test Server" {
		t.Fatalf("unexpected name: %s", info.Name)
	}
	if info.ServerURL != "https://example.com" {
		t.Fatalf("unexpected server URL: %s", info.ServerURL)
	}
	if !info.WebVoiceEnabled {
		t.Fatal("expected web voice enabled")
	}
}

func TestBuildNetworkResponse(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.LocalDataPath = tmp

	m, _ := NewManager(cfg)
	m.CompleteBootstrap(NetworkConfig{
		ExternalHost:           "example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
		UseHTTPS:               true,
	})
	resp := m.BuildNetworkResponse()
	if len(resp.SuggestedSRVRecords) != 3 {
		t.Fatalf("expected 3 SRV records, got %d", len(resp.SuggestedSRVRecords))
	}
}

func TestStateFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 不表达 POSIX 权限位：os.WriteFile 的 0o600 只会映射为
		// 只读/可写属性，os.Stat().Mode().Perm() 对可写文件恒返回 0666。
		// 状态文件的访问控制由父目录 ACL 决定，此处无法断言 0600。
		t.Skip("Windows 不强制 POSIX 权限位，无法断言 0600")
	}
	tmp := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.LocalDataPath = tmp

	m, _ := NewManager(cfg)
	m.CompleteBootstrap(NetworkConfig{
		ExternalHost:           "example.com",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
	})

	stateFile := filepath.Join(tmp, "server-state.json")
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected state file mode 0600, got %o", info.Mode().Perm())
	}
}
