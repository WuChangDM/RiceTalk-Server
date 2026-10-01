// Package cacheinfra provides cache wrappers for Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.3 缓存层设计, P2 spec
// refactor-p2-repository-ristretto.
//
// RistrettoCache implements the Cache interface using ristretto with W-TinyLFU
// admission/eviction and cost-based memory management. It is the preferred
// cache implementation; memoryCache remains as a fallback when ristretto
// initialization fails (design doc §5B.3 + P2 spec).
package cacheinfra

import (
	"time"

	"github.com/dgraph-io/ristretto/v2"
)

// Default W-TinyLFU configuration values per design doc §5B.3.
const (
	// DefaultCacheMaxCost is the default total cost budget (1 GiB). With a
	// cost of 0 per entry (see Set), this effectively bounds the entry count.
	DefaultCacheMaxCost int64 = 1073741824
	// DefaultCacheNumCounters is the default number of access-frequency
	// counters (~10x the expected item count for good eviction accuracy).
	DefaultCacheNumCounters int64 = 10000000
	// DefaultCacheBufferItems is the default Get buffer size; 64 is the
	// recommended value for typical workloads.
	DefaultCacheBufferItems int64 = 64
)

// Ensure RistrettoCache implements the Cache interface at compile time.
var _ Cache = (*RistrettoCache)(nil)

// RistrettoCache implements Cache using ristretto with W-TinyLFU eviction.
// It is safe for concurrent use. Set is asynchronous (ristretto batches
// writes for throughput); use wait to flush pending Sets when deterministic
// Set→Get visibility is required (e.g. in tests).
type RistrettoCache struct {
	cache *ristretto.Cache[string, any]
}

// NewRistrettoCache creates a new RistrettoCache with W-TinyLFU eviction.
//
//   - maxCost       total cost budget (e.g. bytes). With cost 0 per entry this
//                   bounds the number of cached entries.
//   - numCounters   number of access-frequency counters; ~10x the expected
//                   item count for good eviction accuracy.
//   - bufferItems   Get buffer size; 64 is recommended for typical workloads.
//
// Returns an error if the configuration is invalid (e.g. NumCounters == 0).
func NewRistrettoCache(maxCost, numCounters, bufferItems int64) (*RistrettoCache, error) {
	cache, err := ristretto.NewCache(&ristretto.Config[string, any]{
		NumCounters: numCounters,
		MaxCost:     maxCost,
		BufferItems: bufferItems,
	})
	if err != nil {
		return nil, err
	}
	return &RistrettoCache{cache: cache}, nil
}

// Get returns the value for the given key. The boolean is false if the key
// is absent or expired.
func (r *RistrettoCache) Get(key string) (interface{}, bool) {
	return r.cache.Get(key)
}

// Set stores value with the given TTL. A cost of 0 is used because the
// Repository cache stores pointer-sized values and MaxCost bounds the entry
// count. Set is asynchronous; the entry may not be visible to a subsequent
// Get until the internal set buffer is processed.
func (r *RistrettoCache) Set(key string, value interface{}, ttl time.Duration) {
	r.cache.SetWithTTL(key, value, 0, ttl)
}

// Del removes the entry for the given key. No-op if the key is absent.
func (r *RistrettoCache) Del(key string) {
	r.cache.Del(key)
}

// Close releases the background goroutines used by ristretto. It is safe to
// call multiple times.
func (r *RistrettoCache) Close() {
	r.cache.Close()
}

// wait blocks until all pending Set operations have been processed by the
// internal set buffer. ristretto's Set is asynchronous for throughput; this
// is used in tests to guarantee deterministic Set→Get visibility.
func (r *RistrettoCache) wait() {
	r.cache.Wait()
}
