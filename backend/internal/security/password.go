// Package security contains the small authentication and request-boundary
// primitives used by the backend. It intentionally does not persist or
// rotate credentials.
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonAlgorithm          = "argon2id"
	argonVersion            = 19
	argonMemoryKiB   uint32 = 65536
	argonIterations  uint32 = 3
	argonParallelism uint8  = 1
	argonSaltBytes          = 16
	argonKeyBytes           = 32
	MaxPasswordBytes        = 1024
	// PHC strings and their encoded fields are bounded before parsing or
	// decoding. The exact field sizes are part of the Argon2 policy.
	maxPasswordHashBytes = 128
	maxEncodedSaltBytes  = 22 // base64.RawStdEncoding.EncodedLen(16)
	maxEncodedKeyBytes   = 43 // base64.RawStdEncoding.EncodedLen(32)
	// Verification bounds are deliberately finite. They are checked before
	// calling argon2, so an attacker cannot make verification allocate freely.
	maxVerifyMemoryKiB   uint32 = 262144
	maxVerifyIterations  uint32 = 10
	maxVerifyParallelism uint8  = 4
)

var (
	ErrPasswordTooLong         = errors.New("password exceeds maximum length")
	ErrMalformedPasswordHash   = errors.New("malformed password hash")
	ErrUnsupportedPasswordHash = errors.New("unsupported password hash")
	ErrPasswordHashBounds      = errors.New("password hash parameters exceed verification bounds")
)

// HashPassword creates an Argon2id v=19 PHC string. Password bytes are used
// exactly as supplied; no normalization or truncation is performed.
func HashPassword(password []byte) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey(password, salt, argonIterations, argonMemoryKiB, argonParallelism, argonKeyBytes)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$%s$v=%d$m=%d,t=%d,p=%d$%s$%s", argonAlgorithm, argonVersion,
		argonMemoryKiB, argonIterations, argonParallelism, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword verifies an Argon2id PHC string. A wrong password returns
// (false, nil); malformed or unsafe encodings return an error.
func VerifyPassword(password []byte, encoded string) (bool, error) {
	if len(password) > MaxPasswordBytes {
		return false, ErrPasswordTooLong
	}
	params, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	key := argon2.IDKey(password, params.salt, params.iterations, params.memoryKiB, params.parallelism, argonKeyBytes)
	return subtle.ConstantTimeCompare(key, params.key) == 1, nil
}

// NeedsRehash reports whether a valid PHC string differs from the current
// policy. Invalid strings are treated as needing replacement.
func NeedsRehash(encoded string) bool {
	params, err := parsePasswordHash(encoded)
	if err != nil {
		return true
	}
	return params.memoryKiB != argonMemoryKiB || params.iterations != argonIterations ||
		params.parallelism != argonParallelism || len(params.salt) != argonSaltBytes || len(params.key) != argonKeyBytes
}

type passwordParams struct {
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
	salt        []byte
	key         []byte
}

func parsePasswordHash(encoded string) (passwordParams, error) {
	var out passwordParams
	// Check the complete representation before Split can copy or retain any
	// attacker-controlled proportional input.
	if len(encoded) > maxPasswordHashBytes {
		return out, ErrPasswordHashBounds
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != argonAlgorithm {
		if len(parts) > 1 && parts[1] != argonAlgorithm {
			return out, ErrUnsupportedPasswordHash
		}
		return out, ErrMalformedPasswordHash
	}
	if parts[2] != "v=19" {
		return out, ErrUnsupportedPasswordHash
	}
	// Reject wrong-sized fields before touching the Base64 decoder. In
	// particular, valid but oversized fields cannot induce a large decode
	// allocation.
	if len(parts[4]) != maxEncodedSaltBytes || len(parts[5]) != maxEncodedKeyBytes {
		if len(parts[4]) > maxEncodedSaltBytes || len(parts[5]) > maxEncodedKeyBytes {
			return out, ErrPasswordHashBounds
		}
		return out, ErrMalformedPasswordHash
	}
	seen := map[string]bool{}
	for _, item := range strings.Split(parts[3], ",") {
		kv := strings.Split(item, "=")
		if len(kv) != 2 || seen[kv[0]] || (kv[0] != "m" && kv[0] != "t" && kv[0] != "p") {
			return out, ErrMalformedPasswordHash
		}
		seen[kv[0]] = true
		n, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil || n == 0 {
			return out, ErrMalformedPasswordHash
		}
		switch kv[0] {
		case "m":
			out.memoryKiB = uint32(n)
		case "t":
			out.iterations = uint32(n)
		case "p":
			if n > 255 {
				return out, ErrMalformedPasswordHash
			}
			out.parallelism = uint8(n)
		}
	}
	if len(seen) != 3 || out.memoryKiB < 8 || out.iterations == 0 || out.parallelism == 0 {
		return out, ErrMalformedPasswordHash
	}
	// These checks precede all decoding and, critically, all Argon2 allocation.
	if out.memoryKiB > maxVerifyMemoryKiB || out.iterations > maxVerifyIterations || out.parallelism > maxVerifyParallelism {
		return out, ErrPasswordHashBounds
	}
	dec := base64.RawStdEncoding
	// DecodedLen is checked before allocating destination buffers. The field
	// bounds above make these allocations fixed-size even for hostile input.
	saltLen := dec.DecodedLen(len(parts[4]))
	keyLen := dec.DecodedLen(len(parts[5]))
	if saltLen != argonSaltBytes || keyLen != argonKeyBytes || strings.Contains(parts[4], "=") || strings.Contains(parts[5], "=") {
		return passwordParams{}, ErrMalformedPasswordHash
	}
	out.salt = make([]byte, saltLen)
	if n, err := dec.Decode(out.salt, []byte(parts[4])); err != nil || n != saltLen || base64.RawStdEncoding.EncodeToString(out.salt) != parts[4] {
		return passwordParams{}, ErrMalformedPasswordHash
	}
	out.key = make([]byte, keyLen)
	if n, err := dec.Decode(out.key, []byte(parts[5])); err != nil || n != keyLen || base64.RawStdEncoding.EncodeToString(out.key) != parts[5] {
		return passwordParams{}, ErrMalformedPasswordHash
	}
	return out, nil
}
