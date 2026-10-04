// Package config 中的 secrets_integration_test.go 对 loadOrGenerateProductionSecrets
// 进行集成测试，覆盖生产环境首次启动、重启加载、环境变量优先、密钥不完整等场景。
//
// 这些测试以白盒方式（package config）访问未导出的 loadOrGenerateProductionSecrets，
// 验证 secrets.json 自动生成/加载、livekit.yaml 渲染、环境变量优先级等端到端流程。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretEnvKeys 生产密钥相关的环境变量名列表，用于测试前清理残留。
var secretEnvKeys = []string{
	"RRT_JWT_SECRET",
	"RRT_CSRF_TOKEN_SECRET",
	"RRT_ENCRYPTION_KEY",
	"RRT_LIVEKIT_API_KEY",
	"RRT_LIVEKIT_API_SECRET",
}

// unsetSecretEnvVars 清空所有生产密钥相关的环境变量，避免测试环境残留干扰。
func unsetSecretEnvVars(t *testing.T) {
	t.Helper()
	for _, key := range secretEnvKeys {
		os.Unsetenv(key)
	}
}

// writeLiveKitTemplate 在 dir 下创建 livekit/livekit.yaml.template 模板文件，
// 供 loadOrGenerateProductionSecrets 渲染 livekit.yaml 使用。
// loadOrGenerateProductionSecrets 内部使用相对路径 livekit/livekit.yaml.template，
// 因此测试需配合 t.Chdir(dir) 切换工作目录使相对路径可解析。
func writeLiveKitTemplate(t *testing.T, dir string) {
	t.Helper()
	templateDir := filepath.Join(dir, "livekit")
	if err := os.MkdirAll(templateDir, 0o700); err != nil {
		t.Fatalf("创建模板目录失败: %v", err)
	}
	template := `port: {{WS_PORT}}
prometheus_port: {{PROM_PORT}}
keys:
  {{LIVEKIT_API_KEY}}: {{LIVEKIT_API_SECRET}}
rtc:
  node_ip: {{NODE_IP}}
  udp_port: {{UDP_PORT}}
`
	templatePath := filepath.Join(templateDir, "livekit.yaml.template")
	if err := os.WriteFile(templatePath, []byte(template), 0o600); err != nil {
		t.Fatalf("写入模板文件失败: %v", err)
	}
}

// TestIntegration_ProductionFirstBoot_GeneratesSecrets 测试生产环境首次启动：
// 环境变量为空 → loadOrGenerateProductionSecrets 自动生成 secrets.json 并渲染 livekit.yaml。
func TestIntegration_ProductionFirstBoot_GeneratesSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	unsetSecretEnvVars(t)

	// 创建 livekit 模板并切换工作目录，使相对路径 livekit/livekit.yaml.template 可解析。
	writeLiveKitTemplate(t, tmpDir)
	chdirTo(t, tmpDir)

	cfg := &Config{
		Env:           "production",
		LocalDataPath: tmpDir,
	}

	err := loadOrGenerateProductionSecrets(cfg)
	if err != nil {
		t.Fatalf("loadOrGenerateProductionSecrets 失败: %v", err)
	}

	// 验证 5 个核心密钥已填充
	if cfg.JWTSecret == "" {
		t.Error("JWTSecret 未填充")
	}
	if cfg.CSRFTokenSecret == "" {
		t.Error("CSRFTokenSecret 未填充")
	}
	if cfg.EncryptionKey == "" {
		t.Error("EncryptionKey 未填充")
	}
	if cfg.LiveKitAPIKey == "" {
		t.Error("LiveKitAPIKey 未填充")
	}
	if cfg.LiveKitAPISecret == "" {
		t.Error("LiveKitAPISecret 未填充")
	}

	// 验证 secrets.json 已生成
	secretsPath := filepath.Join(tmpDir, "secrets.json")
	if _, err := os.Stat(secretsPath); os.IsNotExist(err) {
		t.Error("secrets.json 未生成")
	}

	// 验证 livekit.yaml 已渲染
	livekitPath := filepath.Join(tmpDir, "livekit.yaml")
	if _, err := os.Stat(livekitPath); os.IsNotExist(err) {
		t.Fatal("livekit.yaml 未渲染")
	}

	// 验证 livekit.yaml 包含 LiveKitAPISecret
	content, err := os.ReadFile(livekitPath)
	if err != nil {
		t.Fatalf("读取 livekit.yaml 失败: %v", err)
	}
	if !strings.Contains(string(content), cfg.LiveKitAPISecret) {
		t.Error("livekit.yaml 不包含 LiveKitAPISecret")
	}
}

