package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

var ErrRandomness = errors.New("secure randomness unavailable")

// PublicError is the sole public error wire representation. HTTP/domain
// mapping is intentionally outside this package.
type PublicError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

func (e PublicError) MarshalJSON() ([]byte, error) {
	type wire struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"requestId"`
	}
	return json.Marshal(wire{e.Code, e.Message, e.RequestID})
}

func NewPublicError(code, message, requestID string) PublicError {
	return PublicError{Code: code, Message: message, RequestID: EnsureRequestID(requestID)}
}

// NewRequestID returns the supplied canonical UUIDv4 or replaces malformed
// input with a fresh UUIDv4. It never echoes malformed request IDs.
func NewRequestID(input string) string {
	if IsCanonicalUUIDv4(input) {
		return input
	}
	id, err := uuid.NewRandom()
	if err != nil { // uuid.NewRandom uses crypto/rand; preserve a safe failure signal.
		return ""
	}
	return id.String()
}

func EnsureRequestID(input string) string { return NewRequestID(input) }

func RequestIDFromHeader(headers http.Header) string {
	return NewRequestID(headers.Get("X-Request-ID"))
}

func IsCanonicalUUIDv4(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value && id.Version() == 4 && id.Variant() == uuid.RFC4122
}

// GenerateRefreshToken creates an opaque 32-byte token encoded as unpadded
// base64url. Only its digest should be retained by future persistence code.
func GenerateRefreshToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", ErrRandomness
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func RefreshTokenDigest(token string) [32]byte { return sha256.Sum256([]byte(token)) }
func DigestRefreshToken(token string) [32]byte { return RefreshTokenDigest(token) }

func IsRefreshTokenEncoding(token string) bool {
	if len(token) != 43 || strings.Contains(token, "=") {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32
}
