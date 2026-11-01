// Package thirdparty downloads and installs embedded dependencies
// (LiveKit, FFmpeg, etc.) so the server can run without requiring
// users to install them manually.
package thirdparty

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"ridgericetalk/internal/config"
)

// Versions holds pinned dependency versions and download URLs.
type Versions struct {
	LiveKit LiveKitVersions `toml:"livekit"`
	FFmpeg  FFmpegVersions  `toml:"ffmpeg"`
}

// LiveKitVersions holds LiveKit download metadata.
type LiveKitVersions struct {
	Version  string                       `toml:"version"`
	Binaries map[string]map[string]Binary `toml:"binaries"`
}

// FFmpegVersions holds FFmpeg download metadata.
type FFmpegVersions struct {
	Version  string                       `toml:"version"`
	Binaries map[string]map[string]Binary `toml:"binaries"`
}

// Binary describes a single downloadable artifact for an OS/architecture.
type Binary struct {
	URL     string `toml:"url"`
	Archive string `toml:"archive"`
}

// LoadVersions parses the embedded dependency manifest.
func LoadVersions(data string) (*Versions, error) {
	var v Versions
	if err := toml.Unmarshal([]byte(data), &v); err != nil {
		return nil, fmt.Errorf("parse versions manifest: %w", err)
	}
	return &v, nil
}

// PlatformKey returns the canonical OS_ARCH key used in the manifest.
func PlatformKey() string {
	return fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
}

// ResolveFFmpeg resolves the ffmpeg and ffprobe executable paths for the given
// configuration. It honors explicit paths, then PATH, and finally downloads the
// pinned binaries when embedded dependency management is enabled.
//
// ctx 用于约束内嵌二进制下载（见 downloadFile）：调用方必须传入可取消的上下文，
// 进程退出/启动被中断时下载会及时停止，避免留下无上限的挂起请求。
func ResolveFFmpeg(ctx context.Context, cfg *config.Config, log func(string, ...any)) error {
	if cfg.FFmpegBinaryPath != "" && cfg.FFprobeBinaryPath != "" {
		if exists(cfg.FFmpegBinaryPath) && exists(cfg.FFprobeBinaryPath) {
			return nil
		}
	}

	// Try PATH first.
	if cfg.FFmpegBinaryPath == "" {
		if path, err := exec.LookPath("ffmpeg"); err == nil {
			cfg.FFmpegBinaryPath = path
		}
	}
	if cfg.FFprobeBinaryPath == "" {
		if path, err := exec.LookPath("ffprobe"); err == nil {
			cfg.FFprobeBinaryPath = path
		}
	}

	if cfg.FFmpegBinaryPath != "" && cfg.FFprobeBinaryPath != "" {
		return nil
	}

	if !cfg.EmbeddedDeps {
		return fmt.Errorf("ffmpeg/ffprobe not found in PATH and embedded deps disabled")
	}

	versions, err := LoadVersions(config.EmbeddedVersionsTOML)
	if err != nil {
		return fmt.Errorf("load embedded versions: %w", err)
	}

	binDir, err := EnsureFFmpeg(ctx, versions, cfg.ThirdPartyDir, log)
	if err != nil {
		return fmt.Errorf("ensure ffmpeg: %w", err)
	}

	ffmpegName := "ffmpeg"
	ffprobeName := "ffprobe"
	if runtime.GOOS == "windows" {
		ffmpegName = "ffmpeg.exe"
		ffprobeName = "ffprobe.exe"
	}
	if cfg.FFmpegBinaryPath == "" {
		cfg.FFmpegBinaryPath = filepath.Join(binDir, ffmpegName)
	}
	if cfg.FFprobeBinaryPath == "" {
		cfg.FFprobeBinaryPath = filepath.Join(binDir, ffprobeName)
	}
	return nil
}

