package network

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetectorWithMockPublicIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"ip": "203.0.113.10"})
	}))
	defer server.Close()

	d := NewDetectorWithURL(server.URL)
	result, err := d.Detect()
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if result.PublicIPv4 != "203.0.113.10" {
		t.Fatalf("unexpected public IPv4: %s", result.PublicIPv4)
	}
	if len(result.LocalIPv4s) == 0 {
		t.Fatal("expected at least one local IPv4")
	}
	if result.UPnPSupported {
		t.Fatal("expected UPnP not supported in MVP")
	}
}

func TestGetLocalAddresses(t *testing.T) {
	addrs, err := getLocalAddresses()
	if err != nil {
		t.Fatalf("get local addresses: %v", err)
	}
	if len(addrs.v4) == 0 && len(addrs.v6) == 0 {
		t.Fatal("expected at least one local address")
	}
	for _, ip := range addrs.v4 {
		if ip == "127.0.0.1" {
			t.Fatal("expected no loopback addresses")
		}
	}
}
