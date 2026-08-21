package migrations

import (
	"context"
	"database/sql"
	"testing"
)

func TestD1LAdditiveTransitionIsAtomicAndLedgerReady(t *testing.T) {
	_, dsn, _ := installD1LTestCatalog(t)
	target := d1lTargetForTest(t, dsn)
	// The fixture helper uses a deterministic digest for catalog tests; make
	// this transition fixture carry the database-owned target.
	db0, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db0.ExecContext(context.Background(), `UPDATE security_control.control_schema_migrations SET target_fingerprint=$1`, target); err != nil {
		db0.Close()
		t.Fatal(err)
	}
	db0.Close()
	got, err := D1LUpgradeLedger(context.Background(), dsn, target)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != D1LValidNextLedgerReady {
		t.Fatalf("state=%s detail=%s", got.State, got.Detail)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows, version int
	var digest []byte
	if err := db.QueryRowContext(context.Background(), `SELECT count(*),min(control_version)::int FROM security_control.control_schema_migrations`).Scan(&rows, &version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT installer_digest FROM security_control.control_schema_migrations`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || version != int(d1LNextControlVersion) || !equalBytes(digest, d1LLedgerTransitionDigestBytes()) {
		t.Fatalf("manifest rows=%d version=%d digest=%x", rows, version, digest)
	}
	var provenanceExists bool
	if err := db.QueryRowContext(context.Background(), `SELECT to_regclass('security_control.admission_provenance') IS NOT NULL`).Scan(&provenanceExists); err != nil {
		t.Fatal(err)
	}
	if !provenanceExists {
		t.Fatal("transition committed without provenance ledger")
	}
}
