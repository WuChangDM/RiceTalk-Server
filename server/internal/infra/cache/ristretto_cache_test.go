// Package cacheinfra provides cache wrappers for Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.3 缓存层设计.
package cacheinfra

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dgraph-io/ristretto/v2"
)

// newTestRistrettoCache builds a small-capacity RistrettoCache suitable for
// unit tests. NumCounters must be > 0 per ristretto's validation.
func newTestRistrettoCache(t *testing.T) *RistrettoCache {
	t.Helper()
	c, err := NewRistrettoCache(1<<20, 1000, 64)
	if err != nil {
		t.Fatalf("NewRistrettoCache failed: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestRistrettoCache_SetGet(t *testing.T) {
	c := newTestRistrettoCache(t)

	c.Set("k1", "v1", time.Minute)
	c.wait() // ristretto Set is asynchronous; flush before Get
	v, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if v != "v1" {
		t.Fatalf("got %v, want v1", v)
	}
}

func TestRistrettoCache_Miss(t *testing.T) {
	c := newTestRistrettoCache(t)

	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected cache miss for nonexistent key")
	}
}

func TestRistrettoCache_TTLExpiration(t *testing.T) {
	c := newTestRistrettoCache(t)

	c.Set("ephemeral", "v", 50*time.Millisecond)
	c.wait()
	if _, ok := c.Get("ephemeral"); !ok {
		t.Fatal("expected hit before TTL")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get("ephemeral"); ok {
		t.Fatal("expected miss after TTL")
	}
}

func TestRistrettoCache_Del(t *testing.T) {
	c := newTestRistrettoCache(t)

	c.Set("k", "v", time.Minute)
	c.wait()
	c.Del("k")
	c.wait()
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss after Del")
	}
}

func TestRistrettoCache_CloseIdempotent(t *testing.T) {
	c, err := NewRistrettoCache(1<<20, 1000, 64)
	if err != nil {
		t.Fatalf("NewRistrettoCache failed: %v", err)
	}
	c.Close()
	c.Close() // should not panic
}

// TestRistrettoCache_InterfaceCompat verifies RistrettoCache satisfies the
// Cache interface and inter-operates with CachedUserRepository like memoryCache.
func TestRistrettoCache_InterfaceCompat(t *testing.T) {
	var c Cache = func() Cache {
		rc, err := NewRistrettoCache(1<<20, 1000, 64)
		if err != nil {
			t.Fatalf("NewRistrettoCache failed: %v", err)
		}
		return rc
	}()
	defer c.Close()

	c.Set("interface", 42, time.Minute)
	if rc, ok := c.(*RistrettoCache); ok {
		rc.wait()
	}
	v, ok := c.Get("interface")
	if !ok {
		t.Fatal("expected cache hit via Cache interface")
	}
	if v != 42 {
		t.Fatalf("got %v, want 42", v)
	}
}

// TestRistrettoCache_NewInvalidConfig ensures invalid config returns an error
// rather than a nil cache (the fallback path in main.go relies on this).
func TestRistrettoCache_NewInvalidConfig(t *testing.T) {
	if _, err := NewRistrettoCache(1<<20, 0, 64); err == nil {
		t.Fatal("expected error when NumCounters == 0")
	}
}

// TestRistrettoCache_Concurrent verifies safe concurrent access to the cache.
// RistrettoCache is backed by ristretto which is safe for concurrent use; this
// test guards against regressions in the wrapper layer. Run with -race for full
// data-race detection.
func TestRistrettoCache_Concurrent(t *testing.T) {
	c := newTestRistrettoCache(t)

	const goroutines = 50
	const opsPerG = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < opsPerG; j++ {
				key := fmt.Sprintf("g%d-k%d", g, j)
				c.Set(key, j, time.Minute)
				if _, ok := c.Get(key); !ok {
					// ristretto Set is async; a miss right after Set is possible
					// and not a correctness bug. We only require no panic/race.
					_ = ok
				}
				c.Del(key)
			}
		}(i)
	}
	wg.Wait()
	c.wait()
}

// TestRistrettoCache_BulkInsert verifies the cache handles a large number of
// entries without error and remains readable. This exercises ristretto's
// internal set buffer and W-TinyLFU admission under load.
func TestRistrettoCache_BulkInsert(t *testing.T) {
	c := newTestRistrettoCache(t)

	const n = 10000
	for i := 0; i < n; i++ {
		c.Set(fmt.Sprintf("bulk-%d", i), i, time.Minute)
	}
	c.wait() // flush all pending Sets

	// Sample-read a subset and confirm at least some entries survived.
	// W-TinyLFU admission may reject a fraction of entries when NumCounters is
	// small relative to the item count, so we assert "some hits" rather than
	// "all hits".
	hits := 0
	for i := 0; i < n; i += 100 {
		if _, ok := c.Get(fmt.Sprintf("bulk-%d", i)); ok {
			hits++
		}
	}
	if hits == 0 {
		t.Fatalf("expected some hits after bulk insert, got 0")
	}
}

// TestRistrettoCache_MetricsFeasibility verifies that ristretto's built-in
// metrics (hits/misses/ratio/evictions) can be enabled and read, confirming
// cache hit-rate monitoring is feasible.
//
// NOTE: NewRistrettoCache currently does NOT set Config.Metrics=true, so the
// production RistrettoCache does not expose metrics yet (spec does not mandate
// it). This test constructs a ristretto cache directly with Metrics enabled to
// validate the underlying capability and document the follow-up improvement
// path for SubTask 7.2 ("缓存命中率监控").
func TestRistrettoCache_MetricsFeasibility(t *testing.T) {
	cache, err := ristretto.NewCache(&ristretto.Config[string, any]{
		NumCounters: 1000,
		MaxCost:     1 << 20,
		BufferItems: 64,
		Metrics:     true,
	})
	if err != nil {
		t.Fatalf("ristretto.NewCache failed: %v", err)
	}
	t.Cleanup(cache.Close)

	cache.SetWithTTL("hit-key", "v", 0, time.Minute)
	cache.Wait()
	cache.Get("hit-key")  // hit
	cache.Get("miss-key") // miss

	m := cache.Metrics
	if m == nil {
		t.Fatal("expected non-nil Metrics when Config.Metrics=true")
	}
	if m.Hits() == 0 {
		t.Errorf("expected at least 1 hit, got %d", m.Hits())
	}
	if m.Misses() == 0 {
		t.Errorf("expected at least 1 miss, got %d", m.Misses())
	}
	// Verify eviction/ratio accessors are callable (no panic).
	_ = m.KeysEvicted()
	_ = m.Ratio()
}
