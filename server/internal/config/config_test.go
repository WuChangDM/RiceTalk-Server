package config

import (
	"os"
	"testing"
)

// chdirTo 切换进程工作目录到 dir，测试结束时恢复。
// 不用 t.Chdir：go1.27 Windows 上其 os.Open(".") 句柄恢复按相对 "." 重新解析必失败，
// CWD 滞留临时目录导致 TempDir 清理 RemoveAll 报 sharing violation（进程自身占用）。
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Errorf("恢复工作目录失败: %v", err)
		}
	})
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Env != "development" {
		t.Errorf("Env = %v, want development", cfg.Env)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %v, want 8080", cfg.Port)
	}
	if cfg.DBDriver != "sqlite" {
		t.Errorf("DBDriver = %v, want sqlite", cfg.DBDriver)
	}
	if cfg.JWTAccessTTL != 15 {
		t.Errorf("JWTAccessTTL = %v, want 15", cfg.JWTAccessTTL)
	}
	if cfg.BcryptCost != 12 {
		t.Errorf("BcryptCost = %v, want 12", cfg.BcryptCost)
	}
	if cfg.MaxUsers != 50 {
		t.Errorf("MaxUsers = %v, want 50", cfg.MaxUsers)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	// Save and restore env vars
	oldEnv := os.Getenv("RRT_ENV")
	oldPort := os.Getenv("RRT_PORT")
	oldDBDriver := os.Getenv("RRT_DB_DRIVER")
	defer func() {
		os.Setenv("RRT_ENV", oldEnv)
		os.Setenv("RRT_PORT", oldPort)
		os.Setenv("RRT_DB_DRIVER", oldDBDriver)
	}()

	os.Setenv("RRT_ENV", "test")
	os.Setenv("RRT_PORT", "9999")
	os.Setenv("RRT_DB_DRIVER", "postgres")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Env != "test" {
		t.Errorf("Env = %v, want test", cfg.Env)
	}
	if cfg.Port != 9999 {
		t.Errorf("Port = %v, want 9999", cfg.Port)
	}
	if cfg.DBDriver != "postgres" {
		t.Errorf("DBDriver = %v, want postgres", cfg.DBDriver)
	}
}

func TestLoad_DefaultsWhenNoConfigFile(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg should not be nil")
	}
	if cfg.ServerName != "RidgeRiceTalk" {
		t.Errorf("ServerName = %v, want RidgeRiceTalk", cfg.ServerName)
	}
}

func TestLoad_EnvPrecedenceOverEnvFile(t *testing.T) {
	// Process environment variables must win over .env file values.
	tmp := t.TempDir()
	chdirTo(t, tmp)

	if err := os.WriteFile(".env", []byte("RRT_PORT=7777\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	oldPort := os.Getenv("RRT_PORT")
	defer os.Setenv("RRT_PORT", oldPort)
	os.Setenv("RRT_PORT", "8888")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 8888 {
		t.Errorf("Port = %v, want 8888 (process env should override .env)", cfg.Port)
	}
}

func TestAppendAllowedOriginsFromPublicAddress_OnlyPublic(t *testing.T) {
	cfg := &Config{PublicAddress: "http://new-server:50100", Port: 50110, AdminPort: 50103}
	appendAllowedOriginsFromPublicAddress(cfg)
	// 应包含 PublicAddress
	if !contains(cfg.AllowedOrigins, "http://new-server:50100") {
		t.Errorf("expected AllowedOrigins to contain PublicAddress, got %v", cfg.AllowedOrigins)
	}
	// 应包含 localhost:Port
	if !contains(cfg.AllowedOrigins, "http://localhost:50110") {
		t.Errorf("expected AllowedOrigins to contain localhost:Port, got %v", cfg.AllowedOrigins)
	}
	// 应包含 localhost:AdminPort
	if !contains(cfg.AllowedOrigins, "http://localhost:50103") {
		t.Errorf("expected AllowedOrigins to contain localhost:AdminPort, got %v", cfg.AllowedOrigins)
	}
}

func TestAppendAllowedOriginsFromPublicAddress_Dedup(t *testing.T) {
	cfg := &Config{
		PublicAddress:   "http://<REDACTED-OLD-SERVER-IP>:50100",
		AllowedOrigins:  []string{"http://<REDACTED-OLD-SERVER-IP>:50100", "http://<REDACTED-OLD-SERVER-IP>:50103"},
		Port:             50110,
		AdminPort:        50103,
		AdminPortShared:  false,
	}
	appendAllowedOriginsFromPublicAddress(cfg)
	// PublicAddress 不应重复
	count := 0
	for _, o := range cfg.AllowedOrigins {
		if o == "http://<REDACTED-OLD-SERVER-IP>:50100" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected PublicAddress to appear once, got %d", count)
	}
}

func TestAppendAllowedOriginsFromPublicAddress_EmptyPublic(t *testing.T) {
	cfg := &Config{PublicAddress: "", Port: 50110, AdminPort: 50103}
	appendAllowedOriginsFromPublicAddress(cfg)
	// 空 PublicAddress 不追加，但 localhost 兜底仍追加
	if contains(cfg.AllowedOrigins, "") {
		t.Errorf("should not append empty string")
	}
	if !contains(cfg.AllowedOrigins, "http://localhost:50110") {
		t.Errorf("expected localhost fallback, got %v", cfg.AllowedOrigins)
	}
}

func TestAppendAllowedOriginsFromPublicAddress_AdminShared(t *testing.T) {
	cfg := &Config{PublicAddress: "http://srv:50100", Port: 50110, AdminPort: 50103, AdminPortShared: true}
	appendAllowedOriginsFromPublicAddress(cfg)
	// AdminPortShared=true 时不追加 localhost:AdminPort
	if contains(cfg.AllowedOrigins, "http://localhost:50103") {
		t.Errorf("should not append localhost:AdminPort when AdminPortShared, got %v", cfg.AllowedOrigins)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestIsAllowedOrigin(t *testing.T) {
	tests := []struct {
		name     string
		origin   string
		allowed  []string
		expected bool
	}{
		{
			name:     "exact match https with default port",
			origin:   "https://192.168.31.187",
			allowed:  []string{"https://192.168.31.187:443"},
			expected: true,
		},
		{
			name:     "exact match http with default port",
			origin:   "http://192.168.31.187",
			allowed:  []string{"http://192.168.31.187:80"},
			expected: true,
		},
		{
			name:     "no match different port",
			origin:   "https://192.168.31.187",
			allowed:  []string{"https://192.168.31.187:8443"},
			expected: false,
		},
		{
			name:     "exact match non-default port",
			origin:   "https://192.168.31.187:8443",
			allowed:  []string{"https://192.168.31.187:8443"},
			expected: true,
		},
		{
			name:     "empty origin matches nothing",
			origin:   "",
			allowed:  []string{"https://192.168.31.187:443"},
			expected: false,
		},
		{
			name:     "multiple allowed origins",
			origin:   "https://localhost",
			allowed:  []string{"https://192.168.31.187:443", "https://localhost:443"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAllowedOrigin(tt.origin, tt.allowed)
			if got != tt.expected {
				t.Errorf("IsAllowedOrigin(%q, %v) = %v, want %v", tt.origin, tt.allowed, got, tt.expected)
			}
		})
	}
}