// TestIntegration_ProductionRestart_LoadsFromSecretsFile 测试生产环境重启：
// secrets.json 已存在 → 从文件加载密钥，不重新生成，文件内容不被覆盖。
func TestIntegration_ProductionRestart_LoadsFromSecretsFile(t *testing.T) {
	tmpDir := t.TempDir()
	unsetSecretEnvVars(t)

	// 预先生成 secrets.json（模拟首次启动后的状态）
	original := GenerateSecrets()
	secretsPath := filepath.Join(tmpDir, "secrets.json")
	if err := SaveSecrets(secretsPath, original); err != nil {
		t.Fatalf("SaveSecrets 失败: %v", err)
	}

	cfg := &Config{
		Env:           "production",
		LocalDataPath: tmpDir,
	}

	err := loadOrGenerateProductionSecrets(cfg)
	if err != nil {
		t.Fatalf("loadOrGenerateProductionSecrets 失败: %v", err)
	}

	// 验证密钥与原 secrets.json 一致（未被重新生成）
	if cfg.JWTSecret != original.JWTSecret {
		t.Errorf("JWTSecret 不一致: got %q, want %q", cfg.JWTSecret, original.JWTSecret)
	}
	if cfg.CSRFTokenSecret != original.CSRFTokenSecret {
		t.Errorf("CSRFTokenSecret 不一致: got %q, want %q", cfg.CSRFTokenSecret, original.CSRFTokenSecret)
	}
	if cfg.EncryptionKey != original.EncryptionKey {
		t.Errorf("EncryptionKey 不一致: got %q, want %q", cfg.EncryptionKey, original.EncryptionKey)
	}
	if cfg.LiveKitAPIKey != original.LiveKitAPIKey {
		t.Errorf("LiveKitAPIKey 不一致: got %q, want %q", cfg.LiveKitAPIKey, original.LiveKitAPIKey)
	}
	if cfg.LiveKitAPISecret != original.LiveKitAPISecret {
		t.Errorf("LiveKitAPISecret 不一致: got %q, want %q", cfg.LiveKitAPISecret, original.LiveKitAPISecret)
	}

	// 验证 secrets.json 未被覆盖（重新加载应与原值一致）
	loaded, err := LoadSecrets(secretsPath)
	if err != nil {
		t.Fatalf("LoadSecrets 失败: %v", err)
	}
	if loaded.JWTSecret != original.JWTSecret {
		t.Error("secrets.json 被覆盖")
	}
	if loaded.LiveKitAPISecret != original.LiveKitAPISecret {
		t.Error("secrets.json 中的 LiveKitAPISecret 被覆盖")
	}
}

