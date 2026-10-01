package middleware

import (
	"hash/fnv"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
)

const defaultMaxClients = 10000
const shardCount = 256

// RateLimiter implements a token bucket rate limiter per client. It supports
// separate limits for authenticated and anonymous clients.
//
// Internally the client map is sharded into 256 independent shards so that
// Allow() only contends with clients that hash to the same shard, not the
// entire process. This removes the previous global mutex bottleneck under
// high concurrency.
type RateLimiter struct {
	shards        [shardCount]*rateLimitShard
	requests      int
	burst         int
	authRequests  int
	authBurst     int
	window        time.Duration
	maxClients    int
}

type rateLimitShard struct {
	mu      sync.RWMutex
	clients map[string]*clientLimit
}

type clientLimit struct {
	tokens    int
	lastCheck time.Time
}

// shardIndex returns the shard index for a clientID using FNV-1a hash.
func shardIndex(clientID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(clientID))
	return h.Sum32() % shardCount
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(requestsPerMin, burst int) *RateLimiter {
	if requestsPerMin <= 0 {
		requestsPerMin = 60
	}
	if burst <= 0 {
		burst = 10
	}
	rl := &RateLimiter{
		requests:     requestsPerMin,
		burst:        burst,
		authRequests: requestsPerMin,
		authBurst:    burst,
		window:       time.Minute,
		maxClients:   defaultMaxClients,
	}
	for i := range rl.shards {
		rl.shards[i] = &rateLimitShard{clients: make(map[string]*clientLimit)}
	}
	return rl
}

// SetAuthLimits configures a higher threshold for authenticated users. If not
// called, authenticated users share the same limits as anonymous clients.
func (rl *RateLimiter) SetAuthLimits(requestsPerMin, burst int) {
	if requestsPerMin > 0 {
		rl.authRequests = requestsPerMin
	}
	if burst > 0 {
		rl.authBurst = burst
	}
}

func (rl *RateLimiter) limitsFor(clientID string) (requests, burst int) {
	if len(clientID) > 5 && clientID[:5] == "user:" {
		return rl.authRequests, rl.authBurst
	}
	return rl.requests, rl.burst
}

// Allow checks if a client is allowed to make a request
func (rl *RateLimiter) Allow(clientID string) bool {
	idx := shardIndex(clientID)
	shard := rl.shards[idx]
	shard.mu.Lock()
	defer shard.mu.Unlock()

	requests, burst := rl.limitsFor(clientID)

	// Memory leak prevention: cleanup stale clients when this shard's client
	// count exceeds a per-shard threshold (proportional to maxClients).
	if len(shard.clients) >= rl.maxClients/shardCount {
		rl.cleanupShardLocked(shard)
	}

	now := time.Now()
	cl, exists := shard.clients[clientID]
	if !exists {
		shard.clients[clientID] = &clientLimit{
			tokens:    burst - 1,
			lastCheck: now,
		}
		return true
	}

	// Replenish tokens based on time elapsed using float arithmetic
	elapsed := now.Sub(cl.lastCheck)
	tokensToAdd := int(float64(elapsed) / float64(rl.window) * float64(requests))
	cl.tokens += tokensToAdd
	if cl.tokens > burst {
		cl.tokens = burst
	}
	cl.lastCheck = now

	if cl.tokens > 0 {
		cl.tokens--
		return true
	}
	return false
}

// cleanupShardLocked removes stale client entries to prevent memory leak.
// Must be called with shard.mu held.
func (rl *RateLimiter) cleanupShardLocked(shard *rateLimitShard) {
	now := time.Now()
	cutoff := now.Add(-rl.window * 2)
	for id, cl := range shard.clients {
		if cl.lastCheck.Before(cutoff) {
			delete(shard.clients, id)
		}
	}
}

// RetryAfter returns the number of seconds until the client can make another
// request. Must be called after Allow() returns false. Returns 0 if the
// client is not rate limited or has tokens available.
func (rl *RateLimiter) RetryAfter(clientID string) int {
	idx := shardIndex(clientID)
	shard := rl.shards[idx]
	shard.mu.RLock()
	defer shard.mu.RUnlock()
	cl, exists := shard.clients[clientID]
	if !exists {
		return 0
	}
	if cl.tokens > 0 {
		return 0
	}
	requests, _ := rl.limitsFor(clientID)
	// Calculate time until 1 token is replenished.
	// tokensPerSec = requests / rl.window.Seconds()
	tokensPerSec := float64(requests) / rl.window.Seconds()
	if tokensPerSec <= 0 {
		return 30 // fallback per API spec §11.1
	}
	secondsToWait := int(1.0/tokensPerSec) + 1
	if secondsToWait < 1 {
		secondsToWait = 1
	}
	return secondsToWait
}

// RateLimit returns a gin middleware for rate limiting
func RateLimit(limiter *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		// M6 fix: Skip rate limiting for WebSocket upgrade requests — they get
		// their own rate limiting (max 3 concurrent connections) inside the Hub.
		// Applying HTTP rate limits to WS upgrades causes false 429s on reconnect.
		if c.Request.Header.Get("Upgrade") == "websocket" {
			c.Next()
			return
		}
		clientID := c.ClientIP()
		// If user is authenticated, use userID for per-user rate limiting
		if userID := GetUserID(c); userID != "" {
			clientID = "user:" + userID
		}
		if !limiter.Allow(clientID) {
			// L28: Return Retry-After header so clients know when to retry (per API spec §11.1)
			retryAfter := limiter.RetryAfter(clientID)
			if retryAfter <= 0 {
				retryAfter = 30 // fallback per API spec §11.1
			}
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, errors.FromError(c, errors.ErrRateLimited))
			return
		}
		c.Next()
	}
}

// AuthRateLimit returns a stricter rate limiter for auth endpoints.
// The limits can be overridden via RRT_AUTH_RATE_LIMIT_REQUESTS and
// RRT_AUTH_RATE_LIMIT_BURST for test/development environments.
func AuthRateLimit() *RateLimiter {
	requests := 10
	burst := 5
	if v, err := strconv.Atoi(os.Getenv("RRT_AUTH_RATE_LIMIT_REQUESTS")); err == nil && v > 0 {
		requests = v
	}
	if v, err := strconv.Atoi(os.Getenv("RRT_AUTH_RATE_LIMIT_BURST")); err == nil && v > 0 {
		burst = v
	}
	return NewRateLimiter(requests, burst)
}
