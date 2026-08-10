package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCanonicalWriterFenceKeyDerivesCorrectedContract(t *testing.T) {
	label := "power-iot-system/security-schema-writer-fence/v1"
	digest := sha256.Sum256([]byte(label))
	if got := hex.EncodeToString(digest[:]); got != "a0afcd73957843ebabfdb27b0ef07317894a7d6ffd995fe80a5efcc593d950a7" {
		t.Fatalf("sha256=%s", got)
	}
	if got := hex.EncodeToString(digest[:8]); got != "a0afcd73957843eb" {
		t.Fatalf("first eight bytes=%s", got)
	}
	if got := CanonicalWriterFenceKey(); got != -6868045010404097045 {
		t.Fatalf("derived key=%d, want -6868045010404097045", got)
	}
	if WriterFenceKey != -6868045010404097045 {
		t.Fatalf("runtime key=%d, want corrected canonical key", WriterFenceKey)
	}
}

func TestParsePostgresDatabaseURLPreservesMigrationOptions(t *testing.T) {
	databaseURL := "postgres://user:pass@example.invalid/power?sslmode=disable&x-migrations-table=%22security%22.%22versions%22&x-migrations-table-quoted=true&x-statement-timeout=1250&x-multi-statement=true&x-multi-statement-max-size=4096"
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(parsed.driverURL, "x-") {
		t.Fatalf("driver URL retained golang-migrate options: %s", parsed.driverURL)
	}
	if parsed.config.MigrationsTable != `"security"."versions"` || !parsed.config.MigrationsTableQuoted {
		t.Fatalf("migration table config=%q quoted=%t", parsed.config.MigrationsTable, parsed.config.MigrationsTableQuoted)
	}
	if parsed.config.StatementTimeout != 1250*time.Millisecond || !parsed.config.MultiStatementEnabled || parsed.config.MultiStatementMaxSize != 4096 {
		t.Fatalf("migration options were not preserved: %+v", parsed.config)
	}
	schema, table, err := migrationMetadataIdentifiers(parsed.config, "public")
	if err != nil {
		t.Fatal(err)
	}
	if schema != "security" || table != "versions" {
		t.Fatalf("metadata identifiers=%q.%q", schema, table)
	}
}

func TestParsePostgresDatabaseURLAcceptsOneQuotedMetadataIdentifier(t *testing.T) {
	parsed, err := parsePostgresDatabaseURL("postgres://user:pass@example.invalid/power?x-migrations-table=%22versions%22&x-migrations-table-quoted=true")
	if err != nil {
		t.Fatal(err)
	}
	schema, table, err := migrationMetadataIdentifiers(parsed.config, "public")
	if err != nil {
		t.Fatal(err)
	}
	if schema != "public" || table != "versions" {
		t.Fatalf("metadata identifiers=%q.%q", schema, table)
	}
}

func TestParsePostgresDatabaseURLRejectsMalformedQuotedMetadataIdentifier(t *testing.T) {
	for _, value := range []string{
		`foo"bar"`,
		`"security"."versions"garbage`,
		`"security"."versions`,
		`""`,
		`"security"versions"`,
		`"security"."versions"."extra"`,
	} {
		t.Run(value, func(t *testing.T) {
			_, err := parsePostgresDatabaseURL("postgres://user:pass@example.invalid/power?x-migrations-table=" + url.QueryEscape(value) + "&x-migrations-table-quoted=true")
			if err == nil {
				t.Fatalf("malformed quoted metadata identifier %q was accepted", value)
			}
		})
	}
}

func TestRequireProtectedWorkNeedsActualExclusiveCapability(t *testing.T) {
	decision := AssessSecuritySchemaWriterFence()
	if decision.Status != WriterFenceEnforced || !decision.ProtectedWorkAllowed {
		t.Fatalf("unexpected decision=%+v", decision)
	}
	if err := decision.RequireProtectedWork(); err == nil {
		t.Fatal("protected work must reject missing capability")
	}
	if err := decision.RequireProtectedWork(ProtectedWorkCapability{}); err == nil {
		t.Fatal("protected work must reject zero capability")
	}
}

func TestSharedWriterFenceRejectsNonTransactionHandle(t *testing.T) {
	if err := AcquireSharedWriterFence(context.Background(), nil); err == nil {
		t.Fatal("nil transaction must fail closed")
	}
}
