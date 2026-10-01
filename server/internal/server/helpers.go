package server

import (
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/version"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/serverstate"
)

// effectiveAdminPort returns the port on which the admin UI/API is actually
// reachable. When admin_port_shared is enabled, admin routes live on the API
// port instead of the dedicated admin port. In production, admin_port_shared
// is always ignored (see route mounting above), so we must report the
// dedicated admin port regardless of the flag.
func effectiveAdminPort(cfg *config.Config) int {
	if cfg.AdminPortShared && cfg.Env != "production" {
		return cfg.Port
	}
	return cfg.AdminPort
}

// buildServerInfo constructs the response payload for GET /server/info.
// It uses the serverstate.Manager as the source of truth for initialization
// status and external network settings.
//
// requestHost 是客户端实际连接的 Host（含端口）：apiUrl/webUrl/adminUrl 以它
// 的地址部分拼接 —— 曾经硬编 localhost，客户端把「服务器」的管理端地址拼成
// 用户本机，远程部署场景点开必然打不开（N23）。
func buildServerInfo(stateMgr *serverstate.Manager, cfg *config.Config, requestHost string) gin.H {
	adminPort := effectiveAdminPort(cfg)
	base := hostBaseURL(requestHost, cfg.Port)
	info := stateMgr.BuildInfoResponse(version.Server)
	return gin.H{
		"name":                info.Name,
		"version":             info.Version,
		"initialized":         info.Initialized,
		"state":               info.State,
		"serverUrl":           info.ServerURL,
		"livekitUrl":          info.LiveKitURL,
		"mediaUdpPort":        info.MediaUDPPort,
		"webVoiceEnabled":     info.WebVoiceEnabled,
		"clientAccessEnabled": info.ClientAccessEnabled,
		"useHttps":            info.UseHTTPS,
		"apiPort":             cfg.Port,
		"adminPort":           adminPort,
		"livekitPort":         cfg.LiveKitPort,
		"apiUrl":              fmt.Sprintf("%s/api/v1", base),
		"webUrl":              base,
		"adminUrl":            fmt.Sprintf("%s:%d/admin", stripPort(base), adminPort),
		"features": gin.H{
			"whiteboard":  true,
			"cloudfs":     true,
			"sharedoc":    true,
			"schedule":    true,
			"minigames":   true,
			"virtualnet":  true,
			"bots":        true,
			"screenshare": true,
		},
	}
}

// hostBaseURL 从请求 Host（host:port）推导「scheme://地址:API端口」。
// 请求 Host 缺端口时按 apiPort 补；IPv6 地址自动补方括号。
func hostBaseURL(requestHost string, apiPort int) string {
	host := requestHost
	if h, _, err := net.SplitHostPort(requestHost); err == nil && h != "" {
		host = h
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(apiPort)))
}

// stripPort 去掉 URL 里的端口（scheme://host:port → scheme://host）。
func stripPort(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	u.Host = u.Hostname()
	return u.String()
}

// joinHostPort 统一走 net.JoinHostPort（IPv6 自动补方括号）。
func joinHostPort(host, port string) string {
	return net.JoinHostPort(host, port)
}

// printStartupInfo prints a formatted access-info banner for double-click launches.
func printStartupInfo(cfg *config.Config) {
	adminPort := effectiveAdminPort(cfg)
	fmt.Println("")
	fmt.Println("========================================")
	fmt.Println("  RidgeRiceTalk Server is Running")
	fmt.Println("========================================")
	fmt.Println("")
	fmt.Printf("  Environment:  %s\n", cfg.Env)
	fmt.Printf("  API Port:     %d\n", cfg.Port)
	fmt.Printf("  Admin Port:   %d\n", adminPort)
	fmt.Println("  LiveKit:      ws://localhost:7880")
	fmt.Println("")
	fmt.Println("  Local Access:")
	fmt.Printf("    Voice:   http://localhost:%d\n", cfg.Port)
	fmt.Printf("    Admin:   http://localhost:%d/admin\n", adminPort)
	fmt.Println("")
	fmt.Printf("  WebSocket:  ws://localhost:%d/ws\n", cfg.Port)
	fmt.Printf("  Metrics:    http://localhost:%d/metrics\n", adminPort)
	fmt.Println("")
	fmt.Println("  (Press Ctrl+C to stop the server)")
	fmt.Println("")
}
