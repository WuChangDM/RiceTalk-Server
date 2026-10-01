package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Storage is the interface for file storage operations
type Storage interface {
	Put(key string, reader io.Reader, size int64, contentType string) (string, error)
	Get(key string) (io.ReadCloser, int64, string, error)
	Delete(key string) error
	Exists(key string) bool
	URL(key string) string
}

// localStorage implements Storage using the local filesystem
type localStorage struct {
	basePath string
	baseURL  string
}

// NewLocalStorage creates a new local file storage
func NewLocalStorage(basePath, baseURL string) (Storage, error) {
	if basePath == "" {
		basePath = "./storage/uploads"
	}
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	return &localStorage{
		basePath: basePath,
		baseURL:  baseURL,
	}, nil
}

func (s *localStorage) Put(key string, reader io.Reader, size int64, contentType string) (string, error) {
	// Sanitize key and create directory structure
	fullPath, err := s.resolvePath(key)
	if err != nil {
		return "", err
	}

	// Ensure parent directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}

	// Write file
	file, err := os.Create(fullPath)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, reader); err != nil {
		os.Remove(fullPath)
		return "", fmt.Errorf("write file: %w", err)
	}

	return key, nil
}

func (s *localStorage) Get(key string) (io.ReadCloser, int64, string, error) {
	fullPath, err := s.resolvePath(key)
	if err != nil {
		return nil, 0, "", err
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return nil, 0, "", fmt.Errorf("open file: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, "", fmt.Errorf("stat file: %w", err)
	}

	contentType := mime.TypeByExtension(filepath.Ext(key))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return file, info.Size(), contentType, nil
}

func (s *localStorage) Delete(key string) error {
	fullPath, err := s.resolvePath(key)
	if err != nil {
		return err
	}
	return os.Remove(fullPath)
}

func (s *localStorage) Exists(key string) bool {
	fullPath, err := s.resolvePath(key)
	if err != nil {
		return false
	}
	_, err = os.Stat(fullPath)
	return err == nil
}

func (s *localStorage) URL(key string) string {
	if s.baseURL == "" {
		return "/api/v1/files/" + key
	}
	return s.baseURL + "/" + key
}

// GenerateKey creates a unique storage key
func GenerateKey(prefix, filename string) string {
	// Generate a hash-based path for even distribution
	hash := sha256.Sum256([]byte(filename + time.Now().String()))
	hashStr := hex.EncodeToString(hash[:])[:8]
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("%s/%s/%s%s", prefix, hashStr[:2], hashStr[2:], ext)
}

// CalculateChecksum computes SHA-256 checksum of a reader
func CalculateChecksum(reader io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ChecksumWriter accumulates the SHA-256 of everything written through it, so
// an upload can hash its payload while it is still being streamed to disk
// (io.MultiWriter / io.TeeReader) instead of re-reading the file afterwards.
//
// Sum returns the same lowercase-hex digest as CalculateChecksum, so the two
// entry points are interchangeable.
type ChecksumWriter struct {
	h hash.Hash
}

// NewChecksumWriter returns a ready-to-use ChecksumWriter.
func NewChecksumWriter() *ChecksumWriter {
	return &ChecksumWriter{h: sha256.New()}
}

// Write implements io.Writer.
func (c *ChecksumWriter) Write(p []byte) (int, error) {
	return c.h.Write(p)
}

// Sum returns the lowercase hex SHA-256 of all bytes written so far.
func (c *ChecksumWriter) Sum() string {
	return hex.EncodeToString(c.h.Sum(nil))
}

// ValidateFileType checks file magic numbers
func ValidateFileType(reader io.Reader, expectedTypes ...string) (string, bool) {
	// Read first 512 bytes for magic number detection
	buf := make([]byte, 512)
	n, _ := reader.Read(buf)
	buf = buf[:n]

	// Simple magic number checks
	magicMap := map[string][][]byte{
		"image/jpeg":      {{0xFF, 0xD8, 0xFF}},
		"image/png":       {{0x89, 0x50, 0x4E, 0x47}},
		"image/gif":       {{0x47, 0x49, 0x46}, {0x47, 0x49, 0x46}},
		"image/webp":      {{0x52, 0x49, 0x46, 0x46}},
		"image/avif":      {{0x00, 0x00, 0x00}},
		"image/heic":      {{0x00, 0x00, 0x00}},
		"image/heif":      {{0x00, 0x00, 0x00}},
		"image/tiff":      {{0x49, 0x49, 0x2A, 0x00}, {0x4D, 0x4D, 0x00, 0x2A}},
		"image/x-icon":    {{0x00, 0x00, 0x01, 0x00}},
		"audio/mpeg":      {{0xFF, 0xFB}, {0xFF, 0xF3}, {0xFF, 0xF2}, {0x49, 0x44, 0x33}},
		"audio/wav":       {{0x52, 0x49, 0x46, 0x46}},
		"audio/ogg":       {{0x4F, 0x67, 0x67, 0x53}},
		"video/mp4":       {{0x00, 0x00, 0x00}, {0x66, 0x74, 0x79, 0x70}},
		"video/webm":      {{0x1A, 0x45, 0xDF, 0xA3}},
		"video/quicktime": {{0x00, 0x00, 0x00}},
		"video/x-msvideo": {{0x52, 0x49, 0x46, 0x46}},
		"video/x-matroska": {{0x1A, 0x45, 0xDF, 0xA3}},
		"video/x-flv":     {{0x46, 0x4C, 0x56}},
		"video/mp2t":      {{0x47}},
		"video/3gpp":      {{0x00, 0x00, 0x00}},
		"application/pdf": {{0x25, 0x50, 0x44, 0x46}},
		"application/zip": {{0x50, 0x4B, 0x03, 0x04}},
	}

	detectedType := ""
	for mimeType, magics := range magicMap {
		for _, magic := range magics {
			if len(buf) >= len(magic) {
				match := true
				for i, b := range magic {
					if buf[i] != b {
						match = false
						break
					}
				}
				if match {
					detectedType = mimeType
					break
				}
			}
		}
		if detectedType != "" {
			break
		}
	}

	// SVG check (text-based)
	if detectedType == "" {
		text := string(buf)
		if strings.Contains(text, "<?xml") && strings.Contains(text, "<svg") {
			detectedType = "image/svg+xml"
		}
	}

	// Plain text check: printable ASCII / UTF-8 text without binary nulls.
	// This is intentionally broad so that generic text uploads (logs, csv, json,
	// etc.) pass validation when "text/plain" is in the expected type list.
	if detectedType == "" {
		text := string(buf)
		isPrintable := true
		for i := 0; i < len(text); i++ {
			b := text[i]
			if b == 0 {
				isPrintable = false
				break
			}
			// Allow common whitespace and printable range; tolerate high bytes for UTF-8.
			if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
				isPrintable = false
				break
			}
		}
		if isPrintable {
			detectedType = "text/plain"
		}
	}

	if len(expectedTypes) == 0 {
		return detectedType, true
	}

	for _, t := range expectedTypes {
		if detectedType == t {
			return detectedType, true
		}
	}
	return detectedType, false
}

// SanitizeSVG removes potentially dangerous elements from SVG content
func SanitizeSVG(content string) string {
	// Simple sanitization: remove script tags and event handlers
	dangerous := []string{
		"<script", "</script>",
		"javascript:",
		"onload=", "onerror=", "onclick=",
		"onmouseover=", "onmouseout=",
	}
	for _, d := range dangerous {
		content = strings.ReplaceAll(content, d, "")
	}
	return content
}

func (s *localStorage) resolvePath(key string) (string, error) {
	key = filepath.Clean(key)
	key = strings.TrimPrefix(key, string(filepath.Separator))
	fullPath := filepath.Join(s.basePath, key)

	absBase, err := filepath.Abs(s.basePath)
	if err != nil {
		return "", fmt.Errorf("resolve base path: %w", err)
	}
	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve full path: %w", err)
	}

	// Ensure resolved path is within basePath
	if !strings.HasPrefix(absFull, absBase+string(filepath.Separator)) && absFull != absBase {
		return "", fmt.Errorf("invalid path: path traversal detected")
	}
	return fullPath, nil
}
