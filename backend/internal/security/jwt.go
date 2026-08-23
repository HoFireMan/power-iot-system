package security

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
)

const (
	JWTIssuer      = "power-iot-backend"
	JWTAudience    = "power-iot-mobile"
	AccessTokenTTL = 10 * time.Minute
	JWTClockSkew   = 30 * time.Second
)

var (
	ErrInvalidKeyring    = errors.New("invalid JWT keyring")
	ErrInvalidToken      = errors.New("invalid access token")
	ErrUnknownSigningKey = errors.New("unknown JWT signing key")
)

// SigningKey is the sole active Ed25519 signing key. Public is optional when
// constructing a keyring and is checked against Private when supplied.
type SigningKey struct {
	KID     string
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

// VerificationKey is a retiring public key. Retiring keys never sign tokens.
type VerificationKey struct {
	KID    string
	Public ed25519.PublicKey
}

// Keyring holds exactly one active signing key and zero or more retiring
// verification keys. It contains no fallback secret or default key.
type Keyring struct {
	active SigningKey
	keys   map[string]ed25519.PublicKey
	now    func() time.Time
}

// NewKeyring validates the key set and rejects duplicate key IDs.
func NewKeyring(active SigningKey, retiring []VerificationKey) (*Keyring, error) {
	if !validKID(active.KID) || len(active.Private) != ed25519.PrivateKeySize {
		return nil, ErrInvalidKeyring
	}
	derived := active.Private.Public().(ed25519.PublicKey)
	if len(active.Public) != 0 && !equalBytes(derived, active.Public) {
		return nil, ErrInvalidKeyring
	}
	keys := map[string]ed25519.PublicKey{active.KID: append(ed25519.PublicKey(nil), derived...)}
	for _, key := range retiring {
		if !validKID(key.KID) || len(key.Public) != ed25519.PublicKeySize || key.KID == active.KID {
			return nil, ErrInvalidKeyring
		}
		if _, exists := keys[key.KID]; exists {
			return nil, ErrInvalidKeyring
		}
		keys[key.KID] = append(ed25519.PublicKey(nil), key.Public...)
	}
	return &Keyring{active: SigningKey{KID: active.KID, Private: append(ed25519.PrivateKey(nil), active.Private...), Public: derived}, keys: keys, now: time.Now}, nil
}

// WithClock returns a copy using a controllable clock, useful for deterministic
// callers and tests without making wall-clock state global.
func (k *Keyring) WithClock(now func() time.Time) *Keyring {
	if now == nil {
		now = time.Now
	}
	copy := *k
	copy.now = now
	return &copy
}

// AccessClaims are deliberately limited to authentication identity. Shop,
// role, admin, and tenant authorization data do not belong in access tokens.
type AccessClaims struct {
	SID string `json:"sid"`
	jwt.RegisteredClaims
}

// IssueAccessToken signs an access token with mandatory identity claims.
func (k *Keyring) IssueAccessToken(subject, sessionID string) (string, error) {
	return k.IssueAccessTokenAt(subject, sessionID, k.clock())
}

func (k *Keyring) IssueAccessTokenAt(subject, sessionID string, now time.Time) (string, error) {
	if k == nil || !validID(subject) || !validID(sessionID) {
		return "", ErrInvalidToken
	}
	if now.IsZero() {
		now = time.Now()
	}
	exp := now.Add(AccessTokenTTL)
	claims := AccessClaims{SID: sessionID, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: JWTIssuer, Audience: jwt.ClaimStrings{JWTAudience}, Subject: subject,
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp),
	}}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = k.active.KID
	return token.SignedString(k.active.Private)
}

// VerifyAccessToken validates algorithm, key ID, signature, registered claims,
// and the sid identity claim. It uses the keyring's clock.
func (k *Keyring) VerifyAccessToken(raw string) (AccessClaims, error) {
	return k.VerifyAccessTokenAt(raw, k.clock())
}

func (k *Keyring) VerifyAccessTokenAt(raw string, now time.Time) (AccessClaims, error) {
	var claims AccessClaims
	if k == nil || strings.TrimSpace(raw) == "" {
		return claims, ErrInvalidToken
	}
	if now.IsZero() {
		now = time.Now()
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithoutClaimsValidation())
	token, err := parser.ParseWithClaims(raw, &claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodEdDSA || token.Method.Alg() != "EdDSA" {
			return nil, ErrInvalidToken
		}
		kid, ok := token.Header["kid"].(string)
		if !ok || !validKID(kid) {
			return nil, ErrUnknownSigningKey
		}
		key, exists := k.keys[kid]
		if !exists {
			return nil, ErrUnknownSigningKey
		}
		return key, nil
	})
	if err != nil || token == nil || !token.Valid {
		return claims, ErrInvalidToken
	}
	if !validID(claims.Issuer) || claims.Issuer != JWTIssuer || len(claims.Audience) != 1 || claims.Audience[0] != JWTAudience ||
		!validID(claims.Subject) || !validID(claims.SID) || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return claims, ErrInvalidToken
	}
	iat, exp := claims.IssuedAt.Time, claims.ExpiresAt.Time
	if iat.After(now.Add(JWTClockSkew)) || !iat.Before(exp) || exp.Sub(iat) > AccessTokenTTL || now.After(exp.Add(JWTClockSkew)) {
		return claims, ErrInvalidToken
	}
	return claims, nil
}

func (k *Keyring) clock() time.Time {
	if k != nil && k.now != nil {
		return k.now()
	}
	return time.Now()
}

func validKID(kid string) bool { return validID(kid) && len(kid) <= 128 }

func validID(value string) bool {
	if value == "" || len(value) > 255 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
