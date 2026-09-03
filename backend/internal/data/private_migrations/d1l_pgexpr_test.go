package migrations

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestD1LPGExprMembershipSerialization(t *testing.T) {
	got, err := SerializeD1LPGExpr("status = ANY (ARRAY['ISSUED'::text, 'ACTIVE'::text]::text[])")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !bytes.HasPrefix(got, []byte("D1L-PGEXPR-V1\x00")) || got[len(got)-1] != 0xff {
		t.Fatal("invalid framing")
	}
	if !bytes.Contains(got, []byte("pg_catalog.text")) {
		t.Fatal("missing list type")
	}
}
func TestD1LPGExprRejectsAlternateMembership(t *testing.T) {
	for _, expression := range []string{
		"status <> ANY (ARRAY['ISSUED']::text[])",
		"status = ANY (ARRAY['ISSUED']::varchar[])",
		"status = ANY (ARRAY[]::text[])",
		"status = ANY (ARRAY['ISSUED'::text])",
		"(status = ANY (ARRAY['ISSUED'::text]::text[])",
	} {
		if _, err := SerializeD1LPGExpr(expression); err == nil {
			t.Errorf("accepted %q", expression)
		}
	}
}
func TestD1LPGExprMembershipCanonicalBytesAndRequiredCast(t *testing.T) {
	got, err := SerializeD1LPGExpr("status = ANY (ARRAY['OPEN'::text]::text[])")
	if err != nil {
		t.Fatal(err)
	}
	const want = "44314c2d5047455850522d5631004c000000347374617475730070675f636174616c6f672e746578740070675f636174616c6f672e224322003d0000000001000000044f50454eff"
	if hex.EncodeToString(got) != want {
		t.Fatalf("canonical bytes=%x want=%s", got, want)
	}
	for _, expression := range []string{
		"status = ANY (ARRAY['OPEN'::text])",
		"status = ANY (ARRAY['OPEN'::text]::varchar[])",
		"status <> ANY (ARRAY['OPEN'::text]::text[])",
	} {
		if _, err := SerializeD1LPGExpr(expression); err == nil {
			t.Errorf("accepted unsupported membership expression %q", expression)
		}
	}
}

func TestD1LPGExprDirectTextCastIsPreserved(t *testing.T) {
	got, err := SerializeD1LPGExpr("status = 'OPEN'::text")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("OPEN")) || !bytes.Contains(got, []byte("pg_catalog.text")) {
		t.Fatal("direct cast was not represented")
	}
}

func TestD1LPGExprMembershipPreservesLHSAndListOrder(t *testing.T) {
	status, err := SerializeD1LPGExpr("status = ANY (ARRAY['OPEN'::text, 'ACTIVE'::text]::text[])")
	if err != nil {
		t.Fatal(err)
	}
	boundaryName, err := SerializeD1LPGExpr("boundary_name = ANY (ARRAY['OPEN'::text, 'ACTIVE'::text]::text[])")
	if err != nil {
		t.Fatal(err)
	}
	// The old list-only representation was identical after dropping the
	// LHS. Keep this assertion as a regression witness for the original
	// collision while requiring the repaired full values to differ.
	prefix := []byte("D1L-PGEXPR-V1\x00")
	legacyPayload := func(serialized []byte) []byte {
		if !bytes.HasPrefix(serialized, prefix) || len(serialized) < len(prefix)+5 || serialized[len(prefix)] != 'L' {
			t.Fatal("invalid membership framing")
		}
		payloadLength := int(binary.BigEndian.Uint32(serialized[len(prefix)+1 : len(prefix)+5]))
		payloadStart := len(prefix) + 5
		payload := serialized[payloadStart : payloadStart+payloadLength]
		lhsEnd := bytes.IndexByte(payload, 0)
		if lhsEnd < 0 {
			t.Fatal("membership LHS separator missing")
		}
		return payload[lhsEnd+1:]
	}
	if !bytes.Equal(legacyPayload(status), legacyPayload(boundaryName)) {
		t.Fatal("regression witness changed: old list-only membership values should collide")
	}
	if bytes.Equal(status, boundaryName) {
		t.Fatal("membership LHS collision: distinct columns serialized identically")
	}
	if !bytes.Contains(status, []byte("status\x00")) || !bytes.Contains(boundaryName, []byte("boundary_name\x00")) {
		t.Fatal("membership LHS is absent from the canonical value")
	}
	reordered, err := SerializeD1LPGExpr("status = ANY (ARRAY['ACTIVE'::text, 'OPEN'::text]::text[])")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(status, reordered) {
		t.Fatal("membership list ordering was discarded")
	}
}

func TestD1LPGExprMembershipRejectsWrongCastAndShape(t *testing.T) {
	for _, expression := range []string{
		"status = ANY (ARRAY['OPEN'::varchar]::text[])",
		"status = ANY (ARRAY['OPEN'::text]::varchar[])",
		"status = ANY (ARRAY['OPEN']::text[])",
		"status <> ANY (ARRAY['OPEN'::text]::text[])",
		"status = ANY (ARRAY[['OPEN'::text]]::text[])",
	} {
		if _, err := SerializeD1LPGExpr(expression); err == nil {
			t.Errorf("accepted unsupported membership shape %q", expression)
		}
	}
}
