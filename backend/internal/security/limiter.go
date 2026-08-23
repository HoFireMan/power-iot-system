package security

import (
	"crypto/sha256"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	LimiterWindow         = 15 * time.Minute
	loginAccountLimit     = 5
	loginIPLimit          = 20
	refreshIPLimit        = 30
	refreshFamilyLimit    = 10
	defaultLimiterEntries = 4096
	maxLimiterEntries     = 100000
	maxLimiterInputBytes  = 1024
)

type limiterClock func() time.Time

type limiterEntry struct {
	count   int
	expires time.Time
	touched time.Time
}

// limiterKey never retains account, address, or session input. The digest is
// fixed-size and the dimension is an internal enum, so map memory is bounded
// independently of attacker-controlled value length.
type limiterKey struct {
	dimension uint8
	digest    [sha256.Size]byte
}

const (
	limiterDimensionInvalid uint8 = iota
	limiterDimensionLoginAccount
	limiterDimensionLoginIP
	limiterDimensionRefreshIP
	limiterDimensionRefreshFamily
)

// LimiterOption configures only local deterministic behavior; this limiter is
// intentionally in-process and is not a distributed/session persistence layer.
type LimiterOption func(*AbuseLimiter)

func WithLimiterClock(clock func() time.Time) LimiterOption {
	return func(l *AbuseLimiter) {
		if clock != nil {
			l.now = clock
		}
	}
}
func WithLimiterMaxEntries(max int) LimiterOption {
	return func(l *AbuseLimiter) {
		if max > 0 && max < maxLimiterEntries {
			l.maxEntries = max
		}
	}
}

type AbuseLimiter struct {
	mu         sync.Mutex
	now        limiterClock
	maxEntries int
	entries    map[limiterKey]limiterEntry
}

func NewAbuseLimiter(options ...LimiterOption) *AbuseLimiter {
	l := &AbuseLimiter{now: time.Now, maxEntries: defaultLimiterEntries, entries: make(map[limiterKey]limiterEntry)}
	for _, option := range options {
		if option != nil {
			option(l)
		}
	}
	return l
}

// AllowLogin checks both normalized account and authoritative client IP. A
// caller records a failed attempt only after authentication failure.
func (l *AbuseLimiter) AllowLogin(account, ip string) bool {
	normalizedAccount, accountOK := normalizeAccount(account)
	normalizedIP, ipOK := normalizeIP(ip)
	return l.allow([]limiterKey{l.keyBounded("login-account", normalizedAccount, accountOK), l.keyBounded("login-ip", normalizedIP, ipOK)}, loginAccountLimit, loginIPLimit)
}
func (l *AbuseLimiter) RecordLoginFailure(account, ip string) {
	normalizedAccount, accountOK := normalizeAccount(account)
	normalizedIP, ipOK := normalizeIP(ip)
	l.record([]entryIncrement{{l.keyBounded("login-account", normalizedAccount, accountOK), loginAccountLimit}, {l.keyBounded("login-ip", normalizedIP, ipOK), loginIPLimit}})
}

func (l *AbuseLimiter) AllowRefresh(ip, sessionFamily string) bool {
	normalizedIP, ipOK := normalizeIP(ip)
	normalizedFamily, familyOK := normalizeLimiterInput(sessionFamily)
	return l.allow([]limiterKey{l.keyBounded("refresh-ip", normalizedIP, ipOK), l.keyBounded("refresh-family", normalizedFamily, familyOK)}, refreshIPLimit, refreshFamilyLimit)
}
func (l *AbuseLimiter) RecordRefreshAttempt(ip, sessionFamily string) {
	normalizedIP, ipOK := normalizeIP(ip)
	normalizedFamily, familyOK := normalizeLimiterInput(sessionFamily)
	l.record([]entryIncrement{{l.keyBounded("refresh-ip", normalizedIP, ipOK), refreshIPLimit}, {l.keyBounded("refresh-family", normalizedFamily, familyOK), refreshFamilyLimit}})
}

