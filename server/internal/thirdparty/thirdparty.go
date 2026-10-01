// Package thirdparty downloads and installs embedded dependencies
// (LiveKit, FFmpeg, etc.) so the server can run without requiring
// users to install them manually.
package thirdparty

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

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
func ResolveFFmpeg(cfg *config.Config, log func(string, ...any)) error {
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

	binDir, err := EnsureFFmpeg(versions, cfg.ThirdPartyDir, log)
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
func EnsureLiveKit(versions *Versions, installDir string, log func(string, ...any)) (string, error) {
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

	tmpFile, err := downloadFile(binary.URL, log)
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
func EnsureFFmpeg(versions *Versions, installDir string, log func(string, ...any)) (string, error) {
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
			tmpFile, err = downloadFile(a.binary.URL, log)
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

func downloadFile(url string, log func(string, ...any)) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", "ridgericetalk-download-*")
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Log rough progress for large downloads (every 10 MiB).
	var total int64
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	const logInterval = 10 * 1024 * 1024
	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return "", werr
			}
			written += int64(n)
			if total > 0 && log != nil && written%logInterval < int64(n) {
				log("[thirdparty] downloaded %.1f/%.1f MiB", float64(written)/(1024*1024), float64(total)/(1024*1024))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
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
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dst, header.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) {
			continue // skip path traversal attempts
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
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
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

	for _, f := range r.File {
		target := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) {
			continue
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
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
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