// EnsureLiveKit ensures the LiveKit server binary exists at the given path,
// downloading it if necessary.
func EnsureLiveKit(ctx context.Context, versions *Versions, installDir string, log func(string, ...any)) (string, error) {
	exeName := "livekit-server"
	if runtime.GOOS == "windows" {
		exeName = "livekit-server.exe"
	}
	binPath := filepath.Join(installDir, "livekit", exeName)

	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}

	platform := PlatformKey()
	binary, ok := versions.LiveKit.Binaries["livekit-server"][platform]
	if !ok {
		return "", fmt.Errorf("no LiveKit binary for platform %s", platform)
	}

	log("[thirdparty] LiveKit binary not found, downloading from %s", binary.URL)
	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return "", fmt.Errorf("create livekit install dir: %w", err)
	}

	tmpFile, err := downloadFile(ctx, binary.URL, log)
	if err != nil {
		return "", fmt.Errorf("download LiveKit: %w", err)
	}
	defer os.Remove(tmpFile)

	extractDir := filepath.Join(installDir, "livekit", "_extract")
	if err := os.RemoveAll(extractDir); err != nil {
		return "", fmt.Errorf("clean extract dir: %w", err)
	}
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return "", fmt.Errorf("create extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := extractArchive(tmpFile, binary.Archive, extractDir); err != nil {
		return "", fmt.Errorf("extract LiveKit: %w", err)
	}

	// Find the binary inside the extracted contents
	found, err := findBinary(extractDir, exeName)
	if err != nil {
		return "", fmt.Errorf("locate LiveKit binary: %w", err)
	}

	if err := os.Rename(found, binPath); err != nil {
		return "", fmt.Errorf("install LiveKit binary: %w", err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(binPath, 0755); err != nil {
			return "", fmt.Errorf("chmod LiveKit binary: %w", err)
		}
	}

	log("[thirdparty] LiveKit installed at %s", binPath)
	return binPath, nil
}

// EnsureFFmpeg ensures FFmpeg and ffprobe binaries exist, downloading them if
// necessary. It returns the directory containing the binaries.
func EnsureFFmpeg(ctx context.Context, versions *Versions, installDir string, log func(string, ...any)) (string, error) {
	binDir := filepath.Join(installDir, "ffmpeg", "bin")

	ffmpegName := "ffmpeg"
	ffprobeName := "ffprobe"
	if runtime.GOOS == "windows" {
		ffmpegName = "ffmpeg.exe"
		ffprobeName = "ffprobe.exe"
	}
	ffmpegPath := filepath.Join(binDir, ffmpegName)
	ffprobePath := filepath.Join(binDir, ffprobeName)

	if exists(ffmpegPath) && exists(ffprobePath) {
		return binDir, nil
	}

	platform := PlatformKey()
	ffmpegBinary, ok := versions.FFmpeg.Binaries["ffmpeg"][platform]
	if !ok {
		return "", fmt.Errorf("no FFmpeg binary for platform %s", platform)
	}
	ffprobeBinary := versions.FFmpeg.Binaries["ffprobe"][platform]

	log("[thirdparty] FFmpeg not found, downloading for %s", platform)

	extractDir := filepath.Join(installDir, "ffmpeg", "_extract")
	if err := os.RemoveAll(extractDir); err != nil {
		return "", fmt.Errorf("clean ffmpeg extract dir: %w", err)
	}
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return "", fmt.Errorf("create ffmpeg extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	// ffmpeg and ffprobe usually ship in the same archive. Download once and
	// reuse the extracted contents when the URLs match.
	archives := []struct {
		name    string
		binary  Binary
		extract bool
	}{
		{"ffmpeg", ffmpegBinary, !exists(ffmpegPath)},
		{"ffprobe", ffprobeBinary, !exists(ffprobePath)},
	}

	downloaded := make(map[string]string)
	for _, a := range archives {
		if !a.extract {
			continue
		}
		tmpFile, ok := downloaded[a.binary.URL]
		if !ok {
			var err error
			tmpFile, err = downloadFile(ctx, a.binary.URL, log)
			if err != nil {
				return "", fmt.Errorf("download %s: %w", a.name, err)
			}
			downloaded[a.binary.URL] = tmpFile
			defer os.Remove(tmpFile)
		}

		if err := extractArchive(tmpFile, a.binary.Archive, extractDir); err != nil {
			return "", fmt.Errorf("extract %s: %w", a.name, err)
		}
	}

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("create ffmpeg bin dir: %w", err)
	}

	for _, name := range []string{"ffmpeg", "ffprobe"} {
		srcName := name
		dstName := name
		if runtime.GOOS == "windows" {
			srcName = name + ".exe"
			dstName = name + ".exe"
		}
		dst := filepath.Join(binDir, dstName)
		if exists(dst) {
			continue
		}
		found, err := findBinary(extractDir, srcName)
		if err != nil {
			return "", fmt.Errorf("locate %s binary: %w", name, err)
		}
		if err := os.Rename(found, dst); err != nil {
			return "", fmt.Errorf("install %s binary: %w", name, err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(dst, 0755); err != nil {
				return "", fmt.Errorf("chmod %s: %w", name, err)
			}
		}
	}

	log("[thirdparty] FFmpeg installed at %s", binDir)
	return binDir, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// 内嵌依赖下载的边界参数（AGENTS.md 硬规则 4：跨网络调用必须有超时、上限与
// 有界重试）。
const (
	// downloadTimeout 是单次下载请求的总时限。FFmpeg 包较大（~80MB），慢速
	// 自托管线路也要能下完，所以给到 30 分钟；注意这里**不设 http.Client.Timeout**
	// （那会对整个 body 读取生效，慢网必被砍），而是用请求级 context 控制。
	downloadTimeout = 30 * time.Minute
	// downloadMaxBytes 是单次下载的大小上限（512MiB）。超过即失败，防止误连到
	// 返回巨大响应的镜像或被中间人塞垃圾流量。
	downloadMaxBytes = int64(512) << 20
	// downloadAttempts 是总尝试次数（含首次），仅对可重试错误生效。
	downloadAttempts = 3
	// downloadRetryBase 是退避基数：1s、2s（指数退避）。
	downloadRetryBase = time.Second
)

// downloadHTTPClient 是内嵌依赖下载专用 client。
//
// 不设 Client.Timeout（大文件慢网会被整段砍断），改为分项超时：建连 10s、
// TLS 握手 15s、等待响应头 30s、空闲连接 90s；整体时长由请求级 context 兜底。
var downloadHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

// httpStatusError 让重试判定能区分 4xx（不重试）与 429/5xx（可重试）。
type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.code)
}