// LoginFailureAccepted and RefreshAttemptAccepted combine check and record for
// callers that do not need to distinguish the two phases. They do not reveal
// whether an account exists.
func (l *AbuseLimiter) LoginFailureAccepted(account, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked()
	normalizedAccount, accountOK := normalizeAccount(account)
	normalizedIP, ipOK := normalizeIP(ip)
	keys := []limiterKey{l.keyBounded("login-account", normalizedAccount, accountOK), l.keyBounded("login-ip", normalizedIP, ipOK)}
	if !l.allowLocked(keys, loginAccountLimit, loginIPLimit) {
		return false
	}
	l.incrementLocked(keys, []int{loginAccountLimit, loginIPLimit})
	return true
}
func (l *AbuseLimiter) RefreshAttemptAccepted(ip, sessionFamily string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked()
	normalizedIP, ipOK := normalizeIP(ip)
	normalizedFamily, familyOK := normalizeLimiterInput(sessionFamily)
	keys := []limiterKey{l.keyBounded("refresh-ip", normalizedIP, ipOK), l.keyBounded("refresh-family", normalizedFamily, familyOK)}
	if !l.allowLocked(keys, refreshIPLimit, refreshFamilyLimit) {
		return false
	}
	l.incrementLocked(keys, []int{refreshIPLimit, refreshFamilyLimit})
	return true
}

type entryIncrement struct {
	key   limiterKey
	limit int
}

func (l *AbuseLimiter) allow(keys []limiterKey, limits ...int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked()
	return l.allowLocked(keys, limits...)
}
func (l *AbuseLimiter) allowLocked(keys []limiterKey, limits ...int) bool {
	for i, key := range keys {
		if i >= len(limits) {
			break
		}
		if entry, ok := l.entries[key]; ok && entry.count >= limits[i] {
			return false
		}
	}
	return true
}
func (l *AbuseLimiter) record(increments []entryIncrement) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked()
	keys := make([]limiterKey, len(increments))
	limits := make([]int, len(increments))
	for i, increment := range increments {
		keys[i], limits[i] = increment.key, increment.limit
	}
	l.incrementLocked(keys, limits)
}
func (l *AbuseLimiter) incrementLocked(keys []limiterKey, limits []int) {
	now := l.now()
	_ = limits // limits document the separate bounded policies at call sites.
	for i, key := range keys {
		if key.dimension == limiterDimensionInvalid {
			continue
		}
		if _, exists := l.entries[key]; !exists {
			l.ensureCapacityLocked()
		}
		entry := l.entries[key]
		limit := 0
		if i < len(limits) {
			limit = limits[i]
		}
		if limit > 0 && entry.count >= limit {
			continue
		}
		entry.count++
		entry.expires = now.Add(LimiterWindow)
		entry.touched = now
		l.entries[key] = entry
	}
}
func (l *AbuseLimiter) cleanupLocked() {
	now := l.now()
	for key, entry := range l.entries {
		if !now.Before(entry.expires) {
			delete(l.entries, key)
		}
	}
}
func (l *AbuseLimiter) ensureCapacityLocked() {
	if len(l.entries) < l.maxEntries {
		return
	}
	var oldest limiterKey
	var when time.Time
	for key, entry := range l.entries {
		if oldest.dimension == limiterDimensionInvalid || entry.touched.Before(when) {
			oldest, when = key, entry.touched
		}
	}
	if oldest.dimension != limiterDimensionInvalid {
		delete(l.entries, oldest)
	}
}
func (l *AbuseLimiter) key(kind, value string) limiterKey {
	return l.keyBounded(kind, value, true)
}

func (l *AbuseLimiter) keyBounded(kind, value string, valid bool) limiterKey {
	if !valid || len(value) > maxLimiterInputBytes {
		return limiterKey{}
	}
	var dimension uint8
	switch kind {
	case "login-account":
		dimension = limiterDimensionLoginAccount
	case "login-ip":
		dimension = limiterDimensionLoginIP
	case "refresh-ip":
		dimension = limiterDimensionRefreshIP
	case "refresh-family":
		dimension = limiterDimensionRefreshFamily
	default:
		return limiterKey{}
	}
	return limiterKey{dimension: dimension, digest: sha256.Sum256([]byte(value))}
}

func normalizeLimiterInput(value string) (string, bool) {
	if len(value) > maxLimiterInputBytes {
		return "", false
	}
	value = strings.TrimSpace(value)
	if len(value) > maxLimiterInputBytes {
		return "", false
	}
	return value, true
}
func normalizeAccount(account string) (string, bool) {
	if len(account) > maxLimiterInputBytes {
		return "", false
	}
	account = strings.ToLower(strings.TrimSpace(account))
	if len(account) > maxLimiterInputBytes {
		return "", false
	}
	return account, true
}
func normalizeIP(value string) (string, bool) {
	if len(value) > maxLimiterInputBytes {
		return "", false
	}
	value = strings.TrimSpace(value)
	if ip := net.ParseIP(value); ip != nil {
		return ip.String(), true
	}
	return value, true
}

// Len is intentionally diagnostic only and exposes no key material.
func (l *AbuseLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked()
	return len(l.entries)
}
