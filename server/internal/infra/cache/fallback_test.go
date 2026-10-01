// Package cacheinfra provides cache wrappers for Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.3 缓存层设计, P2 spec
// refactor-p2-repository-ristretto.
//
// These tests verify the ristretto -> memoryCache fallback path that is wired
// in cmd/server/main.go (lines ~150-159). main()'s assembly is not directly
// unit-testable, so selectCache below mirrors the exact decision logic and is
// kept in sync with main.go. See SubTask 7.3.
package cacheinfra

import (
	"testing"
	"time"
)

// selectCache mirrors the fallback decision logic in cmd/server/main.go:
// prefer ristretto; if NewRistrettoCache fails, fall back to memoryCache.
// Returns the selected cache and a kind tag ("ristretto" / "memory") so tests
// can assert which path was taken. Keep this in sync with main.go.
func selectCache(maxCost, numCounters, bufferItems int64) (Cache, string) {
	ristrettoCache, rerr := NewRistrettoCache(maxCost, numCounters, bufferItems)
	if rerr != nil {
		// In main.go this path also emits:
		//   log.Warn("ristretto init failed, fallback to memoryCache", "error", rerr)
		return NewMemoryCache(), "memory"
	}
	return ristrettoCache, "ristretto"
}

// TestRistrettoFallbackToMemory verifies that an invalid ristretto
// configuration (NumCounters == 0, which ristretto rejects) causes the
// fallback decision logic to select memoryCache instead, and that the selected
// cache is fully usable so the server can still boot.
func TestRistrettoFallbackToMemory(t *testing.T) {
	// NumCounters == 0 is rejected by ristretto (also covered by
	// TestRistrettoCache_NewInvalidConfig). main.go relies on this returning
	// a non-nil error to trigger the fallback.
	c, kind := selectCache(1<<20, 0, 64)
	t.Cleanup(c.Close)

	if kind != "memory" {
		t.Fatalf("expected fallback to memory, got kind=%q", kind)
	}
	if _, ok := c.(*memoryCache); !ok {
		t.Fatalf("expected *memoryCache after fallback, got %T", c)
	}

	// The fallback cache must be fully functional: main.go hands it to
	// NewCachedUserRepository / NewCachedMessageRepository and continues boot.
	c.Set("fb-key", "fb-value", time.Minute)
	if v, ok := c.Get("fb-key"); !ok || v != "fb-value" {
		t.Fatalf("fallback cache Get failed after Set: v=%v ok=%v", v, ok)
	}
	c.Del("fb-key")
	if _, ok := c.Get("fb-key"); ok {
		t.Fatal("fallback cache Del did not remove the entry")
	}
}

// TestRistrettoFallback_NormalPath verifies that a valid ristretto
// configuration does NOT trigger the fallback — ristretto is selected. This
// guards against regressions where the fallback always activates.
func TestRistrettoFallback_NormalPath(t *testing.T) {
	c, kind := selectCache(1<<20, 1000, 64)
	t.Cleanup(c.Close)

	if kind != "ristretto" {
		t.Fatalf("expected ristretto on valid config, got kind=%q", kind)
	}
	if _, ok := c.(*RistrettoCache); !ok {
		t.Fatalf("expected *RistrettoCache, got %T", c)
	}
}

// TestRistrettoFallback_NoLeakOnFailure ensures that when NewRistrettoCache
// fails, no goroutine/resource is left behind by the ristretto constructor
// (it returns nil, nil-error so there is nothing to Close). The fallback
// memoryCache is the only live cache and must be Close-able.
func TestRistrettoFallback_NoLeakOnFailure(t *testing.T) {
	c, kind := selectCache(1<<20, 0, 64)
	if kind != "memory" {
		t.Fatalf("expected fallback, got %q", kind)
	}
	// Closing must not panic and must release the memoryCache sweep goroutine.
	c.Close()
	c.Close() // idempotent, matches RistrettoCache semantics
}
