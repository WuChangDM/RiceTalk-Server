// Package config 中的 secrets_test.go 对 secrets.go 暴露的密钥生成、
// 持久化、加载与校验逻辑进行单元测试，覆盖正常路径与边界场景。
package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGenerateSecrets_Length 验证 GenerateSecrets 生成的各字段长度符合预期：
// 5 个 32 字节密钥 → 64 字符十六进制；LiveKitAPIKey 带固定前缀且 ≥ 16 字符；
// PostgresPassword 为 16 字节 → 32 字符十六进制。
func TestGenerateSecrets_Length(t *testing.T) {
	s := GenerateSecrets()
	if s == nil {
		t.Fatal("GenerateSecrets 返回 nil")
	}
	// 4 个 32 字节核心密钥应为 64 字符十六进制。
	if len(s.JWTSecret) != 64 {
		t.Errorf("JWTSecret length = %d, want 64", len(s.JWTSecret))
	}
	if len(s.CSRFTokenSecret) != 64 {
		t.Errorf("CSRFTokenSecret length = %d, want 64", len(s.CSRFTokenSecret))
	}
	if len(s.EncryptionKey) != 64 {
		t.Errorf("EncryptionKey length = %d, want 64", len(s.EncryptionKey))
	}
	if len(s.LiveKitAPISecret) != 64 {
		t.Errorf("LiveKitAPISecret length = %d, want 64", len(s.LiveKitAPISecret))
	}
	// LiveKitAPIKey 必须满足最小长度并带固定前缀。
	if len(s.LiveKitAPIKey) < 16 {
		t.Errorf("LiveKitAPIKey length = %d, want >= 16", len(s.LiveKitAPIKey))
	}
	if !strings.HasPrefix(s.LiveKitAPIKey, "ridgericetalk_") {
		t.Errorf("LiveKitAPIKey = %q, want prefix %q", s.LiveKitAPIKey, "ridgericetalk_")
	}
	// PostgresPassword 应为 32 字符十六进制。
	if len(s.PostgresPassword) != 32 {
		t.Errorf("PostgresPassword length = %d, want 32", len(s.PostgresPassword))
	}
}

// TestGenerateSecrets_Randomness 验证两次调用 GenerateSecrets 不会产生相同密钥，
// 确保 crypto/rand 提供足够的随机性。
func TestGenerateSecrets_Randomness(t *testing.T) {
	s1 := GenerateSecrets()
	s2 := GenerateSecrets()
	if s1.JWTSecret == s2.JWTSecret {
		t.Error("两次生成的 JWTSecret 相同，随机性不足")
	}
	if s1.CSRFTokenSecret == s2.CSRFTokenSecret {
		t.Error("两次生成的 CSRFTokenSecret 相同，随机性不足")
	}
	if s1.EncryptionKey == s2.EncryptionKey {
		t.Error("两次生成的 EncryptionKey 相同，随机性不足")
	}
	if s1.LiveKitAPIKey == s2.LiveKitAPIKey {
		t.Error("两次生成的 LiveKitAPIKey 相同，随机性不足")
	}
	if s1.LiveKitAPISecret == s2.LiveKitAPISecret {
		t.Error("两次生成的 LiveKitAPISecret 相同，随机性不足")
	}
	if s1.PostgresPassword == s2.PostgresPassword {
		t.Error("两次生成的 PostgresPassword 相同，随机性不足")
	}
}

// TestSaveAndLoadSecrets_RoundTrip 验证 SaveSecrets 写入后 LoadSecrets 能读回
// 完全一致的密钥内容，确保 JSON 序列化/反序列化无损。
func TestSaveAndLoadSecrets_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "secrets.json")
	original := GenerateSecrets()

	if err := SaveSecrets(path, original); err != nil {
		t.Fatalf("SaveSecrets 失败: %v", err)
	}
	loaded, err := LoadSecrets(path)
	if err != nil {
		t.Fatalf("LoadSecrets 失败: %v", err)
	}
	// 比较所有字段：直接比较结构体值。
	if *loaded != *original {
		t.Errorf("loaded != original\nloaded = %+v\noriginal = %+v", *loaded, *original)
	}
}

