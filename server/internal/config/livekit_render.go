// Package config 中的 livekit_render.go 负责将 livekit.yaml.template 渲染为
// 实际可用的 livekit.yaml。设计目标：让新手用户无需手动编辑 LiveKit 配置
// 即可一键部署，首次启动时后端根据自动生成的密钥、检测到的节点 IP 与默认
// 端口渲染配置文件；后续启动检测到 livekit.yaml 已存在则跳过，避免覆盖
// 用户手动修改。
package config

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LiveKitPorts LiveKit 服务端口配置。
type LiveKitPorts struct {
	WSPort   int // WebSocket 信令端口，默认 7880
	UDPPort  int // UDP 媒体端口，默认 7882
	TCPPort  int // TCP fallback 端口，默认 7881
	PromPort int // Prometheus 监控端口，默认 6789
}

// DefaultLiveKitPorts 返回 LiveKit 推荐的默认端口配置：
// WSPort=7880, UDPPort=7882, TCPPort=7881, PromPort=6789。
func DefaultLiveKitPorts() LiveKitPorts {
	return LiveKitPorts{
		WSPort:   7880,
		UDPPort:  7882,
		TCPPort:  7881,
		PromPort: 6789,
	}
}

// DetectNodeIP 自动检测节点 IP。检测顺序：
//  1. 优先请求外部服务（https://api.ipify.org，超时 5 秒）获取公网 IP；
//  2. 失败时回退到本机非 loopback 的 IPv4 地址（遍历 net.Interfaces()，
//     取第一个非 127.0.0.1 的 IPv4）；
//  3. 都失败时返回错误。
func DetectNodeIP() (string, error) {
	// 1. 尝试公网 IP 检测。
	if ip, err := fetchPublicIP("https://api.ipify.org", 5*time.Second); err == nil {
		return ip, nil
	}

	// 2. 回退到本机非 loopback IPv4。
	if ip, err := localIPv4(); err == nil {
		return ip, nil
	}

	// 3. 都失败。
	return "", fmt.Errorf("detect node ip: 公网 IP 与本机 IPv4 均不可用")
}

// fetchPublicIP 请求外部服务获取公网 IP，返回去除首尾空白后的字符串。
// 超时或非 200 状态码均视为失败。
func fetchPublicIP(url string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("request public ip: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read public ip body: %w", err)
	}

	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "", fmt.Errorf("empty public ip")
	}
	return ip, nil
}

// localIPv4 遍历本机网络接口，返回第一个非 loopback 的 IPv4 地址。
func localIPv4() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("list interfaces: %w", err)
	}
	for _, iface := range interfaces {
		// 跳过未启用或 loopback 接口。
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.To4() == nil || ip.IsLoopback() {
				continue
			}
			return ip.To4().String(), nil
		}
	}
	return "", fmt.Errorf("no non-loopback ipv4 address found")
}

// RenderLiveKitConfig 读取 livekit.yaml.template 并渲染为实际配置文件。
// 渲染规则：
//   - 若 outputPath 已存在则跳过渲染（返回 nil），避免覆盖用户手动修改；
//   - 读取 templatePath 内容，用 strings.ReplaceAll 替换 7 个占位符：
//     {{LIVEKIT_API_KEY}}、{{LIVEKIT_API_SECRET}}、{{NODE_IP}}、
//     {{WS_PORT}}、{{UDP_PORT}}、{{TCP_PORT}}、{{PROM_PORT}}；
//   - 旧模板若不包含 {{TCP_PORT}} 占位符，ReplaceAll 不会报错，原值保留；
//   - 用 os.MkdirAll 确保输出目录存在（0700 权限）；
//   - 写入 outputPath，文件权限 0600。
func RenderLiveKitConfig(templatePath, outputPath string, s *SecretsFile, nodeIP string, ports LiveKitPorts) error {
	// 检测输出文件是否已存在：存在则跳过渲染。
	if _, err := os.Stat(outputPath); err == nil {
		log.Printf("INFO: livekit.yaml 已存在，跳过渲染")
		return nil
	}

	// 读取模板内容。
	tmpl, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("read template %s: %w", templatePath, err)
	}

	// 替换占位符。
	out := string(tmpl)
	out = strings.ReplaceAll(out, "{{LIVEKIT_API_KEY}}", s.LiveKitAPIKey)
	out = strings.ReplaceAll(out, "{{LIVEKIT_API_SECRET}}", s.LiveKitAPISecret)
	out = strings.ReplaceAll(out, "{{NODE_IP}}", nodeIP)
	out = strings.ReplaceAll(out, "{{WS_PORT}}", strconv.Itoa(ports.WSPort))
	out = strings.ReplaceAll(out, "{{UDP_PORT}}", strconv.Itoa(ports.UDPPort))
	out = strings.ReplaceAll(out, "{{TCP_PORT}}", strconv.Itoa(ports.TCPPort))
	out = strings.ReplaceAll(out, "{{PROM_PORT}}", strconv.Itoa(ports.PromPort))

	// 确保输出目录存在。
	dir := filepath.Dir(outputPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create output dir %s: %w", dir, err)
		}
	}

	// 写入目标文件，权限 0600。
	if err := os.WriteFile(outputPath, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write livekit.yaml %s: %w", outputPath, err)
	}

	log.Printf("INFO: 已渲染 livekit.yaml 到 %s", outputPath)
	return nil
}
