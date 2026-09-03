//go:build securityintegration

package migrations

import (
	"context"
	"database/sql"
	"testing"
)

func TestD5ProtectedMigrationCreatesAndVerifiesPhysicalCatalog(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	report, err := RunD5Migration(context.Background(), dsn, trustedExternalWriterAdmissionForTest())
	if err != nil || report.Outcome != ProtectedCommittedAndVerified || report.PostCommitState != ProtectedStateCleanV6 || report.PostCommitCatalog != ProtectedCatalogExactV6 {
		t.Fatalf("D5 migration report=%+v err=%v", report, err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"d4_operation_ledger", "d4_operation_journal"} {
		var present bool
		if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&present); err != nil || !present {
			t.Fatalf("D5 table %s present=%t err=%v", table, present, err)
		}
	}
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 6 || dirty {
		t.Fatalf("migration metadata version=%d dirty=%t", version, dirty)
	}
}
