package validator

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHIBPClientIsPwned(t *testing.T) {
	// Compute the SHA-1 hash of a known password to build a deterministic response.
	password := "hunter2"
	h := sha1.Sum([]byte(password))
	hash := strings.ToUpper(hex.EncodeToString(h[:]))
	prefix := hash[:5]
	suffix := hash[5:]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+prefix {
			// Return the target suffix with a positive count and a padding entry with 0.
			_, _ = fmt.Fprintf(w, "%s:123\nFAKE0000:0\n", suffix)
			return
		}
		// Any other prefix returns an empty 200 OK.
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewHIBPClient(server.URL+"/", 0)
	pwned, err := client.IsPwned(password)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pwned {
		t.Fatalf("expected password to be reported as pwned")
	}

	// A password whose hash suffix is not present should not be pwned.
	pwned, err = client.IsPwned("not-in-this-list-password-12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pwned {
		t.Fatalf("expected password to be reported as safe")
	}
}
