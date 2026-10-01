// Package serverstate manages the server lifecycle state machine and network
// configuration. It is the source of truth for whether the server is
// initialized, whether Web voice is enabled, and what external address clients
// should use to connect.
package serverstate

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
)

// ServerState represents the current lifecycle state of the server.
type ServerState string

const (
	// StateUninitialized means the server has not completed the bootstrap
	// wizard and no owner account exists yet.
	StateUninitialized ServerState = "UNINITIALIZED"
	// StateInitialized means the owner exists but external network and Web
	// voice have not been configured.
	StateInitialized ServerState = "INITIALIZED"
	// StateWebVoiceLocked means the server is initialized but Web voice is
	// explicitly disabled by the administrator.
	StateWebVoiceLocked ServerState = "WEB_VOICE_LOCKED"
	// StateWebVoiceReady means Web voice is enabled and configured.
	StateWebVoiceReady ServerState = "WEB_VOICE_READY"
	// StateClientAccessReady means both Web voice and client access are
	// configured and enabled.
	StateClientAccessReady ServerState = "CLIENT_ACCESS_READY"
)

// NetworkConfig holds the external network settings chosen during bootstrap or
// updated from the admin panel.
type NetworkConfig struct {
	ExternalHost           string `json:"externalHost"`
	ExternalHTTPPort       int    `json:"externalHttpPort"`
	ExternalLiveKitWSPort  int    `json:"externalLiveKitWsPort"`
	ExternalMediaUDPPort   int    `json:"externalMediaUdpPort"`
	ExternalAdminPort      int    `json:"externalAdminPort"`      // TCP（默认 9090）
	ExternalLiveKitTCPPort int    `json:"externalLiveKitTcpPort"` // TCP（默认 7881，LiveKit TCP fallback）
	UseHTTPS               bool   `json:"useHttps"`
	UPNPEnabled            bool   `json:"upnpEnabled"`
	TURNTCPFallbackEnabled bool   `json:"turnTcpFallbackEnabled"`
	WebVoiceEnabled        bool   `json:"webVoiceEnabled"`
	ClientAccessEnabled    bool   `json:"clientAccessEnabled"`
}

// DefaultNetworkConfig returns a sensible default network configuration.
// BUG-NET-01: external host/ports can be overridden via environment variables
// (EXTERNAL_HOST, EXTERNAL_HTTP_PORT, EXTERNAL_LIVEKIT_WS_PORT,
// EXTERNAL_MEDIA_UDP_PORT, EXTERNAL_ADMIN_PORT, EXTERNAL_LIVEKIT_TCP_PORT).
// When an env var is unset, the original default is preserved. Persisted state
// in server-state.json always takes precedence over these defaults (see
// Manager.load).
func DefaultNetworkConfig() NetworkConfig {
	nc := NetworkConfig{
		ExternalHost:           "",
		ExternalHTTPPort:       443,
		ExternalLiveKitWSPort:  443,
		ExternalMediaUDPPort:   7882,
		ExternalAdminPort:      9090,
		ExternalLiveKitTCPPort: 7881,
		UseHTTPS:               true,
		UPNPEnabled:            true,
		TURNTCPFallbackEnabled: true,
		WebVoiceEnabled:        false,
		ClientAccessEnabled:    true,
	}
	if v := os.Getenv("EXTERNAL_HOST"); v != "" {
		nc.ExternalHost = v
	}
	if v := os.Getenv("EXTERNAL_HTTP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			nc.ExternalHTTPPort = p
		}
	}
	if v := os.Getenv("EXTERNAL_LIVEKIT_WS_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			nc.ExternalLiveKitWSPort = p
		}
	}
	if v := os.Getenv("EXTERNAL_MEDIA_UDP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			nc.ExternalMediaUDPPort = p
		}
	}
	if v := os.Getenv("EXTERNAL_ADMIN_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			nc.ExternalAdminPort = p
		}
	}
	if v := os.Getenv("EXTERNAL_LIVEKIT_TCP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			nc.ExternalLiveKitTCPPort = p
		}
	}
	return nc
}