// errDownloadTooLarge 表示响应体超过 downloadMaxBytes，属于不可重试错误。
var errDownloadTooLarge = errors.New("download exceeds size limit")

// isRetryableDownloadErr 判定错误是否值得重试：网络层错误、超时、429、5xx 可重试；
// 4xx（除 429）、超过大小上限、本地写文件失败不重试。
func isRetryableDownloadErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errDownloadTooLarge) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.code == http.StatusTooManyRequests || statusErr.code >= 500
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

// downloadFile 把 url 下载到临时文件并返回其路径。
//
// 规则 4：请求带 context（总时限 downloadTimeout）、大小上限 downloadMaxBytes、
// 最多 downloadAttempts 次指数退避重试（4xx 不重试）；任何失败路径都会删除临时文件。
func downloadFile(ctx context.Context, url string, log func(string, ...any)) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		path, err := downloadFileOnce(ctx, url, log)
		if err == nil {
			return path, nil
		}
		lastErr = err
		if !isRetryableDownloadErr(err) {
			return "", err
		}
		if attempt == downloadAttempts {
			break
		}
		delay := downloadRetryBase << (attempt - 1)
		if log != nil {
			log("[thirdparty] download attempt %d/%d failed: %v; retrying in %s", attempt, downloadAttempts, err, delay)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
	return "", lastErr
}

// downloadFileOnce 是单次下载尝试（不含重试）。
func downloadFileOnce(ctx context.Context, url string, log func(string, ...any)) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := downloadHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpStatusError{code: resp.StatusCode}
	}
	if resp.ContentLength > downloadMaxBytes {
		return "", fmt.Errorf("%w: Content-Length %d exceeds %d", errDownloadTooLarge, resp.ContentLength, downloadMaxBytes)
	}

	f, err := os.CreateTemp("", "ridgericetalk-download-*")
	if err != nil {
		return "", err
	}
	// 失败时删除临时文件；成功返回路径时把 cleanup 置空。
	cleanup := func() {
		f.Close()
		os.Remove(f.Name())
	}
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()

	// Log rough progress for large downloads (every 10 MiB).
	var total int64
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	const logInterval = 10 * 1024 * 1024
	var written int64
	// LimitReader 多留 1 字节，用于判定「恰好超限」。
	body := io.LimitReader(resp.Body, downloadMaxBytes+1)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if written+int64(n) > downloadMaxBytes {
				return "", fmt.Errorf("%w: more than %d bytes", errDownloadTooLarge, downloadMaxBytes)
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return "", werr
			}
			written += int64(n)
			if total > 0 && log != nil && written%logInterval < int64(n) {
				log("[thirdparty] downloaded %.1f/%.1f MiB", float64(written)/(1024*1024), float64(total)/(1024*1024))
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return f.Name(), nil
}

