package network

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRecommendStrategiesAllBranches covers all branches of RecommendStrategies.
func TestRecommendStrategiesAllBranches(t *testing.T) {
	t.Run("nil result returns frp + cloudflare + tailscale (no public IP)", func(t *testing.T) {
		strategies := RecommendStrategies(nil)
		if len(strategies) != 3 {
			t.Fatalf("expected 3 strategies for nil result, got %d", len(strategies))
		}
		if strategies[0].Method != "frp" {
			t.Errorf("expected first strategy frp, got %s", strategies[0].Method)
		}
		if strategies[1].Method != "cloudflare_tunnel" {
			t.Errorf("expected second strategy cloudflare_tunnel, got %s", strategies[1].Method)
		}
		if strategies[2].Method != "tailscale" {
			t.Errorf("expected third strategy tailscale, got %s", strategies[2].Method)
		}
		// Verify priorities are sequential starting from 1.
		for i, s := range strategies {
			if s.Priority != i+1 {
				t.Errorf("strategy %d: expected priority %d, got %d", i, i+1, s.Priority)
			}
		}
	})

	t.Run("no public IP returns frp + cloudflare + tailscale", func(t *testing.T) {
		result := &DetectionResult{PublicIPv4: "", UPnPSupported: false}
		strategies := RecommendStrategies(result)
		if len(strategies) != 3 {
			t.Fatalf("expected 3 strategies, got %d", len(strategies))
		}
		if strategies[0].Method != "frp" {
			t.Errorf("expected first strategy frp, got %s", strategies[0].Method)
		}
	})

	t.Run("public IP without UPnP returns ddns + tailscale", func(t *testing.T) {
		result := &DetectionResult{PublicIPv4: "203.0.113.10", UPnPSupported: false}
		strategies := RecommendStrategies(result)
		if len(strategies) != 2 {
			t.Fatalf("expected 2 strategies, got %d", len(strategies))
		}
		if strategies[0].Method != "ddns" {
			t.Errorf("expected first strategy ddns, got %s", strategies[0].Method)
		}
		if strategies[1].Method != "tailscale" {
			t.Errorf("expected second strategy tailscale, got %s", strategies[1].Method)
		}
		if strategies[0].Priority != 1 {
			t.Errorf("expected ddns priority 1, got %d", strategies[0].Priority)
		}
		if strategies[1].Priority != 2 {
			t.Errorf("expected tailscale priority 2, got %d", strategies[1].Priority)
		}
	})

	t.Run("public IP with UPnP returns upnp + tailscale", func(t *testing.T) {
		result := &DetectionResult{PublicIPv4: "203.0.113.10", UPnPSupported: true}
		strategies := RecommendStrategies(result)
		if len(strategies) != 2 {
			t.Fatalf("expected 2 strategies, got %d", len(strategies))
		}
		if strategies[0].Method != "upnp" {
			t.Errorf("expected first strategy upnp, got %s", strategies[0].Method)
		}
		if strategies[1].Method != "tailscale" {
			t.Errorf("expected second strategy tailscale, got %s", strategies[1].Method)
		}
	})

	t.Run("strategies have non-empty reasons", func(t *testing.T) {
		result := &DetectionResult{PublicIPv4: "203.0.113.10", UPnPSupported: true}
		strategies := RecommendStrategies(result)
		for _, s := range strategies {
			if s.Reason == "" {
				t.Errorf("strategy %s has empty reason", s.Method)
			}
		}
	})
}

// TestNewDetector verifies the default constructor sets sensible defaults.
func TestNewDetector(t *testing.T) {
	d := NewDetector()
	if d == nil {
		t.Fatal("expected non-nil detector")
	}
	if d.publicIPURL != "https://api.ipify.org?format=json" {
		t.Errorf("expected default publicIPURL, got %s", d.publicIPURL)
	}
	if d.httpClient == nil {
		t.Error("expected non-nil httpClient")
	}
}

// TestNewDetectorWithURL verifies the custom URL constructor.
func TestNewDetectorWithURL(t *testing.T) {
	d := NewDetectorWithURL("https://custom.example.com/ip")
	if d == nil {
		t.Fatal("expected non-nil detector")
	}
	if d.publicIPURL != "https://custom.example.com/ip" {
		t.Errorf("expected custom URL, got %s", d.publicIPURL)
	}
}

