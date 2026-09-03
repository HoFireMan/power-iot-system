package migrations

import (
	"context"
	"database/sql"
	"errors"
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

func TestRunB02ProtectedOperatorRehearsalTransitionAndAdmission(t *testing.T) {
	database := newB02Database(t)
	ctx := context.Background()
	initial, err := InspectProtectedMigration(ctx, database.DSN(), D5MigrationSpec(b02TestAdmission()))
	if err != nil || initial.State != ProtectedStateCleanV5 {
		t.Fatalf("pre-D6 state=%s err=%v", initial.State, err)
	}
	if report, err := RunD5Migration(ctx, database.DSN(), b02TestAdmission()); err != nil || report.PostCommitState != ProtectedStateCleanV6 {
		t.Fatalf("D6 transition report=%+v err=%v", report, err)
	}
	before, err := InspectProtectedMigration(ctx, database.DSN(), D5MigrationSpec(b02TestAdmission()))
	if err != nil || before.State != ProtectedStateCleanV6 {
		t.Fatalf("pre-B-02 state=%s err=%v", before.State, err)
	}
	denied := errors.New("rehearsal drain is incomplete")
	if _, err := RunB02ProtectedMigrationOperator(ctx, database.DSN(), func(context.Context) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("failed admission error=%v, want %v", err, denied)
	}
	unchanged, err := InspectProtectedMigration(ctx, database.DSN(), D5MigrationSpec(b02TestAdmission()))
	if err != nil || unchanged.State != ProtectedStateCleanV6 {
		t.Fatalf("failed admission changed state=%s err=%v", unchanged.State, err)
	}
	db, err := sql.Open("postgres", database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var billingRelation sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('electricity_rate_sets')`).Scan(&billingRelation); err != nil {
		t.Fatal(err)
	}
	if billingRelation.Valid {
		t.Fatalf("failed admission created billing relation %q", billingRelation.String)
	}
	if _, err := RunB02ProtectedMigrationOperator(ctx, database.DSN(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("accepted rehearsal operator failed: %v", err)
	}
	version, dirty, err := Version(database.DSN())
	if err != nil || version != 7 || dirty {
		t.Fatalf("metadata version=%d dirty=%t err=%v", version, dirty, err)
	}
	var sets, plans, ratePlans, tiers int
	for _, query := range []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM electricity_rate_sets`, &sets},
		{`SELECT count(*) FROM electricity_tariff_plans`, &plans},
		{`SELECT count(*) FROM electricity_rate_plans`, &ratePlans},
		{`SELECT count(*) FROM electricity_rate_tiers`, &tiers},
	} {
		if err := db.QueryRow(query.query).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if sets != 1 || plans != 3 || ratePlans != 3 || tiers != 34 {
		t.Fatalf("B-02 seed counts sets=%d plans=%d ratePlans=%d tiers=%d", sets, plans, ratePlans, tiers)
	}
	var provider, versionCode string
	if err := db.QueryRow(`SELECT provider, version_code FROM electricity_rate_sets`).Scan(&provider, &versionCode); err != nil {
		t.Fatal(err)
	}
	if provider != "TAIPOWER" || versionCode != "TAIPOWER_2025_10_01" {
		t.Fatalf("rate set=%s/%s", provider, versionCode)
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
