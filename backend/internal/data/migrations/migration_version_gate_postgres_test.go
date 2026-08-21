package migrations

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"testing"

	"power-iot-backend/internal/testsupport"
)

func newVersionGateTestDatabase(t *testing.T) *testsupport.Database {
	t.Helper()
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; migration Version gate test requires PostgreSQL")
	}
	db, err := testsupport.New(context.Background(), source, Up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func reservedD1LVersionURL(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("x-migrations-table", `"security_control"."control_schema_migrations"`)
	query.Set("x-migrations-table-quoted", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func installReservedD1LMetadataFixture(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `
		CREATE SCHEMA security_control;
		CREATE TABLE security_control.control_schema_migrations (
			control_version bigint NOT NULL,
			dirty boolean NOT NULL,
			target_fingerprint bytea NOT NULL,
			installer_digest bytea NOT NULL,
			install_id uuid NOT NULL,
			installed_at timestamptz NOT NULL
		);
		INSERT INTO security_control.control_schema_migrations
			(control_version, dirty, target_fingerprint, installer_digest, install_id, installed_at)
		VALUES (1, false, decode(repeat('00', 32), 'hex'), decode(repeat('00', 32), 'hex'), '00000000-0000-0000-0000-000000000001', clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
}

func TestVersionPreservesOrdinaryApplicationBehavior(t *testing.T) {
	db := newVersionGateTestDatabase(t)
	version, dirty, err := Version(db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if version != protectedSchemaVersion || dirty {
		t.Fatalf("ordinary application Version=%d dirty=%t, want clean v%d", version, dirty, protectedSchemaVersion)
	}
}

func TestVersionRejectsReservedD1LTargetBeforeMetadataInspection(t *testing.T) {
	db := newVersionGateTestDatabase(t)
	installReservedD1LMetadataFixture(t, db.DSN())
	reservedURL := reservedD1LVersionURL(t, db.DSN())

	if _, _, err := Version(reservedURL); !errors.Is(err, ErrD1LGenericRoute) {
		t.Fatalf("reserved Version error=%v, want errors.Is(..., ErrD1LGenericRoute)", err)
	}
	if err := Up(db.DSN()); !errors.Is(err, ErrD1LGenericRoute) {
		t.Fatalf("reserved Up error=%v, want errors.Is(..., ErrD1LGenericRoute)", err)
	}
	if err := Down(db.DSN()); !errors.Is(err, ErrD1LGenericRoute) {
		t.Fatalf("reserved Down error=%v, want errors.Is(..., ErrD1LGenericRoute)", err)
	}

	probe, err := sql.Open("postgres", db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	var applicationVersion, controlVersion int
	var applicationDirty, controlDirty bool
	if err := probe.QueryRowContext(context.Background(), `
		SELECT app.version, app.dirty, control.control_version, control.dirty
		FROM schema_migrations AS app
		CROSS JOIN security_control.control_schema_migrations AS control`).Scan(
		&applicationVersion, &applicationDirty, &controlVersion, &controlDirty); err != nil {
		t.Fatal(err)
	}
	if applicationVersion != protectedSchemaVersion || applicationDirty || controlVersion != 1 || controlDirty {
		t.Fatalf("migration metadata namespaces changed: application=%d/%t control=%d/%t", applicationVersion, applicationDirty, controlVersion, controlDirty)
	}
}
