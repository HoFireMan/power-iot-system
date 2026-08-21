package migrations

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"testing"
)

func TestCR1TargetPreimageUsesFrozenFieldOrderAndWidths(t *testing.T) {
	identity := cr1TargetIdentity{
		database: "db", schema: "public", serverAddress: "127.0.0.1", serverPort: 55434,
		migrationSchema: "public", migrationTable: "schema_migrations", clusterID: 0x0102030405060708,
	}
	preimage, err := encodeCR1TargetPreimage(identity)
	if err != nil {
		t.Fatal(err)
	}
	header := append([]byte(cr1TargetPrefix), 0x00, 0x01, 0x00, 0x07)
	if !bytes.HasPrefix(preimage, header) {
		t.Fatalf("preimage prefix/header=%x", preimage[:len(cr1TargetPrefix)+5])
	}
	offset := len(cr1TargetPrefix) + 4
	fields := []struct {
		tag  byte
		want []byte
	}{
		{cr1TargetDatabase, []byte("db")},
		{cr1TargetSchema, []byte("public")},
		{cr1TargetServerAddress, []byte("127.0.0.1")},
		{cr1TargetServerPort, []byte{0xd8, 0x8a}},
		{cr1TargetMigrationSchema, []byte("public")},
		{cr1TargetMigrationTable, []byte("schema_migrations")},
		{cr1TargetClusterID, []byte{1, 2, 3, 4, 5, 6, 7, 8}},
	}
	for _, field := range fields {
		tag, want := field.tag, field.want
		if preimage[offset] != tag || preimage[offset+1] != cr1TargetValue {
			t.Fatalf("field tag/state at %d=%x/%x, want tag=%d state=%d", offset, preimage[offset], preimage[offset+1], tag, cr1TargetValue)
		}
		length := binary.BigEndian.Uint32(preimage[offset+2 : offset+6])
		if int(length) != len(want) || !bytes.Equal(preimage[offset+6:offset+6+int(length)], want) {
			t.Fatalf("field %d bytes=%x want=%x", tag, preimage[offset+6:offset+6+int(length)], want)
		}
		offset += 6 + int(length)
	}
	if offset != len(preimage) {
		t.Fatalf("unparsed preimage bytes=%d", len(preimage)-offset)
	}
	digest := sha256.Sum256(preimage)
	if len(digest) != 32 {
		t.Fatal("target digest must be raw SHA-256")
	}
}

func TestCanonicalCR1TargetFieldsFailClosed(t *testing.T) {
	if _, err := canonicalCR1Identifier("e\u0301", "database"); !errors.Is(err, ErrD1LTargetIdentity) {
		t.Fatalf("non-NFC identifier error=%v", err)
	}
	if _, err := canonicalCR1Address(sql.NullString{String: "127.0.0.01", Valid: true}); !errors.Is(err, ErrD1LTargetIdentity) {
		t.Fatalf("non-canonical address error=%v", err)
	}
	if _, err := canonicalCR1ClusterID("001"); !errors.Is(err, ErrD1LTargetIdentity) {
		t.Fatalf("non-canonical cluster error=%v", err)
	}
}
