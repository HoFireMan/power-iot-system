package migrations

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"power-iot-backend/internal/testsupport"
)

func b02TestAdmission() ExternalWriterAdmission {
	return ExternalWriterAdmission{evidence: &externalWriterAdmissionEvidence{
		managedCooperativeWriters: true,
		directSQLControlled:       true,
		operationalDrainEvidence:  true,
	}}
}

func newB02Database(t *testing.T) *testsupport.Database {
	t.Helper()
	database, err := testsupport.New(context.Background(), os.Getenv("TEST_DATABASE_URL"), Up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return database
}

func migrateB02ForTest(t *testing.T, dsn string) {
	t.Helper()
	admission := b02TestAdmission()
	if _, err := RunD5Migration(context.Background(), dsn, admission); err != nil {
		t.Fatal(err)
	}
	if _, err := RunB02Migration(context.Background(), dsn, admission); err != nil {
		t.Fatal(err)
	}
}

func TestRunB02MigrationV7CatalogAndTimescaleDimension(t *testing.T) {
	database := newB02Database(t)
	migrateB02ForTest(t, database.DSN())
	version, dirty, err := Version(database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if version != 7 || dirty {
		t.Fatalf("metadata version=%d dirty=%t", version, dirty)
	}
	db, err := sql.Open("postgres", database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var dimension string
	if err := db.QueryRow(`SELECT column_name FROM timescaledb_information.dimensions WHERE hypertable_schema=current_schema() AND hypertable_name='power_readings' AND dimension_type='Time'`).Scan(&dimension); err != nil {
		t.Fatal(err)
	}
	if dimension != "recorded_at" {
		t.Fatalf("time dimension=%q", dimension)
	}
	inspection, err := InspectProtectedMigration(context.Background(), database.DSN(), D5MigrationSpec(b02TestAdmission()))
	if err != nil {
		t.Fatal(err)
	}
	if inspection.State != ProtectedStateCleanB02 {
		t.Fatalf("state=%s catalog=%s", inspection.State, inspection.Catalog)
	}
}

func TestB02CatalogDriftRefusesCleanAdmission(t *testing.T) {
	tests := []struct {
		name   string
		mutate string
	}{
		{name: "coverage check dropped", mutate: `ALTER TABLE power_readings DROP CONSTRAINT power_readings_coverage_profile_check`},
		{name: "digest check altered", mutate: `ALTER TABLE telemetry_ingest_keys DROP CONSTRAINT telemetry_ingest_keys_coverage_digest_length; ALTER TABLE telemetry_ingest_keys ADD CONSTRAINT telemetry_ingest_keys_coverage_digest_length CHECK (canonical_coverage_digest IS NULL OR octet_length(canonical_coverage_digest)=16)`},
		{name: "coverage index predicate altered", mutate: `DROP INDEX idx_power_readings_coverage_mp_interval_start; CREATE INDEX idx_power_readings_coverage_mp_interval_start ON power_readings (measurement_point_id, interval_start) WHERE coverage_version = 2`},
		{name: "conflict default dropped", mutate: `ALTER TABLE telemetry_ingest_keys ALTER COLUMN conflict_detected DROP DEFAULT`},
		{name: "conflict nullability altered", mutate: `ALTER TABLE telemetry_ingest_keys ALTER COLUMN conflict_detected DROP NOT NULL`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			database := newB02Database(t)
			migrateB02ForTest(t, database.DSN())
			db, err := sql.Open("postgres", database.DSN())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.mutate); err != nil {
				db.Close()
				t.Fatal(err)
			}
			db.Close()
			inspection, err := InspectProtectedMigration(context.Background(), database.DSN(), D5MigrationSpec(b02TestAdmission()))
			if err == nil && inspection.State == ProtectedStateCleanB02 {
				t.Fatalf("drift admitted as clean B-02: catalog=%s", inspection.Catalog)
			}
			if _, err := BootstrapAndAdmit(context.Background(), database.DSN()); err == nil {
				t.Fatal("runtime admission accepted drifted B-02 catalog")
			}
		})
	}
}
