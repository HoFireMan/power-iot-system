package security

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLimiterBoundsOversizedInputsAndCardinality(t *testing.T) {
	now := time.Unix(100, 0)
	l := NewAbuseLimiter(WithLimiterClock(func() time.Time { return now }), WithLimiterMaxEntries(3))
	if oversized := l.key("generic", strings.Repeat("x", maxLimiterInputBytes+1)); oversized.dimension != limiterDimensionInvalid {
		t.Fatal("oversized generic key was retained")
	}
	if !l.LoginFailureAccepted(strings.Repeat("a", maxLimiterInputBytes+1), "192.0.2.1") {
		t.Fatal("oversized account should not suppress the independent IP policy")
	}
	if got := l.Len(); got != 1 {
		t.Fatalf("oversized account retained a key: len=%d", got)
	}
	if !l.RefreshAttemptAccepted("192.0.2.2", strings.Repeat("s", maxLimiterInputBytes+1)) {
		t.Fatal("oversized session should not suppress the independent IP policy")
	}
	if got := l.Len(); got > 3 {
		t.Fatalf("entry cardinality exceeded configured maximum: %d", got)
	}
	for i := 0; i < 20; i++ {
		if !l.LoginFailureAccepted("account-"+strconv.Itoa(i), "198.51.100."+strconv.Itoa(i+1)) {
			t.Fatalf("entry %d unexpectedly blocked", i)
		}
	}
	if got := l.Len(); got > 3 {
		t.Fatalf("entry cardinality exceeded configured maximum: %d", got)
	}
	now = now.Add(LimiterWindow + time.Second)
	if got := l.Len(); got != 0 {
		t.Fatalf("expired entries remain: %d", got)
	}
}

func TestLimiterLoginThresholds(t *testing.T) {
	l := NewAbuseLimiter()
	for i := 0; i < loginAccountLimit; i++ {
		if !l.LoginFailureAccepted("same-account", "203.0.113."+strconv.Itoa(i+1)) {
			t.Fatalf("account attempt %d blocked", i)
		}
	}
	if l.LoginFailureAccepted("same-account", "203.0.113.20") {
		t.Fatal("sixth account attempt accepted")
	}

	l = NewAbuseLimiter()
	for i := 0; i < loginIPLimit; i++ {
		if !l.LoginFailureAccepted("account-"+strconv.Itoa(i), "198.51.100.10") {
			t.Fatalf("IP attempt %d blocked", i)
		}
	}
	if l.LoginFailureAccepted("new-account", "198.51.100.10") {
		t.Fatal("21st IP attempt accepted")
	}
}

func TestLimiterRefreshThresholds(t *testing.T) {
	l := NewAbuseLimiter()
	for i := 0; i < refreshIPLimit; i++ {
		if !l.RefreshAttemptAccepted("192.0.2.10", "family-"+strconv.Itoa(i)) {
			t.Fatalf("refresh IP attempt %d blocked", i)
		}
	}
	if l.RefreshAttemptAccepted("192.0.2.10", "new-family") {
		t.Fatal("31st refresh IP attempt accepted")
	}

	l = NewAbuseLimiter()
	for i := 0; i < refreshFamilyLimit; i++ {
		if !l.RefreshAttemptAccepted("192.0.2."+strconv.Itoa(i+1), "same-family") {
			t.Fatalf("refresh family attempt %d blocked", i)
		}
	}
	if l.RefreshAttemptAccepted("192.0.2.100", "same-family") {
		t.Fatal("11th refresh family attempt accepted")
	}
}
