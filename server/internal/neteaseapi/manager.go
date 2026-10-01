// Package neteaseapi manages the NeteaseCloudMusicApi Node.js subprocess.
package neteaseapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/logger"
)

const (
	defaultPort         = 3300
	healthCheckInterval = 3 * time.Second
	healthCheckTimeout  = 30 * time.Second
	stopWaitTimeout     = 5 * time.Second
)

// Manager manages the NeteaseCloudMusicApi subprocess.
type Manager struct {
	cfg        *config.Config
	log        *logger.Logger
	serviceDir string
	cmd        *exec.Cmd
	mu         sync.Mutex
	stopCh     chan struct{}
	wg         sync.WaitGroup
	started    bool
}

// New creates a new Netease API manager.
func New(log *logger.Logger) *Manager {
	return &Manager{
		log:    log,
		stopCh: make(chan struct{}),
	}
}

// Start installs dependencies if needed and starts the Netease API server.
func (m *Manager) Start(cfg *config.Config) error {
	m.cfg = cfg

	if !cfg.EmbeddedDeps {
		m.log.Info("[netease-api] embedded deps disabled, using external endpoint", "endpoint", cfg.NeteaseAPIEndpoint)
		return nil
	}

	m.serviceDir = filepath.Join(config.ServerDir(), "services", "netease-api")
	if _, err := os.Stat(filepath.Join(m.serviceDir, "package.json")); err != nil {
		m.log.Warn("[netease-api] source not found, using external endpoint", "dir", m.serviceDir)
		return nil
	}

	m.log.Info("[netease-api] installing dependencies...")
	if err := m.runNpmInstall(); err != nil {
		m.log.Warn("[netease-api] npm install failed, music bot may be unavailable", "error", err)
		return nil // non-fatal
	}

	if err := m.startProcess(); err != nil {
		m.log.Warn("[netease-api] failed to start, music bot may be unavailable", "error", err)
		return nil // non-fatal
	}

	m.started = true
	m.wg.Add(1)
	go m.watchdog()

	// Wait for health check
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()
	if m.waitHealthy(ctx) {
		m.log.Info("[netease-api] ready", "endpoint", cfg.NeteaseAPIEndpoint)
	} else {
		m.log.Warn("[netease-api] health check timed out, music bot may be unavailable")
	}

	return nil
}

// Stop gracefully shuts down the Netease API subprocess.
func (m *Manager) Stop() {
	if !m.started {
		return
	}
	m.log.Info("[netease-api] stopping subprocess...")
	close(m.stopCh)

	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		if runtime.GOOS == "windows" {
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(os.Interrupt)
		}

		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()

		select {
		case <-done:
			m.log.Info("[netease-api] subprocess exited gracefully")
		case <-time.After(stopWaitTimeout):
			_ = cmd.Process.Kill()
			m.log.Warn("[netease-api] subprocess force killed after timeout")
		}
	}

	m.wg.Wait()
	m.started = false
}

func (m *Manager) runNpmInstall() error {
	cmd := exec.Command("npm", "install", "--omit=dev")
	cmd.Dir = m.serviceDir
	cmd.Stdout = &prefixWriter{prefix: "[netease-api:npm] ", log: m.log}
	cmd.Stderr = &prefixWriter{prefix: "[netease-api:npm:err] ", log: m.log}
	return cmd.Run()
}

func (m *Manager) startProcess() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := exec.Command("node", "app.js")
	cmd.Dir = m.serviceDir
	cmd.Env = append(os.Environ(), "PORT=3300")
	cmd.Stdout = &prefixWriter{prefix: "[netease-api] ", log: m.log}
	cmd.Stderr = &prefixWriter{prefix: "[netease-api:err] ", log: m.log}

	if err := cmd.Start(); err != nil {
		return err
	}

	m.cmd = cmd
	m.log.Info("[netease-api] process started", "pid", cmd.Process.Pid)
	return nil
}

func (m *Manager) healthCheck() bool {
	endpoint := m.cfg.NeteaseAPIEndpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("http://127.0.0.1:%d", defaultPort)
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (m *Manager) waitHealthy(ctx context.Context) bool {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if m.healthCheck() {
				return true
			}
		}
	}
}

func (m *Manager) watchdog() {
	defer m.wg.Done()

	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
		}

		if m.healthCheck() {
			continue
		}

		m.log.Warn("[netease-api] process unhealthy, restarting")
		m.mu.Lock()
		cmd := m.cmd
		m.mu.Unlock()

		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}

		if err := m.startProcess(); err != nil {
			m.log.Error("[netease-api] restart failed", "error", err)
		}
	}
}

// serverDir returns the directory that should be used for resolving bundled
// server resources. It delegates to config.ServerDir so that `go run` falls
// back to the current working directory.
func serverDir() string {
	return config.ServerDir()
}

type prefixWriter struct {
	prefix string
	log    *logger.Logger
}

func (w *prefixWriter) Write(p []byte) (n int, err error) {
	lines := strings.Split(string(p), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			w.log.Info(w.prefix + line)
		}
	}
	return len(p), nil
}
