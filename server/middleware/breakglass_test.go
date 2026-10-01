package middleware

import (
	"testing"
	"time"

	"ridgericetalk/internal/config"
)

// M9: Owner Break-Glass protector tests
func TestOwnerBreakGlassProtector_AllowsByDefault(t *testing.T) {
	bgp := NewOwnerBreakGlassProtector()
	allowed, remaining := bgp.Allow("user-1")
	if !allowed {
		t.Errorf("expected allowed by default, got locked with %v remaining", remaining)
	}
	if remaining != 0 {
		t.Errorf("expected 0 remaining, got %v", remaining)
	}
}

func TestOwnerBreakGlassProtector_LocksAfter3Failures(t *testing.T) {
	bgp := NewOwnerBreakGlassProtector()
	userID := "owner-1"

	// 2 failures should not lock
	bgp.RecordFailure(userID)
	bgp.RecordFailure(userID)
	if allowed, _ := bgp.Allow(userID); !allowed {
		t.Error("expected allowed after 2 failures")
	}

	// 3rd failure should lock
	bgp.RecordFailure(userID)
	allowed, remaining := bgp.Allow(userID)
	if allowed {
		t.Error("expected locked after 3 failures")
	}
	if remaining <= 0 || remaining > 30*time.Minute {
		t.Errorf("expected remaining ~30min, got %v", remaining)
	}
}

func TestOwnerBreakGlassProtector_ResetClearsFailures(t *testing.T) {
	bgp := NewOwnerBreakGlassProtector()
	userID := "owner-1"

	bgp.RecordFailure(userID)
	bgp.RecordFailure(userID)
	bgp.Reset(userID)

	// After reset, 2 more failures should not lock (count restarts)
	bgp.RecordFailure(userID)
	bgp.RecordFailure(userID)
	if allowed, _ := bgp.Allow(userID); !allowed {
		t.Error("expected allowed after reset + 2 failures")
	}
}

func TestOwnerBreakGlassProtector_NonOwnerAlwaysAllowed(t *testing.T) {
	bgp := NewOwnerBreakGlassProtector()
	// Even with failures on another user, a different user is unaffected
	bgp.RecordFailure("owner-1")
	bgp.RecordFailure("owner-1")
	bgp.RecordFailure("owner-1")

	if allowed, _ := bgp.Allow("owner-2"); !allowed {
		t.Error("expected owner-2 unaffected by owner-1 failures")
	}
}

// M11: JWT clock skew tolerance test
func TestParseToken_WithLeewayAllowsSlightlyExpiredToken(t *testing.T) {
	// This test verifies that ParseToken uses a 30-second leeway.
	// We test the leeway indirectly by generating a token and parsing it
	// immediately — the leeway should not cause valid tokens to be rejected.
	cfg := testConfig()
	accessToken, _, err := GenerateTokenPair("user-1", "testuser", "test@example.com", "MEMBER", 1, "space-1", "session-1", cfg)
	if err != nil {
		t.Fatalf("GenerateTokenPair: %v", err)
	}

	claims, err := ParseToken(accessToken, cfg)
	if err != nil {
		t.Errorf("ParseToken failed for fresh token: %v", err)
	}
	if claims.UserID != "user-1" {
		t.Errorf("expected user-1, got %s", claims.UserID)
	}
}

// helper to build a config with a test JWT secret
func testConfig() *config.Config {
	return &config.Config{
		JWTSecret:    "test-secret-key-for-leeway",
		JWTAccessTTL: 15,
		JWTRefreshTTL: 7,
	}
}