// TestDetectPublicIPv4ErrorPaths covers the error branches of detectPublicIPv4.
func TestDetectPublicIPv4ErrorPaths(t *testing.T) {
	t.Run("non-200 status returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		d := NewDetectorWithURL(server.URL)
		ip, err := d.detectPublicIPv4()
		if err == nil {
			t.Fatal("expected error for 500 status")
		}
		if ip != "" {
			t.Errorf("expected empty IP on error, got %s", ip)
		}
	})

	t.Run("empty IP field returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]string{"ip": ""})
		}))
		defer server.Close()

		d := NewDetectorWithURL(server.URL)
		_, err := d.detectPublicIPv4()
		if err == nil {
			t.Fatal("expected error for empty IP")
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not-json"))
		}))
		defer server.Close()

		d := NewDetectorWithURL(server.URL)
		_, err := d.detectPublicIPv4()
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("connection error returns error", func(t *testing.T) {
		// Create a server then immediately close it to force connection errors.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := server.URL
		server.Close()

		d := NewDetectorWithURL(url)
		_, err := d.detectPublicIPv4()
		if err == nil {
			t.Fatal("expected connection error")
		}
	})
}

// TestDetectCoversPublicIPv6Path exercises Detect to ensure the
// detectPublicIPv6 branch is executed (regardless of whether an IPv6 address
// is found on the test machine).
func TestDetectCoversPublicIPv6Path(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"ip": "203.0.113.20"})
	}))
	defer server.Close()

	d := NewDetectorWithURL(server.URL)
	result, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if result.PublicIPv4 != "203.0.113.20" {
		t.Errorf("expected public IPv4 203.0.113.20, got %s", result.PublicIPv4)
	}
	// detectPublicIPv6 either returns an address or empty string + error.
	// We just verify the field is accessible.
	_ = result.PublicIPv6
	// UPnP detection should set UPnPSupported=false (MVP) and possibly
	// populate UPnPError.
	if result.UPnPSupported {
		t.Errorf("expected UPnP not supported in MVP")
	}
}

// TestDetectPublicIPv6Directly calls detectPublicIPv6 directly to ensure the
// function is covered. The result depends on the machine's network
// configuration.
func TestDetectPublicIPv6Directly(t *testing.T) {
	_, err := detectPublicIPv6()
	// Either we find an IPv6 address (no error) or we don't (error). Both
	// are acceptable; we just verify the function doesn't panic.
	_ = err
}

// TestDetectUPnP covers the detectUPnP stub.
func TestDetectUPnP(t *testing.T) {
	supported, err := detectUPnP()
	if supported {
		t.Errorf("expected UPnP not supported in MVP")
	}
	if err == nil {
		t.Errorf("expected error from detectUPnP stub")
	}
}

// TestDetectionResultJSON verifies the JSON serialization of DetectionResult.
func TestDetectionResultJSON(t *testing.T) {
	result := &DetectionResult{
		LocalIPv4s:    []string{"192.168.1.1"},
		LocalIPv6s:    []string{"2001:db8::1"},
		PublicIPv4:    "203.0.113.10",
		PublicIPv6:    "2001:db8::10",
		UPnPSupported: false,
		UPnPError:     "not available",
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "localIpv4s") {
		t.Errorf("expected localIpv4s in JSON, got: %s", content)
	}
	if !strings.Contains(content, "publicIpv4") {
		t.Errorf("expected publicIpv4 in JSON, got: %s", content)
	}
	if !strings.Contains(content, "upnpError") {
		t.Errorf("expected upnpError in JSON, got: %s", content)
	}
}

// TestStrategyJSON verifies the JSON serialization of Strategy.
func TestStrategyJSON(t *testing.T) {
	s := Strategy{Method: "upnp", Priority: 1, Reason: "test"}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "upnp") {
		t.Errorf("expected method in JSON, got: %s", content)
	}
	if !strings.Contains(content, "test") {
		t.Errorf("expected reason in JSON, got: %s", content)
	}
}
