// Package config 中的 secrets.go 负责生产环境密钥的自动生成与持久化。
// 设计目标：让新手用户无需手动生成密钥即可一键部署，首次启动时后端
// 自动生成所需的 5 个随机密钥并写入 secrets.json，后续启动从文件读取。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretsFile 持久化的生产密钥集合，对应 <LocalDataPath>/secrets.json 的结构。
type SecretsFile struct {
	JWTSecret        string `json:"jwt_secret"`
	CSRFTokenSecret  string `json:"csrf_token_secret"`
	EncryptionKey    string `json:"encryption_key"`
	LiveKitAPIKey    string `json:"livekit_api_key"`
	LiveKitAPISecret string `json:"livekit_api_secret"`
	PostgresPassword string `json:"postgres_password"`
}

// 长度常量，单位为字节；最终密文为十六进制编码后的字符数（每字节 2 字符）。
const (
	// secretBytes 生成 JWT/CSRF/Encryption/LiveKitAPISecret 等核心密钥所用的随机字节数。
	// 编码后得到 64 字符的十六进制字符串，满足 AES-256 等算法对密钥长度的要求。
	secretBytes = 32
	// livekitAPIKeyRandomBytes LiveKit API Key 中随机部分的字节数，编码后为 16 字符。
	livekitAPIKeyRandomBytes = 8
	// postgresPasswordBytes Postgres 密码所用的随机字节数，编码后为 32 字符。
	postgresPasswordBytes = 16

	// 下列常量为校验时各字段的最小字符长度。
	minSecretLength        = 64 // JWT/CSRF/Encryption/LiveKitAPISecret ≥ 64 字符
	minLiveKitAPIKeyLength = 16 // LiveKitAPIKey ≥ 16 字符
	minPostgresPwdLength   = 32 // PostgresPassword ≥ 32 字符

	// livekitAPIKeyPrefix LiveKit API Key 的固定前缀，便于辨识密钥来源。
	livekitAPIKeyPrefix = "ridgericetalk"
)

// GenerateSecrets 使用 crypto/rand 生成一组新的生产密钥。
// JWTSecret/CSRFTokenSecret/EncryptionKey/LiveKitAPISecret 为 32 字节随机数
// 的十六进制编码（64 字符）；LiveKitAPIKey 为 "ridgericetalk_" + 8 字节随机
// 十六进制（共 27 字符）；PostgresPassword 为 16 字节随机数的十六进制编码
// （32 字符）。返回值永远不会为 nil。
func GenerateSecrets() *SecretsFile {
	return &SecretsFile{
		JWTSecret:        randomHex(secretBytes),
		CSRFTokenSecret:  randomHex(secretBytes),
		EncryptionKey:    randomHex(secretBytes),
		LiveKitAPIKey:    livekitAPIKeyPrefix + "_" + randomHex(livekitAPIKeyRandomBytes),
		LiveKitAPISecret: randomHex(secretBytes),
		PostgresPassword: randomHex(postgresPasswordBytes),
	}
}

// LoadSecrets 从指定路径读取 secrets.json 并解析为 SecretsFile。
// 文件不存在时返回包装了 os.ErrNotExist 的错误，调用方可通过
// errors.Is(err, os.ErrNotExist) 判断以触发首次生成逻辑；
// 文件存在但 JSON 格式错误时返回包装错误；字段不完整时返回
// ValidateSecrets 的错误。
func LoadSecrets(path string) (*SecretsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// 保留 os.ErrNotExist 以便上层判断是否首次启动。
		return nil, fmt.Errorf("read secrets file %s: %w", path, err)
	}

	var s SecretsFile
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse secrets file %s: %w", path, err)
	}

	if err := ValidateSecrets(&s); err != nil {
		return nil, fmt.Errorf("validate secrets file %s: %w", path, err)
	}

	return &s, nil
}

// SaveSecrets 将 SecretsFile 以 JSON 格式（缩进 2 空格）写入指定路径，
// 文件权限 0600。若父目录不存在会通过 os.MkdirAll 创建。为保证写入的
// 原子性，先写入同目录下的临时文件再 rename 覆盖目标文件，避免半截写入
// 导致后续启动加载失败。
func SaveSecrets(path string, s *SecretsFile) error {
	if s == nil {
		return errors.New("save secrets: nil SecretsFile")
	}

	// 确保父目录存在（首次启动时 LocalDataPath 可能尚未创建）。
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create secrets dir %s: %w", dir, err)
		}
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}

	// 原子写入：先写临时文件再 rename，避免崩溃导致文件损坏。
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write secrets tmp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// rename 失败时尝试清理临时文件，避免残留。
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename secrets file %s -> %s: %w", tmpPath, path, err)
	}

	return nil
}

// ValidateSecrets 校验 SecretsFile 的所有字段非空且长度满足最小要求。
// 失败时返回 fmt.Errorf，在错误信息中列出所有不合规的字段名，便于
// 运维人员定位是哪个密钥缺失或过短。校验规则：
//   - JWTSecret/CSRFTokenSecret/EncryptionKey/LiveKitAPISecret 长度 ≥ 64 字符
//   - LiveKitAPIKey 长度 ≥ 16 字符
//   - PostgresPassword 长度 ≥ 32 字符
func ValidateSecrets(s *SecretsFile) error {
	if s == nil {
		return errors.New("secrets is nil")
	}

	var missing []string // 缺失或为空的字段
	var tooShort []string // 长度不足的字段

	// 长密钥字段：要求 ≥ 64 字符。
	longFields := []struct {
		name string
		val  string
	}{
		{"jwt_secret", s.JWTSecret},
		{"csrf_token_secret", s.CSRFTokenSecret},
		{"encryption_key", s.EncryptionKey},
		{"livekit_api_secret", s.LiveKitAPISecret},
	}
	for _, f := range longFields {
		if f.val == "" {
			missing = append(missing, f.name)
		} else if len(f.val) < minSecretLength {
			tooShort = append(tooShort, fmt.Sprintf("%s(len=%d,需要>=%d)", f.name, len(f.val), minSecretLength))
		}
	}

	// LiveKitAPIKey：要求 ≥ 16 字符。
	if s.LiveKitAPIKey == "" {
		missing = append(missing, "livekit_api_key")
	} else if len(s.LiveKitAPIKey) < minLiveKitAPIKeyLength {
		tooShort = append(tooShort, fmt.Sprintf("livekit_api_key(len=%d,需要>=%d)", len(s.LiveKitAPIKey), minLiveKitAPIKeyLength))
	}

	// PostgresPassword：要求 ≥ 32 字符。
	if s.PostgresPassword == "" {
		missing = append(missing, "postgres_password")
	} else if len(s.PostgresPassword) < minPostgresPwdLength {
		tooShort = append(tooShort, fmt.Sprintf("postgres_password(len=%d,需要>=%d)", len(s.PostgresPassword), minPostgresPwdLength))
	}

	if len(missing) == 0 && len(tooShort) == 0 {
		return nil
	}

	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "缺失字段: "+strings.Join(missing, ", "))
	}
	if len(tooShort) > 0 {
		parts = append(parts, "长度不足字段: "+strings.Join(tooShort, ", "))
	}
	return errors.New(strings.Join(parts, "; "))
}

// randomHex 生成 n 字节的 cryptographically secure 随机数并返回其十六进制编码。
// 若 crypto/rand 失败（极少发生，通常为系统熵池枯竭），返回空字符串；
// 调用方应通过 ValidateSecrets 在持久化前进行兜底校验。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
