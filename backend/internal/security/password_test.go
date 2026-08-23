package security

import (
	"strings"
	"testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword([]byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	const expectedPHCPrefix = "$argon2id$v=19$m=65536,t=3,p=1$"
	if !strings.HasPrefix(hash, expectedPHCPrefix) {
		t.Fatalf("unexpected PHC structure: prefix=%t length=%d", strings.HasPrefix(hash, expectedPHCPrefix), len(hash))
	}
	if ok, err := VerifyPassword([]byte("correct horse"), hash); err != nil || !ok {
		t.Fatalf("verify: %v %v", ok, err)
	}
	if ok, err := VerifyPassword([]byte("wrong"), hash); err != nil || ok {
		t.Fatalf("wrong password: %v %v", ok, err)
	}
	if NeedsRehash(hash) {
		t.Fatal("current hash needs rehash")
	}
}
func TestPasswordHashRejectsOversizeAndUnsafeEnvelope(t *testing.T) {
	if _, err := HashPassword(make([]byte, MaxPasswordBytes+1)); err == nil {
		t.Fatal("oversize accepted")
	}
	base, _ := HashPassword([]byte("p"))
	unsafe := strings.Replace(base, "m=65536", "m=429496729", 1)
	if _, err := VerifyPassword([]byte("p"), unsafe); err == nil {
		t.Fatal("unsafe memory accepted")
	}
	if _, err := VerifyPassword([]byte("p"), strings.Replace(base, "argon2id", "argon2i", 1)); err == nil {
		t.Fatal("alternate algorithm accepted")
	}
	if _, err := VerifyPassword([]byte("p"), strings.Replace(base, "v=19", "v=18", 1)); err == nil {
		t.Fatal("alternate version accepted")
	}
	if _, err := VerifyPassword([]byte("p"), strings.Replace(base, "t=3", "t=11", 1)); err == nil {
		t.Fatal("excessive iterations accepted")
	}
	if _, err := VerifyPassword([]byte("p"), strings.Replace(base, "p=1", "p=5", 1)); err == nil {
		t.Fatal("excessive parallelism accepted")
	}
}

func TestPasswordHashRejectsOversizedPHCFieldsBeforeVerification(t *testing.T) {
	base, err := HashPassword([]byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(base, "$")
	// This is a valid, unpadded Base64 field but is larger than the fixed salt
	// representation. It must be rejected without entering Argon2.
	parts[4] = strings.Repeat("A", 1000)
	if _, err := VerifyPassword([]byte("p"), strings.Join(parts, "$")); err == nil {
		t.Fatal("oversized valid Base64 field accepted")
	}
	// Exercise the encoded-field bound independently while staying inside the
	// complete PHC bound.
	parts = strings.Split(base, "$")
	parts[4] = strings.Repeat("A", maxEncodedSaltBytes+1)
	if _, err := VerifyPassword([]byte("p"), strings.Join(parts, "$")); err == nil {
		t.Fatal("oversized salt field accepted")
	}
	parts = strings.Split(base, "$")
	parts[5] = strings.Repeat("A", maxEncodedKeyBytes+1)
	if _, err := VerifyPassword([]byte("p"), strings.Join(parts, "$")); err == nil {
		t.Fatal("oversized key field accepted")
	}
	parts = strings.Split(base, "$")
	// Raw Base64 decoders can accept non-zero discarded trailing bits; PHC
	// verification requires the canonical re-encoding.
	parts[4] = parts[4][:maxEncodedSaltBytes-1] + "B"
	if _, err := VerifyPassword([]byte("p"), strings.Join(parts, "$")); err == nil {
		t.Fatal("noncanonical Base64 accepted")
	}
}