// TestSaveSecrets_FilePermission 验证 SaveSecrets 写入的文件权限为 0600，
// 避免密钥被同机其他用户读取。
func TestSaveSecrets_FilePermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 不表达 POSIX 权限位：os.WriteFile 的 0o600 只会映射为
		// 只读/可写属性，os.Stat().Mode().Perm() 对可写文件恒返回 0666。
		// 密钥文件的访问控制由父目录 ACL 决定，此处无法断言 0600。
		t.Skip("Windows 不强制 POSIX 权限位，无法断言 0600")
	}
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "secrets.json")
	s := GenerateSecrets()

	if err := SaveSecrets(path, s); err != nil {
		t.Fatalf("SaveSecrets 失败: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat 失败: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("文件权限 = %o, want 0600", got)
	}
}

// TestSaveSecrets_NilSecrets 验证传入 nil 时 SaveSecrets 返回错误，
// 避免空指针 panic。
func TestSaveSecrets_NilSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "secrets.json")
	if err := SaveSecrets(path, nil); err == nil {
		t.Error("传入 nil 时未返回错误")
	}
}

// TestLoadSecrets_NotExist 验证文件不存在时 LoadSecrets 返回包装了
// os.ErrNotExist 的错误，便于上层判断是否首次启动。
func TestLoadSecrets_NotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	_, err := LoadSecrets(path)
	if err == nil {
		t.Fatal("文件不存在时未返回错误")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("期望 errors.Is(err, os.ErrNotExist) 为真, got %v", err)
	}
}

// TestLoadSecrets_InvalidJSON 验证文件内容非法 JSON 时 LoadSecrets 返回错误，
// 且错误不应被误判为 os.ErrNotExist。
func TestLoadSecrets_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "secrets.json")
	if err := os.WriteFile(path, []byte("invalid json"), 0o600); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	_, err := LoadSecrets(path)
	if err == nil {
		t.Fatal("非法 JSON 未返回错误")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Error("非法 JSON 不应被判定为 ErrNotExist")
	}
}

// TestValidateSecrets_EmptyFields 验证全空字段会被 ValidateSecrets 拒绝。
func TestValidateSecrets_EmptyFields(t *testing.T) {
	s := &SecretsFile{}
	if err := ValidateSecrets(s); err == nil {
		t.Error("全空字段期望返回错误, 实际为 nil")
	}
}

// TestValidateSecrets_NilSecrets 验证传入 nil 指针时 ValidateSecrets 返回错误。
func TestValidateSecrets_NilSecrets(t *testing.T) {
	if err := ValidateSecrets(nil); err == nil {
		t.Error("nil 指针期望返回错误, 实际为 nil")
	}
}

// TestValidateSecrets_ShortSecrets 验证长度不足的密钥会被 ValidateSecrets 拒绝。
func TestValidateSecrets_ShortSecrets(t *testing.T) {
	s := &SecretsFile{
		JWTSecret:        "short",
		CSRFTokenSecret:  "short",
		EncryptionKey:    "short",
		LiveKitAPIKey:    "short",
		LiveKitAPISecret: "short",
		PostgresPassword: "short",
	}
	if err := ValidateSecrets(s); err == nil {
		t.Error("短密钥期望返回错误, 实际为 nil")
	}
}

// TestValidateSecrets_Valid 验证 GenerateSecrets 生成的合法密钥能通过 ValidateSecrets。
func TestValidateSecrets_Valid(t *testing.T) {
	s := GenerateSecrets()
	if err := ValidateSecrets(s); err != nil {
		t.Errorf("合法密钥未通过校验: %v", err)
	}
}
