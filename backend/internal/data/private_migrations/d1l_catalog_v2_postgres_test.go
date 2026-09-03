package migrations

import (
	"context"
	"database/sql"
	"testing"
)

func TestD1LCatalogRejectsHybridAndWrongNextDigest(t *testing.T) {
	_, dsn, target := installD1LTestCatalog(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), string(d1LLedgerTransitionBytes)); err != nil {
		t.Fatal(err)
	}
	obs := recognizeD1LDatabase(t, dsn, target)
	if obs.State != D1LHybrid {
		t.Fatalf("hybrid state=%s detail=%s", obs.State, obs.Detail)
	}
}

func TestD1LCatalogRejectsWrongNextDigestFailClosed(t *testing.T) {
	_, dsn, _ := installD1LTestCatalog(t)
	target := d1lTargetForTest(t, dsn)
	db0, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db0.ExecContext(context.Background(), `UPDATE security_control.control_schema_migrations SET target_fingerprint=$1`, target); err != nil {
		db0.Close()
		t.Fatal(err)
	}
	db0.Close()
	// Keep the transition proof in one owner transaction, then mutate only the
	// current manifest digest to exercise the version-aware digest gate.
	if _, err := D1LUpgradeLedger(context.Background(), dsn, target); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE security_control.control_schema_migrations DROP CONSTRAINT control_schema_migrations_installer_digest_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE security_control.control_schema_migrations SET installer_digest=decode(repeat('00',32),'hex')`); err != nil {
		t.Fatal(err)
	}
	obs := recognizeD1LDatabase(t, dsn, target)
	if obs.State != D1LWrongInstallerDigest {
		t.Fatalf("wrong digest state=%s detail=%s", obs.State, obs.Detail)
	}
}
