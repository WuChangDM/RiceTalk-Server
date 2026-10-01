package validator

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HIBPClient checks passwords against the Have I Been Pwned (HIBP) k-anonymity API.
// Only the first 5 characters of the SHA-1 hash are sent over the network.
type HIBPClient struct {
	BaseURL string
	Timeout time.Duration
}

// NewHIBPClient creates a new HIBP client with sensible defaults.
func NewHIBPClient(baseURL string, timeout time.Duration) *HIBPClient {
	if baseURL == "" {
		baseURL = "https://api.pwnedpasswords.com/range/"
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &HIBPClient{BaseURL: baseURL, Timeout: timeout}
}

// IsPwned returns true if the password appears in a known data breach.
// It uses HIBP's k-anonymity model so the full password hash is never transmitted.
func (c *HIBPClient) IsPwned(password string) (bool, error) {
	h := sha1.Sum([]byte(password))
	hash := strings.ToUpper(hex.EncodeToString(h[:]))
	if len(hash) < 5 {
		return false, fmt.Errorf("unexpected hash length")
	}
	prefix := hash[:5]
	suffix := hash[5:]

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+prefix, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "RidgeRiceTalk")
	// Add-Padding returns additional fake suffixes with count 0 to mitigate
	// response-size based fingerprinting.
	req.Header.Add("Add-Padding", "true")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HIBP API returned status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.EqualFold(parts[0], suffix) {
			count, _ := strconv.Atoi(parts[1])
			return count > 0, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}
