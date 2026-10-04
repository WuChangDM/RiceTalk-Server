package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
)

// OwnerBreakGlassProtector implements the Owner Break-Glass emergency lockout
// mechanism (per design doc §9.7). After 3 consecutive failed sensitive
// operations, the Owner's admin access is locked for 30 minutes.
//
// Sensitive operations include: modifying ports, deleting members,
// and modifying user roles.
//
// This is an in-memory protector (like BruteForceProtector); state is cleared
// on server restart. This is acceptable because the lockout is a defense-in-
// depth measure, not the primary access control.
type OwnerBreakGlassProtector struct {
	failures        map[string]*breakGlassFailure // userID -> failures
	mu              sync.RWMutex
	maxFailures     int           // 3
	lockoutDuration time.Duration // 30 minutes
}

type breakGlassFailure struct {
	Count     int
	FirstFail time.Time
	LockedAt  *time.Time
}

// NewOwnerBreakGlassProtector creates a new OwnerBreakGlassProtector with
// the design-specified thresholds (3 failures → 30 minute lockout).
func NewOwnerBreakGlassProtector() *OwnerBreakGlassProtector {
	return &OwnerBreakGlassProtector{
		failures:        make(map[string]*breakGlassFailure),
		maxFailures:     3,
		lockoutDuration: 30 * time.Minute,
	}
}

// Allow checks if the given Owner user is allowed to perform sensitive
// operations. Returns (true, 0) if allowed, or (false, remaining) if locked.
func (bgp *OwnerBreakGlassProtector) Allow(userID string) (bool, time.Duration) {
	bgp.mu.Lock()
	defer bgp.mu.Unlock()

	now := time.Now()
	if f, exists := bgp.failures[userID]; exists {
		if f.LockedAt != nil {
			lockLeft := bgp.lockoutDuration - now.Sub(*f.LockedAt)
			if lockLeft > 0 {
				return false, lockLeft
			}
			// Lockout expired — clear the record
			delete(bgp.failures, userID)
		}
	}
	return true, 0
}

// RecordFailure records a failed sensitive operation for the given Owner user.
// When the failure count reaches maxFailures, the user is locked for
// lockoutDuration.
func (bgp *OwnerBreakGlassProtector) RecordFailure(userID string) {
	bgp.mu.Lock()
	defer bgp.mu.Unlock()

	now := time.Now()
	f, exists := bgp.failures[userID]
	if !exists {
		bgp.failures[userID] = &breakGlassFailure{Count: 1, FirstFail: now}
		return
	}
	f.Count++
	if f.Count >= bgp.maxFailures && f.LockedAt == nil {
		lockedAt := now
		f.LockedAt = &lockedAt
	}
}

// Reset clears the failure record for a user after a successful sensitive
// operation.
func (bgp *OwnerBreakGlassProtector) Reset(userID string) {
	bgp.mu.Lock()
	defer bgp.mu.Unlock()
	delete(bgp.failures, userID)
}

// OwnerBreakGlassCheck is a gin middleware that checks whether the current
// Owner user is locked out by the Break-Glass protector. Apply this to
// sensitive admin operation routes.
//
// Non-Owner users (ADMIN/MEMBER) are allowed through — Break-Glass only
// applies to the Owner account per design doc §9.7.
func OwnerBreakGlassCheck(bgp *OwnerBreakGlassProtector) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := GetRole(c)
		if role != RoleOwner {
			// Break-Glass only applies to Owner
			c.Next()
			return
		}

		userID := GetUserID(c)
		if userID == "" {
			c.Next()
			return
		}

		if allowed, remaining := bgp.Allow(userID); !allowed {
			lockErr := errors.New(errors.AUTH_ACCOUNT_LOCKED,
				"Owner account locked due to repeated sensitive operation failures; try again in "+remaining.String())
			c.AbortWithStatusJSON(lockErr.Status, errors.FromError(c, lockErr))
			return
		}

		c.Next()
	}
}

// StartCleanup runs periodic cleanup of stale Break-Glass entries.
func (bgp *OwnerBreakGlassProtector) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bgp.cleanup()
			}
		}
	}()
}

func (bgp *OwnerBreakGlassProtector) cleanup() {
	bgp.mu.Lock()
	defer bgp.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-2 * time.Hour)
	for userID, f := range bgp.failures {
		if f.FirstFail.Before(cutoff) {
			delete(bgp.failures, userID)
		}
	}
}