// ServerURL returns the external server URL for API/Web access.
func (nc *NetworkConfig) ServerURL() string {
	scheme := "http"
	if nc.UseHTTPS {
		scheme = "https"
	}
	host := nc.ExternalHost
	if host == "" {
		host = "localhost"
	}
	port := nc.ExternalHTTPPort
	if (nc.UseHTTPS && port == 443) || (!nc.UseHTTPS && port == 80) {
		return fmt.Sprintf("%s://%s", scheme, host)
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

// LiveKitURL returns the external LiveKit WebSocket URL.
func (nc *NetworkConfig) LiveKitURL() string {
	scheme := "ws"
	if nc.UseHTTPS {
		scheme = "wss"
	}
	host := nc.ExternalHost
	if host == "" {
		host = "localhost"
	}
	port := nc.ExternalLiveKitWSPort
	if (nc.UseHTTPS && port == 443) || (!nc.UseHTTPS && port == 80) {
		return fmt.Sprintf("%s://%s/livekit", scheme, host)
	}
	return fmt.Sprintf("%s://%s:%d/livekit", scheme, host, port)
}

// persistedState is the on-disk representation of the state file.
type persistedState struct {
	Initialized bool          `json:"initialized"`
	Network     NetworkConfig `json:"network"`
}

// Manager is the source of truth for server lifecycle state.
type Manager struct {
	mu       sync.RWMutex
	state    ServerState
	network  NetworkConfig
	cfg      *config.Config
	dataPath string
}

// NewManager creates a state manager. It attempts to load persisted state from
// the configured local data path.
func NewManager(cfg *config.Config) (*Manager, error) {
	m := &Manager{
		state:    StateUninitialized,
		network:  DefaultNetworkConfig(),
		cfg:      cfg,
		dataPath: filepath.Join(cfg.LocalDataPath, "server-state.json"),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	m.reconcileState()
	return m, nil
}

// load reads persisted state from disk. If the file does not exist, the
// manager remains in the uninitialized state with default network config.
func (m *Manager) load() error {
	data, err := os.ReadFile(m.dataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read server state file: %w", err)
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		return fmt.Errorf("parse server state file: %w", err)
	}
	if ps.Initialized {
		m.state = StateInitialized
		m.network = ps.Network
	}
	return nil
}

// save persists the current state to disk.
func (m *Manager) save() error {
	if err := os.MkdirAll(filepath.Dir(m.dataPath), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	ps := persistedState{
		Initialized: m.state != StateUninitialized,
		Network:     m.network,
	}
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal server state: %w", err)
	}
	if err := os.WriteFile(m.dataPath, data, 0o600); err != nil {
		return fmt.Errorf("write server state file: %w", err)
	}
	return nil
}

// reconcileState derives the concrete state from the network config.
func (m *Manager) reconcileState() {
	if m.state == StateUninitialized {
		return
	}
	if m.network.WebVoiceEnabled {
		if m.network.ClientAccessEnabled {
			m.state = StateClientAccessReady
		} else {
			m.state = StateWebVoiceReady
		}
	} else {
		m.state = StateWebVoiceLocked
	}
}

// CurrentState returns the current server state.
func (m *Manager) CurrentState() ServerState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// IsInitialized reports whether the server has completed bootstrap.
func (m *Manager) IsInitialized() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state != StateUninitialized
}

// IsWebVoiceEnabled reports whether Web voice is enabled.
func (m *Manager) IsWebVoiceEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.network.WebVoiceEnabled
}

// IsClientAccessEnabled reports whether Windows client access is enabled.
func (m *Manager) IsClientAccessEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.network.ClientAccessEnabled
}

// Network returns a copy of the current network configuration.
func (m *Manager) Network() NetworkConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.network
}

// CompleteBootstrap marks the server as initialized and applies the bootstrap
// network configuration. This transition is only valid from UNINITIALIZED.
func (m *Manager) CompleteBootstrap(network NetworkConfig) *errors.AppError {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StateUninitialized {
		return errors.New(errors.SYSTEM_CONFLICT, "server is already initialized")
	}
	if err := validateNetwork(network); err != nil {
		return err
	}
	m.network = network
	m.state = StateInitialized
	m.reconcileState()
	if err := m.save(); err != nil {
		return errors.ErrInternal.WithDetails(err.Error())
	}
	return nil
}

// UpdateNetwork updates the network configuration after bootstrap.
func (m *Manager) UpdateNetwork(network NetworkConfig) *errors.AppError {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == StateUninitialized {
		return errors.New(errors.SYSTEM_CONFLICT, "server is not initialized")
	}
	if err := validateNetwork(network); err != nil {
		return err
	}
	m.network = network
	m.reconcileState()
	if err := m.save(); err != nil {
		return errors.ErrInternal.WithDetails(err.Error())
	}
	return nil
}

