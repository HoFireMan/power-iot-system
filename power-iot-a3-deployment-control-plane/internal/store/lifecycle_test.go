package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestWireDecodersRejectAmbiguousValues(t *testing.T) {
	if _, err := decodeNonce(""); err == nil {
		t.Fatal("empty nonce accepted")
	}
	if _, err := decodeNonce("AQ"); err == nil {
		t.Fatal("short nonce accepted")
	}
	if _, err := secretHash("AQ"); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestConstantEquality(t *testing.T) {
	if !equal([]byte{1, 2}, []byte{1, 2}) {
		t.Fatal("equal values rejected")
	}
	if equal([]byte{1}, []byte{1, 2}) || equal([]byte{1}, []byte{2}) {
		t.Fatal("different values accepted")
	}
}

func TestEnvelopeRoundTripAndStrictParsing(t *testing.T) {
	aid := uuid.New()
	nonce := []byte("0123456789abcdef")
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}
	raw := EncodeEnvelope(aid, 7, nonce, secret)
	got, err := parseEnvelope(raw)
	if err != nil || got.authID != aid || got.epoch != 7 || !equal(got.nonce, nonce) || !equal(got.secret, secret) {
		t.Fatalf("envelope round trip failed: %v", err)
	}
	if _, err = parseEnvelope(raw + "="); err == nil {
		t.Fatal("non-canonical envelope accepted")
	}
	if _, err = parseEnvelope("d1lba.v1.bad.bad.bad.bad"); err == nil {
		t.Fatal("malformed envelope accepted")
	}
}

func TestIssueAndInspectWireNeverContainRawSecret(t *testing.T) {
	body, err := json.Marshal(IssueResult{AuthorizationID: "a", State: "ISSUED", Envelope: "d1lba.v1.x.y.z.s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"secret"`) || strings.Contains(string(body), `"verifier"`) {
		t.Fatalf("issue wire leaked raw secret fields: %s", body)
	}
	body, err = json.Marshal(InspectResult{AuthorizationID: "a", State: "ISSUED"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret") {
		t.Fatalf("inspect wire leaked secret field: %s", body)
	}
}
