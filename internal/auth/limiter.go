package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrRateLimited       = errors.New("authentication rate limited")
	ErrInvalidAuthConfig = errors.New("invalid authentication configuration")
)

const (
	limiterUsernameDomain = "login-username"
	limiterIPDomain       = "login-client-ip"
)

type LoginLimiterOptions struct {
	Window      time.Duration
	MaxAttempts int
	Capacity    int
	Secret      []byte
}

type limiterBucket struct {
	attempts    int
	windowStart time.Time
	lastSeen    time.Time
}

type LoginLimiter struct {
	mu          sync.Mutex
	window      time.Duration
	maxAttempts int
	capacity    int
	secret      []byte
	buckets     map[[sha256.Size]byte]limiterBucket
}

func NewLoginLimiter(opts LoginLimiterOptions) (*LoginLimiter, error) {
	if opts.Window <= 0 || opts.MaxAttempts <= 0 || opts.Capacity < 2 || len(opts.Secret) < sha256.Size {
		return nil, ErrInvalidAuthConfig
	}
	return &LoginLimiter{
		window:      opts.Window,
		maxAttempts: opts.MaxAttempts,
		capacity:    opts.Capacity,
		secret:      append([]byte(nil), opts.Secret...),
		buckets:     make(map[[sha256.Size]byte]limiterBucket),
	}, nil
}

func (l *LoginLimiter) Check(username, clientIP string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupExpiredLocked(now)
	for _, key := range l.loginKeys(username, clientIP) {
		bucket, ok := l.buckets[key]
		if ok && bucket.attempts >= l.maxAttempts {
			return ErrRateLimited
		}
	}
	return nil
}

func (l *LoginLimiter) RecordFailure(username, clientIP string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupExpiredLocked(now)
	keys := l.loginKeys(username, clientIP)
	for _, key := range keys {
		bucket, ok := l.buckets[key]
		if !ok {
			if len(l.buckets) >= l.capacity {
				l.evictOldestLocked(keys)
			}
			l.buckets[key] = limiterBucket{attempts: 1, windowStart: now, lastSeen: now}
			continue
		}
		bucket.attempts++
		bucket.lastSeen = now
		l.buckets[key] = bucket
	}
}

func (l *LoginLimiter) RecordSuccess(username string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupExpiredLocked(now)
	delete(l.buckets, l.key(limiterUsernameDomain, normalizeUsername(username)))
}

func (l *LoginLimiter) cleanupExpiredLocked(now time.Time) {
	for key, bucket := range l.buckets {
		if !now.Before(bucket.windowStart.Add(l.window)) {
			delete(l.buckets, key)
		}
	}
}

func (l *LoginLimiter) evictOldestLocked(protected [2][sha256.Size]byte) {
	var oldestKey [sha256.Size]byte
	var oldestTime time.Time
	first := true
	for key, bucket := range l.buckets {
		if key == protected[0] || key == protected[1] {
			continue
		}
		if first || bucket.lastSeen.Before(oldestTime) {
			oldestKey = key
			oldestTime = bucket.lastSeen
			first = false
		}
	}
	if !first {
		delete(l.buckets, oldestKey)
	}
}

func (l *LoginLimiter) loginKeys(username, clientIP string) [2][sha256.Size]byte {
	return [2][sha256.Size]byte{
		l.key(limiterUsernameDomain, normalizeUsername(username)),
		l.key(limiterIPDomain, clientIP),
	}
}

func (l *LoginLimiter) key(domain, value string) [sha256.Size]byte {
	digest := hmac.New(sha256.New, l.secret)
	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(value))
	var key [sha256.Size]byte
	copy(key[:], digest.Sum(nil))
	return key
}

func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}
