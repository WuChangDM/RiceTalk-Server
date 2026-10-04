// Package cacheinfra provides cache wrappers for Repository interfaces.
// Design doc: 服务端详细开发文档 §5B.3 缓存层设计.
package cacheinfra

import (
	"testing"
	"time"
)

func TestMemoryCache_SetGet(t *testing.T) {
	c := NewMemoryCache()
	defer c.Close()

	c.Set("k1", "v1", time.Minute)
	v, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if v != "v1" {
		t.Fatalf("got %v, want v1", v)
	}
}

func TestMemoryCache_Miss(t *testing.T) {
	c := NewMemoryCache()
	defer c.Close()

	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected cache miss for nonexistent key")
	}
}

func TestMemoryCache_TTLExpiration(t *testing.T) {
	c := NewMemoryCache()
	defer c.Close()

	c.Set("ephemeral", "v", 50*time.Millisecond)
	if _, ok := c.Get("ephemeral"); !ok {
		t.Fatal("expected hit before TTL")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get("ephemeral"); ok {
		t.Fatal("expected miss after TTL")
	}
}

func TestMemoryCache_Del(t *testing.T) {
	c := NewMemoryCache()
	defer c.Close()

	c.Set("k", "v", time.Minute)
	c.Del("k")
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss after Del")
	}
}

func TestMemoryCache_CloseIdempotent(t *testing.T) {
	c := NewMemoryCache()
	c.Close()
	c.Close() // should not panic
}