func extractArchive(src, archiveType, dst string) error {
	switch archiveType {
	case "tar.gz":
		return extractTarGz(src, dst)
	case "tar.xz":
		return extractTarXz(src, dst)
	case "zip":
		return extractZip(src, dst)
	default:
		return fmt.Errorf("unsupported archive type: %s", archiveType)
	}
}

// extractTarXz extracts a tar.xz archive. On Unix systems it delegates to the
// system `tar` binary because Go's standard library does not include an xz
// decompressor. This keeps the server free of additional dependencies.
func extractTarXz(src, dst string) error {
	if runtime.GOOS != "windows" {
		cmd := exec.Command("tar", "-xJf", src, "-C", dst)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tar -xJf failed: %w: %s", err, string(out))
		}
		return nil
	}
	return fmt.Errorf("tar.xz extraction on Windows requires an external xz decompressor")
}

// 解压保护上限（1.9.5 task-138，规则 4 的同族风险：资源耗尽）。
//
// 内嵌依赖包来自第三方 CDN，理论上可信，但下载链路只校验「传输大小」不校验
// 「解压后大小」——压缩炸弹可以在几百 KB 的包里塞进几十 GB。这里在解压前用
// 条目数 + 声明的解压后总量预检，解压过程中再用累计写入量兜底（防止声明的
// UncompressedSize 撒谎）。
const (
	extractMaxTotalBytes int64 = 512 << 20 // 512MiB
	extractMaxEntries          = 10000
)

var (
	errExtractTooLarge  = errors.New("archive exceeds extraction size limit")
	errExtractTooMany   = errors.New("archive has too many entries")
	errExtractTraversal = errors.New("archive entry escapes target directory")
)

// extractBudget 累计解压条目数与写入字节数，超限立即失败（不静默跳过）。
type extractBudget struct {
	entries int
	bytes   int64
}

func (b *extractBudget) addEntry() error {
	b.entries++
	if b.entries > extractMaxEntries {
		return fmt.Errorf("%w: more than %d entries", errExtractTooMany, extractMaxEntries)
	}
	return nil
}

func (b *extractBudget) addBytes(n int64) error {
	b.bytes += n
	if b.bytes > extractMaxTotalBytes {
		return fmt.Errorf("%w: more than %d bytes", errExtractTooLarge, extractMaxTotalBytes)
	}
	return nil
}

// remaining 返回还能写入的字节数 +1，供 io.LimitReader 提前截断。
func (b *extractBudget) remaining() int64 {
	left := extractMaxTotalBytes - b.bytes
	if left < 0 {
		return 1
	}
	return left + 1
}

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	budget := &extractBudget{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := budget.addEntry(); err != nil {
			return err
		}

		target := filepath.Join(dst, header.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("%w: %s", errExtractTraversal, header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(out, io.LimitReader(tr, budget.remaining()))
			out.Close()
			if copyErr != nil {
				return copyErr
			}
			if err := budget.addBytes(n); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractZip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	if len(r.File) > extractMaxEntries {
		return fmt.Errorf("%w: %d entries", errExtractTooMany, len(r.File))
	}
	// 预检：先按声明的解压后大小估算总量，尽早拒绝压缩炸弹。
	var declared uint64
	for _, f := range r.File {
		declared += f.UncompressedSize64
		if declared > uint64(extractMaxTotalBytes) {
			return fmt.Errorf("%w: declared %d bytes", errExtractTooLarge, declared)
		}
	}

	budget := &extractBudget{}
	for _, f := range r.File {
		if err := budget.addEntry(); err != nil {
			return err
		}
		target := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("%w: %s", errExtractTraversal, f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(rc, budget.remaining()))
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
		if err := budget.addBytes(n); err != nil {
			return err
		}
	}
	return nil
}

func findBinary(dir, name string) (string, error) {
	var found string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Name() == name {
			found = path
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("binary %q not found in %s", name, dir)
	}
	return found, nil
}
