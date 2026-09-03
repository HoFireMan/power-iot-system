package migrations

import (
	"context"
	"database/sql"
	"testing"
)

func TestDeviceLifecycleStandaloneUpgradeBackfillsAndIsIdempotent(t *testing.T) {
	database := newB02Database(t)
	migrateB02ForTest(t, database.DSN())

	db, err := sql.Open("postgres", database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var clientID uint
	if err := db.QueryRow(`INSERT INTO clients (name, code) VALUES ('Lifecycle Client', 'lifecycle-client') RETURNING id`).Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	var deviceID uint
	if err := db.QueryRow(`INSERT INTO devices (mac_address, name, inventory_owner_client_id) VALUES ('AABBCCDDEEFF', 'lifecycle fixture', $1) RETURNING id`, clientID).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	// Model a database that completed B-02 before 000012 existed. This is a
	// disposable test-only rollback of the additive lifecycle objects; the
	// protected production DOWN migration remains guarded.
	for _, statement := range []string{
		`DROP TRIGGER devices_lifecycle_terminal ON devices`,
		`DROP FUNCTION prevent_device_lifecycle_reactivation()`,
		`ALTER TABLE devices DROP CONSTRAINT devices_lifecycle_status_check`,
		`DROP INDEX IF EXISTS devices_lifecycle_status_idx`,
		`ALTER TABLE devices DROP COLUMN lifecycle_status`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	before, err := InspectProtectedMigration(context.Background(), database.DSN(), D5MigrationSpec(b02TestAdmission()))
	if err != nil || before.State != ProtectedStateCleanB02 {
		t.Fatalf("pre-lifecycle B-02 state=%s err=%v", before.State, err)
	}
	admission := b02TestAdmission()
	if err := RunDeviceLifecycleMigration(context.Background(), database.DSN(), admission); err != nil {
		t.Fatal(err)
	}
	if err := RunDeviceLifecycleMigration(context.Background(), database.DSN(), admission); err != nil {
		t.Fatalf("idempotent rerun failed: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT lifecycle_status FROM devices WHERE id = $1`, deviceID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" {
		t.Fatalf("backfilled lifecycle status=%q, want ACTIVE", status)
	}
}
