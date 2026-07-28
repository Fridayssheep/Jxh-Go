package auth

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLoginLimiterRejectsAttemptAfterThreshold(t *testing.T) {
	limiter, err := NewLoginLimiter(LoginLimiterOptions{
		Window:      5 * time.Minute,
		MaxAttempts: 5,
		Capacity:    32,
		Secret:      bytes.Repeat([]byte{1}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	for attempt := 1; attempt <= 5; attempt++ {
		if err := limiter.Check("  Alice  ", "192.0.2.10", now); err != nil {
			t.Fatalf("Check() before failure %d = %v", attempt, err)
		}
		limiter.RecordFailure("  Alice  ", "192.0.2.10", now)
	}
	if err := limiter.Check("alice", "192.0.2.10", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Check() after threshold = %v, want ErrRateLimited", err)
	}
}

func TestLoginLimiterRejectsEitherUsernameOrIPBucket(t *testing.T) {
	limiter := newTestLoginLimiter(t, 2, 32)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	limiter.RecordFailure("alice", "192.0.2.10", now)
	limiter.RecordFailure("alice", "192.0.2.11", now)
	if err := limiter.Check("ALICE", "192.0.2.99", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("username bucket Check() = %v, want ErrRateLimited", err)
	}

	limiter = newTestLoginLimiter(t, 2, 32)
	limiter.RecordFailure("alice", "192.0.2.10", now)
	limiter.RecordFailure("bob", "192.0.2.10", now)
	if err := limiter.Check("charlie", "192.0.2.10", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("IP bucket Check() = %v, want ErrRateLimited", err)
	}
}

func TestLoginLimiterSuccessClearsOnlyNormalizedUsernameBucket(t *testing.T) {
	limiter := newTestLoginLimiter(t, 2, 32)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	for range 2 {
		limiter.RecordFailure(" Alice ", "192.0.2.10", now)
	}

	limiter.RecordSuccess("ALICE", now)
	if err := limiter.Check("alice", "192.0.2.99", now); err != nil {
		t.Fatalf("username bucket was not cleared: %v", err)
	}
	if err := limiter.Check("other", "192.0.2.10", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("IP bucket Check() = %v, want ErrRateLimited", err)
	}
}

func TestPasswordChangeLimiterIsIsolatedFromLoginUsernameAndIPBuckets(t *testing.T) {
	limiter := newTestLoginLimiter(t, 2, 32)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	for range 2 {
		limiter.RecordPasswordChangeFailure("usr_1", now)
	}
	if err := limiter.CheckPasswordChange("usr_1", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("password change bucket Check() = %v, want ErrRateLimited", err)
	}
	if err := limiter.Check("alice", "192.0.2.10", now); err != nil {
		t.Fatalf("password change failures polluted login buckets: %v", err)
	}

	for range 2 {
		limiter.RecordFailure("alice", "192.0.2.10", now)
	}
	if err := limiter.CheckPasswordChange("usr_2", now); err != nil {
		t.Fatalf("login failures polluted another password change bucket: %v", err)
	}
	limiter.RecordPasswordChangeSuccess("usr_1", now)
	if err := limiter.CheckPasswordChange("usr_1", now); err != nil {
		t.Fatalf("password change success did not clear its bucket: %v", err)
	}
}

func TestLoginLimiterStoresOnlyFixedLengthDigests(t *testing.T) {
	limiter := newTestLoginLimiter(t, 2, 32)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	limiter.RecordFailure(" SensitiveAdmin ", "192.0.2.10", now)

	if len(limiter.buckets) != 2 {
		t.Fatalf("bucket count = %d, want 2", len(limiter.buckets))
	}
	for key := range limiter.buckets {
		if len(key) != 32 {
			t.Fatalf("bucket key length = %d, want 32", len(key))
		}
	}
}

func TestLoginLimiterBoundsCapacity(t *testing.T) {
	const capacity = 4
	limiter := newTestLoginLimiter(t, 2, capacity)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	for i := range 20 {
		limiter.RecordFailure(fmt.Sprintf("user-%d", i), fmt.Sprintf("192.0.2.%d", i), now.Add(time.Duration(i)*time.Second))
		if got := len(limiter.buckets); got > capacity {
			t.Fatalf("bucket count after insert %d = %d, want <= %d", i, got, capacity)
		}
	}
}

func TestLoginLimiterCleansExpiredBucketsAtWindowBoundary(t *testing.T) {
	limiter := newTestLoginLimiter(t, 2, 32)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	limiter.RecordFailure("alice", "192.0.2.10", now)

	if err := limiter.Check("bob", "192.0.2.11", now.Add(5*time.Minute)); err != nil {
		t.Fatalf("Check() at window boundary = %v", err)
	}
	if got := len(limiter.buckets); got != 0 {
		t.Fatalf("expired bucket count = %d, want 0", got)
	}
}

func TestLoginLimiterIsConcurrentAndCapacitySafe(t *testing.T) {
	const capacity = 64
	limiter := newTestLoginLimiter(t, 5, capacity)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	var workers sync.WaitGroup
	for worker := range 24 {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for attempt := range 100 {
				username := fmt.Sprintf("user-%d", (worker+attempt)%40)
				clientIP := fmt.Sprintf("192.0.2.%d", (worker*3+attempt)%40)
				at := now.Add(time.Duration(attempt) * time.Millisecond)
				_ = limiter.Check(username, clientIP, at)
				limiter.RecordFailure(username, clientIP, at)
				if attempt%7 == 0 {
					limiter.RecordSuccess(username, at)
				}
			}
		}(worker)
	}
	workers.Wait()

	if got := len(limiter.buckets); got > capacity {
		t.Fatalf("bucket count = %d, want <= %d", got, capacity)
	}
}

func TestLoginLimiterRejectsShortHMACSecret(t *testing.T) {
	_, err := NewLoginLimiter(LoginLimiterOptions{
		Window:      5 * time.Minute,
		MaxAttempts: 5,
		Capacity:    32,
		Secret:      bytes.Repeat([]byte{1}, 31),
	})
	if !errors.Is(err, ErrInvalidAuthConfig) {
		t.Fatalf("NewLoginLimiter() error = %v, want ErrInvalidAuthConfig", err)
	}
}

func TestLoginLimiterCapacityChurnPreservesCurrentUsernameBucket(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	for trial := range 1000 {
		limiter := newTestLoginLimiter(t, 2, 2)
		limiter.RecordFailure("alice", "192.0.2.1", now)
		limiter.RecordFailure("alice", "192.0.2.2", now)
		if err := limiter.Check("alice", "192.0.2.99", now); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("trial %d lost the current username bucket: %v", trial, err)
		}
	}
}

func newTestLoginLimiter(t *testing.T, maxAttempts, capacity int) *LoginLimiter {
	t.Helper()
	limiter, err := NewLoginLimiter(LoginLimiterOptions{
		Window:      5 * time.Minute,
		MaxAttempts: maxAttempts,
		Capacity:    capacity,
		Secret:      bytes.Repeat([]byte{1}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	return limiter
}
