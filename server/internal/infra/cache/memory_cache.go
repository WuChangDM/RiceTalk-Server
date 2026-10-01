// Package cacheinfra provides cache wrappers for Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.3 缓存层设计.
//
// This package implements the cache layer using a simple in-memory TTL cache.
// The interface is designed to be compatible with github.com/dgraph/ristretto/v2
// so the underlying implementation can be swapped in once the dependency is
// available in the build environment.
package cacheinfra

import (
	"sync"
	"time"
)

// Cache is a minimal TTL cache interface compatible with ristretto's usage
// patterns for Repository caching. Implementations must be safe for concurrent
// use.
type Cache interface {
	Get(key string) (interface{}, bool)
	Set(key string, value interface{}, ttl time.Duration)
	Del(key string)
	Close()
}

// entry holds a value and its expiration time.
type entry struct {
	value interface{}
	expAt time.Time
}

// memoryCache is a thread-safe in-memory TTL cache used as a placeholder
// for ristretto. It is intentionally simple: no admission control, no
// eviction policy beyond lazy expiration.
type memoryCache struct {
	mu      sync.RWMutex
	items   map[string]entry
	stopCh  chan struct{}
	stopped bool
}

// NewMemoryCache creates a new in-memory TTL cache. A background goroutine
// periodically sweeps expired entries; call Close to release it.
func NewMemoryCache() Cache {
	c := &memoryCache{
		items:  make(map[string]entry),
		stopCh: make(chan struct{}),
	}
	go c.sweep(time.Minute)
	return c
}

func (c *memoryCache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expAt) {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return nil, false
	}
	return e.value, true
}

func (c *memoryCache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	c.items[key] = entry{value: value, expAt: time.Now().Add(ttl)}
	c.mu.Unlock()
}

func (c *memoryCache) Del(key string) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

func (c *memoryCache) Close() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	close(c.stopCh)
	c.mu.Unlock()
}

// sweep periodically removes expired entries.
func (c *memoryCache) sweep(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
			now := time.Now()
			c.mu.Lock()
			for k, e := range c.items {
				if now.After(e.expAt) {
					delete(c.items, k)
				}
			}
			c.mu.Unlock()
		}
	}
}
