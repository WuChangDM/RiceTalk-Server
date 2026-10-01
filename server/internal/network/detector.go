// Package network provides network environment detection for the server,
// including local addresses, public IP, IPv6, and UPnP capability.
package network

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// DetectionResult contains the detected network environment information.
type DetectionResult struct {
	LocalIPv4s []string `json:"localIpv4s"`
	LocalIPv6s []string `json:"localIpv6s"`
	PublicIPv4 string   `json:"publicIpv4"`
	PublicIPv6 string   `json:"publicIpv6"`
	UPnPSupported bool  `json:"upnpSupported"`
	UPnPError    string `json:"upnpError,omitempty"`
}

// Detector detects network environment.
type Detector struct {
	publicIPURL string
	httpClient  *http.Client
}

// NewDetector creates a new network detector.
func NewDetector() *Detector {
	return &Detector{
		publicIPURL: "https://api.ipify.org?format=json",
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// NewDetectorWithURL creates a detector with a custom public IP detection URL.
func NewDetectorWithURL(url string) *Detector {
	d := NewDetector()
	d.publicIPURL = url
	return d
}

// Detect runs all network detection checks.
func (d *Detector) Detect() (*DetectionResult, error) {
	result := &DetectionResult{}

	localAddrs, err := getLocalAddresses()
	if err != nil {
		return nil, fmt.Errorf("get local addresses: %w", err)
	}
	result.LocalIPv4s = localAddrs.v4
	result.LocalIPv6s = localAddrs.v6

	publicIPv4, err := d.detectPublicIPv4()
	if err != nil {
		result.PublicIPv4 = ""
	} else {
		result.PublicIPv4 = publicIPv4
	}

	publicIPv6, err := detectPublicIPv6()
	if err != nil {
		result.PublicIPv6 = ""
	} else {
		result.PublicIPv6 = publicIPv6
	}

	upnpSupported, upnpErr := detectUPnP()
	result.UPnPSupported = upnpSupported
	if upnpErr != nil {
		result.UPnPError = upnpErr.Error()
	}

	return result, nil
}

type localAddressResult struct {
	v4 []string
	v6 []string
}

func getLocalAddresses() (*localAddressResult, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := &localAddressResult{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
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
			if ipnet.IP.IsLoopback() {
				continue
			}
			if ipnet.IP.To4() != nil {
				result.v4 = append(result.v4, ipnet.IP.String())
			} else {
				result.v6 = append(result.v6, ipnet.IP.String())
			}
		}
	}
	return result, nil
}

func (d *Detector) detectPublicIPv4() (string, error) {
	resp, err := d.httpClient.Get(d.publicIPURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	var body struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.IP == "" {
		return "", fmt.Errorf("empty public IP")
	}
	return body.IP, nil
}

func detectPublicIPv6() (string, error) {
	// For MVP, detect the first non-loopback, non-link-local IPv6 address.
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
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
			if ip.To4() != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no public IPv6 address found")
}

// Strategy represents a recommended connectivity / tunneling strategy.
type Strategy struct {
	Method   string `json:"method"`   // upnp | ddns | frp | cloudflare_tunnel | tailscale
	Priority int    `json:"priority"` // 1 = highest
	Reason   string `json:"reason"`   // human-readable explanation
}

// RecommendStrategies returns a prioritized list of connectivity strategies
// based on the detection result. Priority order:
//   - UPnP (public IP + UPnP available)
//   - DDNS (public IP, no UPnP)
//   - FRP / Cloudflare Tunnel (no public IP)
//   - Tailscale (P2P, always offered as a fallback)
func RecommendStrategies(result *DetectionResult) []Strategy {
	strategies := make([]Strategy, 0)
	priority := 1
	hasPublicIPv4 := result != nil && result.PublicIPv4 != ""

	if hasPublicIPv4 && result.UPnPSupported {
		strategies = append(strategies, Strategy{
			Method:   "upnp",
			Priority: priority,
			Reason:   "UPnP 可用，自动端口映射",
		})
		priority++
	} else if hasPublicIPv4 {
		strategies = append(strategies, Strategy{
			Method:   "ddns",
			Priority: priority,
			Reason:   "有公网 IP，可使用 DDNS 绑定域名直连",
		})
		priority++
	} else {
		strategies = append(strategies, Strategy{
			Method:   "frp",
			Priority: priority,
			Reason:   "无公网 IP，可使用 FRP 反向代理穿透",
		})
		priority++
		strategies = append(strategies, Strategy{
			Method:   "cloudflare_tunnel",
			Priority: priority,
			Reason:   "无公网 IP，可使用 Cloudflare Tunnel 免费穿透",
		})
		priority++
	}

	// Tailscale 作为 P2P 方案始终可用
	strategies = append(strategies, Strategy{
		Method:   "tailscale",
		Priority: priority,
		Reason:   "Tailscale 提供 P2P 直连，适合小规模私有部署",
	})

	return strategies
}