func validateNetwork(nc NetworkConfig) *errors.AppError {
	if nc.ExternalHost == "" {
		return errors.ErrBadRequest.WithDetails("external host is required")
	}
	if nc.ExternalHTTPPort <= 0 || nc.ExternalHTTPPort > 65535 {
		return errors.ErrBadRequest.WithDetails("external HTTP port must be between 1 and 65535")
	}
	if nc.ExternalLiveKitWSPort <= 0 || nc.ExternalLiveKitWSPort > 65535 {
		return errors.ErrBadRequest.WithDetails("external LiveKit WS port must be between 1 and 65535")
	}
	if nc.ExternalMediaUDPPort <= 0 || nc.ExternalMediaUDPPort > 65535 {
		return errors.ErrBadRequest.WithDetails("external media UDP port must be between 1 and 65535")
	}
	if nc.ExternalAdminPort <= 0 || nc.ExternalAdminPort > 65535 {
		return errors.ErrBadRequest.WithDetails("external admin port must be between 1 and 65535")
	}
	if nc.ExternalLiveKitTCPPort <= 0 || nc.ExternalLiveKitTCPPort > 65535 {
		return errors.ErrBadRequest.WithDetails("external LiveKit TCP port must be between 1 and 65535")
	}
	return nil
}

// ResolveExternalHost returns the best available external host. If the
// configured external host is empty or "localhost", it falls back to the first
// non-loopback IPv4 address.
func (m *Manager) ResolveExternalHost() string {
	m.mu.RLock()
	host := m.network.ExternalHost
	m.mu.RUnlock()
	if host != "" && host != "localhost" && host != "127.0.0.1" {
		return host
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "localhost"
}

// InfoResponse is the shape returned by /api/v1/server/info.
type InfoResponse struct {
	Name                string      `json:"name"`
	Version             string      `json:"version"`
	Initialized         bool        `json:"initialized"`
	State               ServerState `json:"state"`
	ServerURL           string      `json:"serverUrl"`
	LiveKitURL          string      `json:"livekitUrl"`
	MediaUDPPort        int         `json:"mediaUdpPort"`
	WebVoiceEnabled     bool        `json:"webVoiceEnabled"`
	ClientAccessEnabled bool        `json:"clientAccessEnabled"`
	UseHTTPS            bool        `json:"useHttps"`
}

// BuildInfoResponse builds the server info response.
func (m *Manager) BuildInfoResponse(version string) InfoResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return InfoResponse{
		Name:                m.cfg.ServerName,
		Version:             version,
		Initialized:         m.state != StateUninitialized,
		State:               m.state,
		ServerURL:           m.network.ServerURL(),
		LiveKitURL:          m.network.LiveKitURL(),
		MediaUDPPort:        m.network.ExternalMediaUDPPort,
		WebVoiceEnabled:     m.network.WebVoiceEnabled,
		ClientAccessEnabled: m.network.ClientAccessEnabled,
		UseHTTPS:            m.network.UseHTTPS,
	}
}

// NetworkResponse is the shape returned by /api/v1/server/network.
type NetworkResponse struct {
	Network             NetworkConfig `json:"network"`
	SuggestedSRVRecords []string      `json:"suggestedSrvRecords"`
	SRVTCPPredicate     string        `json:"srvTcpPredicate"`
	SRVUDPPredicate     string        `json:"srvUdpPredicate"`
}

// BuildNetworkResponse builds the network configuration response including SRV
// record suggestions.
func (m *Manager) BuildNetworkResponse() NetworkResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return NetworkResponse{
		Network: m.network,
		SuggestedSRVRecords: []string{
			fmt.Sprintf("_rrt._tcp.%s. 300 IN SRV 10 10 %d %s.", m.network.ExternalHost, m.network.ExternalHTTPPort, m.network.ExternalHost),
			fmt.Sprintf("_rrt-media._udp.%s. 300 IN SRV 10 10 %d %s.", m.network.ExternalHost, m.network.ExternalMediaUDPPort, m.network.ExternalHost),
			fmt.Sprintf("_rrt-livekit-tcp._tcp.%s. 300 IN SRV 10 10 %d %s.", m.network.ExternalHost, m.network.ExternalLiveKitTCPPort, m.network.ExternalHost),
		},
		SRVTCPPredicate: fmt.Sprintf("_rrt._tcp.%s", m.network.ExternalHost),
		SRVUDPPredicate: fmt.Sprintf("_rrt-media._udp.%s", m.network.ExternalHost),
	}
}