// TestIntegration_EnvVarPriority_SkipsSecretsFile 测试环境变量优先：
// 5 个核心密钥均通过环境变量提供 → loadOrGenerateProductionSecrets 直接返回，
// 不读取也不生成 secrets.json，保留环境变量的值不被覆盖。
func TestIntegration_EnvVarPriority_SkipsSecretsFile(t *testing.T) {
	tmpDir := t.TempDir()

	// 设置环境变量（t.Setenv 会在测试结束后自动恢复原值）
	t.Setenv("RRT_JWT_SECRET", "env_var_jwt_secret_value")
	t.Setenv("RRT_CSRF_TOKEN_SECRET", "env_var_csrf_secret_value")
	t.Setenv("RRT_ENCRYPTION_KEY", "env_var_encryption_key_value")
	t.Setenv("RRT_LIVEKIT_API_KEY", "env_var_livekit_key")
	t.Setenv("RRT_LIVEKIT_API_SECRET", "env_var_livekit_secret_value")

	cfg := &Config{
		Env:           "production",
		LocalDataPath: tmpDir,
	}
	// 模拟 LoadConfig 中环境变量加载到 cfg 字段的过程
	// （loadOrGenerateProductionSecrets 只检查 cfg 字段，不直接读环境变量）
	cfg.JWTSecret = os.Getenv("RRT_JWT_SECRET")
	cfg.CSRFTokenSecret = os.Getenv("RRT_CSRF_TOKEN_SECRET")
	cfg.EncryptionKey = os.Getenv("RRT_ENCRYPTION_KEY")
	cfg.LiveKitAPIKey = os.Getenv("RRT_LIVEKIT_API_KEY")
	cfg.LiveKitAPISecret = os.Getenv("RRT_LIVEKIT_API_SECRET")

	err := loadOrGenerateProductionSecrets(cfg)
	if err != nil {
		t.Fatalf("loadOrGenerateProductionSecrets 失败: %v", err)
	}

	// 验证密钥未被覆盖（仍为环境变量值）
	if cfg.JWTSecret != "env_var_jwt_secret_value" {
		t.Errorf("环境变量 JWTSecret 被覆盖: got %q", cfg.JWTSecret)
	}
	if cfg.CSRFTokenSecret != "env_var_csrf_secret_value" {
		t.Errorf("环境变量 CSRFTokenSecret 被覆盖: got %q", cfg.CSRFTokenSecret)
	}
	if cfg.EncryptionKey != "env_var_encryption_key_value" {
		t.Errorf("环境变量 EncryptionKey 被覆盖: got %q", cfg.EncryptionKey)
	}
	if cfg.LiveKitAPIKey != "env_var_livekit_key" {
		t.Errorf("环境变量 LiveKitAPIKey 被覆盖: got %q", cfg.LiveKitAPIKey)
	}
	if cfg.LiveKitAPISecret != "env_var_livekit_secret_value" {
		t.Errorf("环境变量 LiveKitAPISecret 被覆盖: got %q", cfg.LiveKitAPISecret)
	}

	// 验证 secrets.json 未生成（环境变量已提供全部密钥时应跳过文件操作）
	secretsPath := filepath.Join(tmpDir, "secrets.json")
	if _, err := os.Stat(secretsPath); !os.IsNotExist(err) {
		t.Error("环境变量已设置时不应生成 secrets.json")
	}
}

// TestIntegration_IncompleteSecrets_ReturnsError 测试密钥不完整：
// secrets.json 缺少 LiveKitAPISecret → LoadSecrets 校验失败 → loadOrGenerateProductionSecrets 报错退出。
func TestIntegration_IncompleteSecrets_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	unsetSecretEnvVars(t)

	// 构造不完整的 secrets.json（缺 LiveKitAPISecret）
	full := GenerateSecrets()
	incomplete := &SecretsFile{
		JWTSecret:        full.JWTSecret,
		CSRFTokenSecret:  full.CSRFTokenSecret,
		EncryptionKey:    full.EncryptionKey,
		LiveKitAPIKey:    full.LiveKitAPIKey,
		LiveKitAPISecret: "", // 缺失
		PostgresPassword: full.PostgresPassword,
	}
	// 直接用 os.WriteFile 写入原始 JSON，明确测试 LoadSecrets 的校验逻辑
	rawData, err := json.Marshal(incomplete)
	if err != nil {
		t.Fatalf("json.Marshal 失败: %v", err)
	}
	secretsPath := filepath.Join(tmpDir, "secrets.json")
	if err := os.WriteFile(secretsPath, rawData, 0o600); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	cfg := &Config{
		Env:           "production",
		LocalDataPath: tmpDir,
	}

	err = loadOrGenerateProductionSecrets(cfg)
	if err == nil {
		t.Error("期望返回错误（密钥不完整），实际为 nil")
	}
}
