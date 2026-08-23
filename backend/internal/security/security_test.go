package security

import (
	"crypto/ed25519"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTKeyringAndValidation(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(nil)
	keyring, err := NewKeyring(SigningKey{KID: "active", Private: private}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	keyring = keyring.WithClock(func() time.Time { return now })
	raw, err := keyring.IssueAccessToken("user-1", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := keyring.VerifyAccessToken(raw)
	if err != nil || claims.Subject != "user-1" || claims.SID != "session-1" {
		t.Fatalf("claims validation failed: subject_ok=%t sid_ok=%t err=%v", claims.Subject == "user-1", claims.SID == "session-1", err)
	}
	if _, err := keyring.VerifyAccessTokenAt(raw, now.Add(11*time.Minute)); err == nil {
		t.Fatal("expired token accepted")
	}
	_, retiringPrivate, _ := ed25519.GenerateKey(nil)
	retiringPub := retiringPrivate.Public().(ed25519.PublicKey)
	retiring, err := NewKeyring(SigningKey{KID: "active", Private: private}, []VerificationKey{{KID: "retiring", Public: retiringPub}})
	if err != nil {
		t.Fatal(err)
	}
	retiringToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, AccessClaims{SID: "session-1", RegisteredClaims: jwt.RegisteredClaims{Issuer: JWTIssuer, Audience: jwt.ClaimStrings{JWTAudience}, Subject: "user-1", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL))}})
	retiringToken.Header["kid"] = "retiring"
	retiringRaw, err := retiringToken.SignedString(retiringPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retiring.VerifyAccessTokenAt(retiringRaw, now); err != nil {
		t.Fatalf("retiring key: %v", err)
	}
	if _, err := keyring.IssueAccessToken("", "session"); err == nil {
		t.Fatal("empty subject accepted")
	}
}
func TestJWTRejectsRequiredInvalidForms(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(nil)
	keyring, err := NewKeyring(SigningKey{KID: "active", Private: private}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	keyring = keyring.WithClock(func() time.Time { return now })
	makeToken := func(method jwt.SigningMethod, kid string, claims jwt.Claims) string {
		token := jwt.NewWithClaims(method, claims)
		token.Header["kid"] = kid
		var signingKey any = private
		if method == jwt.SigningMethodHS256 {
			signingKey = []byte("wrong-algorithm-key")
		}
		raw, signingErr := token.SignedString(signingKey)
		if signingErr != nil {
			t.Fatal(signingErr)
		}
		return raw
	}
	base := func() AccessClaims {
		return AccessClaims{SID: "sid", RegisteredClaims: jwt.RegisteredClaims{Issuer: JWTIssuer, Audience: jwt.ClaimStrings{JWTAudience}, Subject: "sub", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL))}}
	}
	valid := makeToken(jwt.SigningMethodEdDSA, "active", base())
	if _, err := keyring.VerifyAccessTokenAt(valid, now); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		makeToken(jwt.SigningMethodHS256, "active", base()),
		makeToken(jwt.SigningMethodEdDSA, "unknown", base()),
		makeToken(jwt.SigningMethodEdDSA, "active", AccessClaims{SID: "sid", RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings{JWTAudience}, Subject: "sub", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL))}}),
		makeToken(jwt.SigningMethodEdDSA, "active", AccessClaims{SID: "", RegisteredClaims: jwt.RegisteredClaims{Issuer: JWTIssuer, Audience: jwt.ClaimStrings{JWTAudience}, Subject: "sub", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL))}}),
		makeToken(jwt.SigningMethodEdDSA, "active", AccessClaims{SID: "sid", RegisteredClaims: jwt.RegisteredClaims{Issuer: "wrong", Audience: jwt.ClaimStrings{JWTAudience}, Subject: "sub", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL))}}),
		makeToken(jwt.SigningMethodEdDSA, "active", AccessClaims{SID: "sid", RegisteredClaims: jwt.RegisteredClaims{Issuer: JWTIssuer, Audience: jwt.ClaimStrings{JWTAudience}, Subject: "sub", IssuedAt: jwt.NewNumericDate(now.Add(time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(11 * time.Minute))}}),
		makeToken(jwt.SigningMethodEdDSA, "active", AccessClaims{SID: "sid", RegisteredClaims: jwt.RegisteredClaims{Issuer: JWTIssuer, Audience: jwt.ClaimStrings{JWTAudience}, Subject: "sub", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL + time.Second))}}),
	}
	for i, raw := range cases {
		if _, err := keyring.VerifyAccessTokenAt(raw, now); err == nil {
			t.Fatalf("invalid token %d accepted", i)
		}
	}
	if _, err := keyring.VerifyAccessTokenAt(makeToken(jwt.SigningMethodEdDSA, "active", AccessClaims{SID: "sid", RegisteredClaims: jwt.RegisteredClaims{Issuer: JWTIssuer, Audience: jwt.ClaimStrings{JWTAudience}, Subject: "sub", IssuedAt: jwt.NewNumericDate(now.Add(-11 * time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(-time.Minute))}}), now); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestRefreshAndRequestID(t *testing.T) {
	token, err := GenerateRefreshToken()
	if err != nil || !IsRefreshTokenEncoding(token) {
		t.Fatalf("generated refresh token invalid: present=%t encoding=%t err=%v", token != "", IsRefreshTokenEncoding(token), err)
	}
	digest := DigestRefreshToken(token)
	if len(digest) != 32 {
		t.Fatal("bad digest")
	}
	if IsCanonicalUUIDv4("not-an-id") || !IsCanonicalUUIDv4(NewRequestID("bad")) {
		t.Fatal("request ID")
	}
	e := NewPublicError("bad_request", "invalid request", "bad")
	if e.RequestID == "bad" || e.Code == "" {
		t.Fatal(e)
	}
}
func TestTrustedProxyAndUntrustedForwarding(t *testing.T) {
	peer := net.ParseIP("203.0.113.9")
	for name, headers := range map[string]http.Header{
		"forwarded":       {"Forwarded": []string{"for=198.51.100.5"}},
		"x-forwarded-for": {"X-Forwarded-For": []string{"198.51.100.5"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ResolveClientIP(peer, headers, TrustedProxyConfig{}); !got.Equal(peer) {
				t.Fatalf("untrusted peer became %v", got)
			}
		})
	}

	config, err := NewTrustedProxyConfig([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveClientIP(peer, http.Header{"X-Forwarded-For": []string{"198.51.100.5"}}, config); !got.Equal(net.ParseIP("198.51.100.5")) {
		t.Fatalf("trusted peer got %v", got)
	}
}

func TestTrustedProxyForwardedChains(t *testing.T) {
	config, err := NewTrustedProxyConfig([]string{
		"192.0.2.0/24",   // configured intermediate proxy
		"203.0.113.0/24", // configured edge proxy
	})
	if err != nil {
		t.Fatal(err)
	}

	// Forwarded is ordered from the client toward the nearest proxy. Every
	// configured hop is trusted, so the first untrusted address is the client.
	peer := net.ParseIP("203.0.113.9")
	headers := http.Header{"Forwarded": []string{
		"for=198.51.100.5;proto=https, for=192.0.2.9, for=203.0.113.8",
	}}
	if got := ResolveClientIP(peer, headers, config); !got.Equal(net.ParseIP("198.51.100.5")) {
		t.Fatalf("multi-hop chain resolved to %v", got)
	}

	// A mixed chain stops at the nearest untrusted hop rather than trusting
	// addresses farther left in the header.
	mixed := http.Header{"X-Forwarded-For": []string{"198.51.100.5, 198.18.0.7, 203.0.113.8"}}
	if got := ResolveClientIP(peer, mixed, config); !got.Equal(net.ParseIP("198.18.0.7")) {
		t.Fatalf("mixed chain resolved to %v", got)
	}
}

func TestTrustedProxyMalformedForwardingUsesDirectPeer(t *testing.T) {
	config, err := NewTrustedProxyConfig([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	peer := net.ParseIP("203.0.113.9")
	cases := []http.Header{
		{"X-Forwarded-For": []string{"198.51.100.5, not-an-address"}},
		{"Forwarded": []string{"for=198.51.100.5, for=not-an-address"}},
	}
	for _, headers := range cases {
		if got := ResolveClientIP(peer, headers, config); !got.Equal(peer) {
			t.Fatalf("malformed forwarding became %v", got)
		}
	}
}

func TestTrustedProxyHasNoTrustAllDefault(t *testing.T) {
	config, err := NewTrustedProxyConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.Configured() {
		t.Fatal("empty configuration trusts proxies")
	}
	peer := net.ParseIP("127.0.0.1")
	headers := http.Header{"X-Forwarded-For": []string{"198.51.100.5"}}
	if got := ResolveClientIP(peer, headers, config); !got.Equal(peer) {
		t.Fatalf("default configuration trusted spoofed client %v", got)
	}
}
func TestLimiterThresholdAndExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	l := NewAbuseLimiter(WithLimiterClock(func() time.Time { return now }), WithLimiterMaxEntries(10))
	for i := 0; i < 5; i++ {
		if !l.LoginFailureAccepted(" User ", "192.0.2.1") {
			t.Fatalf("attempt %d blocked", i)
		}
	}
	if l.LoginFailureAccepted("user", "192.0.2.1") {
		t.Fatal("sixth attempt accepted")
	}
	now = now.Add(LimiterWindow + time.Second)
	if !l.LoginFailureAccepted("user", "192.0.2.1") {
		t.Fatal("expired attempt blocked")
	}
	if l.Len() == 0 {
		t.Fatal("expected active entries")
	}
}
