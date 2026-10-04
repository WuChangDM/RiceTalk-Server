package middleware

import (
	"sync"
	"time"
)

const (
	maxAccountAttempts = 10000 // maximum entries to prevent memory exhaustion
	maxIPAttempts      = 10000
)

// LoginAttempt tracks failed login attempts
type LoginAttempt struct {
	Count     int
	FirstFail time.Time
	LockedAt  *time.Time
}

// BruteForceProtector implements double-layer rate limiting for login:
// - Account-level: 5 failures → 30 min lockout
// - IP-level: 20 failures → 1 hour lockout
type BruteForceProtector struct {
	accountAttempts map[string]*LoginAttempt
	ipAttempts      map[string]*LoginAttempt
	mu              sync.RWMutex
}

// NewBruteForceProtector creates a new BruteForceProtector
func NewBruteForceProtector() *BruteForceProtector {
	return &BruteForceProtector{
		accountAttempts: make(map[string]*LoginAttempt),
		ipAttempts:      make(map[string]*LoginAttempt),
	}
}

// AllowAccount checks if the given account is allowed to attempt login.
func (bfp *BruteForceProtector) AllowAccount(email string) (bool, time.Duration) {
	bfp.mu.Lock()
	defer bfp.mu.Unlock()

	now := time.Now()
	if attempt, exists := bfp.accountAttempts[email]; exists {
		if attempt.LockedAt != nil {
			lockLeft := 30*time.Minute - now.Sub(*attempt.LockedAt)
			if lockLeft > 0 {
				return false, lockLeft
			}
			delete(bfp.accountAttempts, email)
		}
	}
	return true, 0
}

// AllowIP checks if the given IP is allowed to attempt login.
func (bfp *BruteForceProtector) AllowIP(ip string) (bool, time.Duration) {
	bfp.mu.Lock()
	defer bfp.mu.Unlock()

	now := time.Now()
	if attempt, exists := bfp.ipAttempts[ip]; exists {
		if attempt.LockedAt != nil {
			lockLeft := 1*time.Hour - now.Sub(*attempt.LockedAt)
			if lockLeft > 0 {
				return false, lockLeft
			}
			delete(bfp.ipAttempts, ip)
		}
	}
	return true, 0
}

// RecordFailure records a failed login for both account and IP.
func (bfp *BruteForceProtector) RecordFailure(email, ip string) {
	bfp.mu.Lock()
	defer bfp.mu.Unlock()

	now := time.Now()

	// Prevent memory exhaustion from distributed attacks
	if len(bfp.accountAttempts) >= maxAccountAttempts {
		bfp.evictOldestAccount()
	}
	if len(bfp.ipAttempts) >= maxIPAttempts {
		bfp.evictOldestIP()
	}

	aa, exists := bfp.accountAttempts[email]
	if !exists {
		bfp.accountAttempts[email] = &LoginAttempt{Count: 1, FirstFail: now}
	} else {
		aa.Count++
		if aa.Count >= 5 {
			aa.LockedAt = &now
		}
	}

	ia, exists := bfp.ipAttempts[ip]
	if !exists {
		bfp.ipAttempts[ip] = &LoginAttempt{Count: 1, FirstFail: now}
	} else {
		ia.Count++
		if ia.Count >= 20 {
			ia.LockedAt = &now
		}
	}
}

func (bfp *BruteForceProtector) evictOldestAccount() {
	var oldest string
	var oldestTime time.Time
	for k, v := range bfp.accountAttempts {
		if oldestTime.IsZero() || v.FirstFail.Before(oldestTime) {
			oldest = k
			oldestTime = v.FirstFail
		}
	}
	if oldest != "" {
		delete(bfp.accountAttempts, oldest)
	}
}

func (bfp *BruteForceProtector) evictOldestIP() {
	var oldest string
	var oldestTime time.Time
	for k, v := range bfp.ipAttempts {
		if oldestTime.IsZero() || v.FirstFail.Before(oldestTime) {
			oldest = k
			oldestTime = v.FirstFail
		}
	}
	if oldest != "" {
		delete(bfp.ipAttempts, oldest)
	}
}

// ResetAccount clears failure records for an account on successful login.
func (bfp *BruteForceProtector) ResetAccount(email string) {
	bfp.mu.Lock()
	defer bfp.mu.Unlock()
	delete(bfp.accountAttempts, email)
}

// StartCleanup runs periodic cleanup of stale entries.
func (bfp *BruteForceProtector) StartCleanup() {
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			bfp.cleanup()
		}
	}()
}

func (bfp *BruteForceProtector) cleanup() {
	bfp.mu.Lock()
	defer bfp.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-2 * time.Hour)
	for email, attempt := range bfp.accountAttempts {
		if attempt.FirstFail.Before(cutoff) {
			delete(bfp.accountAttempts, email)
		}
	}
	for ip, attempt := range bfp.ipAttempts {
		if attempt.FirstFail.Before(cutoff) {
			delete(bfp.ipAttempts, ip)
		}
	}
}
