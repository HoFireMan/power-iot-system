package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/golang-migrate/migrate/v4/database/postgres"
	"golang.org/x/text/unicode/norm"
)

var ErrD1LTargetIdentity = errors.New("D1-L target identity proof failed")

const (
	cr1TargetPrefix          = "POWER-IOT-A3-TARGET"
	cr1TargetEncoding        = byte(0x01)
	cr1TargetFieldCount      = uint16(7)
	cr1TargetValue           = byte(0x01)
	cr1TargetNull            = byte(0x02)
	cr1TargetError           = byte(0x03)
	cr1TargetDatabase        = byte(0x01)
	cr1TargetSchema          = byte(0x02)
	cr1TargetServerAddress   = byte(0x03)
	cr1TargetServerPort      = byte(0x04)
	cr1TargetMigrationSchema = byte(0x05)
	cr1TargetMigrationTable  = byte(0x06)
	cr1TargetClusterID       = byte(0x07)
)

type cr1TargetIdentity struct {
	database        string
	schema          string
	serverAddress   string
	serverPort      uint16
	migrationSchema string
	migrationTable  string
	clusterID       uint64
}

// deriveCR1TargetFingerprint obtains every target field from the supplied
// pinned PostgreSQL session. The configured migration relation is included in
// the identity, but no caller-provided digest is used as an authority input.
func deriveCR1TargetFingerprint(ctx context.Context, q migrationMetadataQueryer, config *postgres.Config) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if q == nil || config == nil {
		return nil, fmt.Errorf("%w: pinned connection and migration configuration are required", ErrD1LTargetIdentity)
	}
	var databaseName, schemaName, address, clusterID sql.NullString
	var port sql.NullInt64
	if err := q.QueryRowContext(ctx, `
		SELECT current_database(), current_schema(), host(inet_server_addr()),
		       inet_server_port(), system_identifier::text
		  FROM pg_control_system()`).
		Scan(&databaseName, &schemaName, &address, &port, &clusterID); err != nil {
		return nil, fmt.Errorf("%w: read pinned PostgreSQL identity: %v", ErrD1LTargetIdentity, err)
	}
	if !databaseName.Valid || !schemaName.Valid || !clusterID.Valid {
		return nil, fmt.Errorf("%w: database, schema, and cluster identity are required", ErrD1LTargetIdentity)
	}
	canonicalDatabase, err := canonicalCR1Identifier(databaseName.String, "database")
	if err != nil {
		return nil, err
	}
	canonicalSchema, err := canonicalCR1Identifier(schemaName.String, "schema")
	if err != nil {
		return nil, err
	}
	canonicalAddress, err := canonicalCR1Address(address)
	if err != nil {
		return nil, err
	}
	if !port.Valid || port.Int64 < 1 || port.Int64 > 65535 {
		return nil, fmt.Errorf("%w: server port is missing or outside uint16 range", ErrD1LTargetIdentity)
	}
	canonicalCluster, err := canonicalCR1ClusterID(clusterID.String)
	if err != nil {
		return nil, err
	}
	migrationSchema, migrationTable, err := migrationMetadataIdentifiers(config, canonicalSchema)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve migration relation: %v", ErrD1LTargetIdentity, err)
	}
	migrationSchema, err = canonicalCR1Identifier(migrationSchema, "migration schema")
	if err != nil {
		return nil, err
	}
	migrationTable, err = canonicalCR1Identifier(migrationTable, "migration table")
	if err != nil {
		return nil, err
	}
	identity := cr1TargetIdentity{
		database: canonicalDatabase, schema: canonicalSchema, serverAddress: canonicalAddress,
		serverPort: uint16(port.Int64), migrationSchema: migrationSchema,
		migrationTable: migrationTable, clusterID: canonicalCluster,
	}
	preimage, err := encodeCR1TargetPreimage(identity)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(preimage)
	return append([]byte(nil), digest[:]...), nil
}

func canonicalCR1Identifier(value, field string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%w: %s is empty or invalid UTF-8", ErrD1LTargetIdentity, field)
	}
	if norm.NFC.String(value) != value {
		return "", fmt.Errorf("%w: %s is not NFC", ErrD1LTargetIdentity, field)
	}
	return value, nil
}

func canonicalCR1Address(value sql.NullString) (string, error) {
	if !value.Valid || value.String == "" || strings.Contains(value.String, "%") || !utf8.ValidString(value.String) {
		return "", fmt.Errorf("%w: server address is missing or invalid", ErrD1LTargetIdentity)
	}
	ip := net.ParseIP(value.String)
	if ip == nil {
		return "", fmt.Errorf("%w: server address is not an IP literal", ErrD1LTargetIdentity)
	}
	canonical := ip.String()
	if ip4 := ip.To4(); ip4 != nil {
		canonical = ip4.String()
	}
	if value.String != canonical {
		return "", fmt.Errorf("%w: server address is not canonical", ErrD1LTargetIdentity)
	}
	return canonical, nil
}

func canonicalCR1ClusterID(value string) (uint64, error) {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, fmt.Errorf("%w: cluster identity is not canonical decimal", ErrD1LTargetIdentity)
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return 0, fmt.Errorf("%w: cluster identity is outside uint64 range", ErrD1LTargetIdentity)
	}
	return parsed, nil
}

func encodeCR1TargetPreimage(identity cr1TargetIdentity) ([]byte, error) {
	fields := []struct {
		tag   byte
		value []byte
	}{
		{cr1TargetDatabase, []byte(identity.database)},
		{cr1TargetSchema, []byte(identity.schema)},
		{cr1TargetServerAddress, []byte(identity.serverAddress)},
		{cr1TargetServerPort, func() []byte {
			out := make([]byte, 2)
			binary.BigEndian.PutUint16(out, identity.serverPort)
			return out
		}()},
		{cr1TargetMigrationSchema, []byte(identity.migrationSchema)},
		{cr1TargetMigrationTable, []byte(identity.migrationTable)},
		{cr1TargetClusterID, func() []byte { out := make([]byte, 8); binary.BigEndian.PutUint64(out, identity.clusterID); return out }()},
	}
	preimage := make([]byte, 0, len(cr1TargetPrefix)+1+1+2+len(fields)*6)
	preimage = append(preimage, []byte(cr1TargetPrefix)...)
	preimage = append(preimage, 0x00, cr1TargetEncoding)
	var count [2]byte
	binary.BigEndian.PutUint16(count[:], cr1TargetFieldCount)
	preimage = append(preimage, count[:]...)
	for _, field := range fields {
		if len(field.value) > int(^uint32(0)) {
			return nil, fmt.Errorf("%w: target field %d is too large", ErrD1LTargetIdentity, field.tag)
		}
		preimage = append(preimage, field.tag, cr1TargetValue)
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field.value)))
		preimage = append(preimage, length[:]...)
		preimage = append(preimage, field.value...)
	}
	return preimage, nil
}
