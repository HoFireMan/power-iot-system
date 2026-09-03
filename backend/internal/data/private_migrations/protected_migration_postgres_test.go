//go:build securityintegration

package migrations

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/lib/pq"
	"power-iot-backend/internal/testsupport"
)

func newProtectedFixtureDatabase(t *testing.T) string {
	t.Helper()
	source := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_MIGRATION_DATABASE_URL is required")
	}
	db, err := testsupport.New(context.Background(), source, Up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close disposable database: %v", err)
		}
	})
	return db.DSN()
}

func newProtectedEmptyDatabase(t *testing.T) string {
	t.Helper()
	source := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_MIGRATION_DATABASE_URL is required")
	}
	db, err := testsupport.New(context.Background(), source, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close disposable database: %v", err)
		}
	})
	return db.DSN()
}

func protectedFixtureSpec(apply func(context.Context, *sql.Tx) error) ProtectedMigrationSpec {
	return ProtectedMigrationSpec{
		ExternalWriterAdmission: trustedExternalWriterAdmissionForTest(),
		V6CatalogTables:         append([]string(nil), protectedV6CatalogTables...),
		Apply:                   apply,
		V5SemanticVerifier:      func(context.Context, ProtectedMigrationQueryer) error { return nil },
		V6SemanticVerifier:      func(context.Context, ProtectedMigrationQueryer) error { return nil },
	}
}

func applyFinalFixture(ctx context.Context, tx *sql.Tx) error {
	if err := applyD5Migration(ctx, tx); err != nil {
		return err
	}
	for _, name := range targetForeignKeys {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+foreignKeyTable(name)+` VALIDATE CONSTRAINT `+pq.QuoteIdentifier(name)); err != nil {
			return err
		}
	}
	for _, column := range targetNullableColumns {
		parts := strings.SplitN(column, ".", 2)
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+pq.QuoteIdentifier(parts[0])+` ALTER COLUMN `+pq.QuoteIdentifier(parts[1])+` SET NOT NULL`); err != nil {
			return err
		}
	}
	return nil
}

func foreignKeyTable(name string) string {
	switch name {
	case "security_shops_client_id_fkey":
		return "shops"
	case "security_devices_inventory_owner_client_id_fkey":
		return "devices"
	case "security_user_shop_relations_user_id_fkey", "security_user_shop_relations_shop_id_fkey":
		return "user_shop_relations"
	case "security_admin_binding_operations_client_id_fkey":
		return "admin_binding_operations"
	default:
		return "admin_binding_audits"
	}
}

func TestProtectedMigrationPostgresClassificationAndCardinality(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	spec := protectedFixtureSpec(nil)
	report, err := InspectProtectedMigration(context.Background(), dsn, spec)
	if err != nil || report.State != ProtectedStateCleanV5 || report.Catalog != ProtectedCatalogExactV5 {
		t.Fatalf("clean v5 report=%+v err=%v", report, err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE d3_duplicate_versions(version bigint NOT NULL, dirty boolean NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO d3_duplicate_versions(version, dirty) VALUES (5, false), (5, false)`); err != nil {
		t.Fatal(err)
	}
	customParsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	customQuery := customParsed.Query()
	customQuery.Set("x-migrations-table", `"public"."d3_duplicate_versions"`)
	customQuery.Set("x-migrations-table-quoted", "true")
	customParsed.RawQuery = customQuery.Encode()
	duplicate, err := InspectProtectedMigration(context.Background(), customParsed.String(), spec)
	if err == nil || duplicate.State != ProtectedStateAmbiguous || !errors.Is(err, ErrProtectedMigrationState) {
		t.Fatalf("duplicate report=%+v err=%v", duplicate, err)
	}
	if _, err := db.Exec(`DROP TABLE d3_duplicate_versions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_migrations SET dirty = true`); err != nil {
		t.Fatal(err)
	}
	dirty, err := InspectProtectedMigration(context.Background(), dsn, spec)
	if err == nil || dirty.State != ProtectedStateDirtyV5 {
		t.Fatalf("dirty v5 report=%+v err=%v", dirty, err)
	}
	if _, err := db.Exec(`UPDATE schema_migrations SET dirty = false`); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedMigrationPostgresCanonicalCatalogAuthorityAndUnrelatedObjects(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	invalid := protectedFixtureSpec(nil)
	invalid.V6CatalogTables = []string{"clients"}
	if _, err := InspectProtectedMigration(context.Background(), dsn, invalid); !errors.Is(err, ErrProtectedMigrationSpec) {
		t.Fatalf("caller-controlled catalog inventory was accepted: %v", err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE d3_unrelated_object(id bigint); ALTER TABLE shops ADD CONSTRAINT d3_unrelated_fk FOREIGN KEY (id) REFERENCES clients(id) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	spec := protectedFixtureSpec(nil)
	clean, err := InspectProtectedMigration(context.Background(), dsn, spec)
	if err != nil || clean.State != ProtectedStateCleanV5 || clean.Catalog != ProtectedCatalogExactV5 {
		t.Fatalf("unrelated object changed clean-v5 classification: report=%+v err=%v", clean, err)
	}
	if _, err := db.Exec(`ALTER TABLE shops ADD CONSTRAINT d3_unexpected_protected_fk FOREIGN KEY (client_id) REFERENCES clients(id) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	assertProtectedInspectionAmbiguous(t, dsn, spec)
}

func TestProtectedMigrationPostgresCatalogAuthorityPreservesV6ClassificationWithUnrelatedObject(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE d3_unrelated_v6_object(id bigint)`); err != nil {
		t.Fatal(err)
	}
	report, err := RunProtectedMigration(context.Background(), dsn, protectedFixtureSpec(applyFinalFixture))
	if err != nil || report.PostCommitState != ProtectedStateCleanV6 || report.PostCommitCatalog != ProtectedCatalogExactV6 {
		t.Fatalf("unrelated object changed clean-v6 classification: report=%+v err=%v", report, err)
	}
}

func TestProtectedMigrationPostgresCustomMetadataAuthority(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE SCHEMA metadata_fixture`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE metadata_fixture.d3_custom_versions(version bigint NOT NULL, dirty boolean NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO metadata_fixture.d3_custom_versions(version, dirty) VALUES (5, false)`); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("x-migrations-table", `"metadata_fixture"."d3_custom_versions"`)
	query.Set("x-migrations-table-quoted", "true")
	parsed.RawQuery = query.Encode()
	custom := protectedFixtureSpec(nil)
	report, err := InspectProtectedMigration(context.Background(), parsed.String(), custom)
	if err != nil || report.State != ProtectedStateCleanV5 {
		t.Fatalf("custom metadata report=%+v err=%v", report, err)
	}
	decoy, err := InspectProtectedMigration(context.Background(), parsed.String(), custom)
	if err != nil || decoy.State != ProtectedStateCleanV5 {
		t.Fatalf("custom authority was not honored report=%+v err=%v", decoy, err)
	}
}

func TestProtectedMigrationPostgresTransitionAndExplicitV6Recovery(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	spec := protectedFixtureSpec(applyFinalFixture)
	report, err := RunProtectedMigration(context.Background(), dsn, spec)
	if err != nil || report.Outcome != ProtectedCommittedAndVerified || report.PostCommitState != ProtectedStateCleanV6 {
		t.Fatalf("transition report=%+v err=%v", report, err)
	}
}

func TestProtectedMigrationPostgresUnknownCommitDoesNotRetry(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	spec := protectedFixtureSpec(applyFinalFixture)
	applyCalls := 0
	spec.Apply = func(ctx context.Context, tx *sql.Tx) error {
		applyCalls++
		return applyFinalFixture(ctx, tx)
	}
	commits := 0
	report, err := runProtectedMigration(context.Background(), dsn, spec, protectedMigrationHooks{Commit: func(ctx context.Context, tx *sql.Tx) error {
		commits++
		if err := tx.Commit(); err != nil {
			return err
		}
		if commits == 2 {
			return errors.New("injected commit acknowledgement loss")
		}
		return nil
	}})
	if err == nil || report.Outcome != ProtectedCommitOutcomeUnknown || report.PostCommitState != ProtectedStateTransitionV6 || applyCalls != 1 {
		t.Fatalf("unknown report=%+v err=%v apply_calls=%d commits=%d", report, err, applyCalls, commits)
	}
	if !errors.Is(err, ErrProtectedMigrationNoRetry) || !errors.Is(err, ErrProtectedMigrationUnknownCommit) {
		t.Fatalf("unknown outcome lost no-retry provenance: %v", err)
	}
	if _, err := RecoverProtectedMigration(context.Background(), dsn, spec, ProtectedRecoveryCompleteCleanV6); err != nil {
		t.Fatal(err)
	}
}

func withMigrationMetadataTable(t *testing.T, dsn, table string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("x-migrations-table", table)
	query.Set("x-migrations-table-quoted", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func assertProtectedInspectionAmbiguous(t *testing.T, dsn string, spec ProtectedMigrationSpec) {
	t.Helper()
	report, err := InspectProtectedMigration(context.Background(), dsn, spec)
	if err == nil || !errors.Is(err, ErrProtectedMigrationState) || report.State != ProtectedStateAmbiguous {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func prepareDirtyV6(t *testing.T, dsn string, spec ProtectedMigrationSpec) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyFinalFixture(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_migrations SET version = 6, dirty = true`); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedMigrationPostgresAdversarialForeignKeyEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate string
	}{
		{"missing expected foreign key and decoy relation", `ALTER TABLE shops DROP CONSTRAINT security_shops_client_id_fkey; CREATE SCHEMA decoy_fk; CREATE TABLE decoy_fk.shops(id bigint, client_id bigint); ALTER TABLE decoy_fk.shops ADD CONSTRAINT security_shops_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) NOT VALID`},
		{"wrong referencing columns", `ALTER TABLE shops DROP CONSTRAINT security_shops_client_id_fkey; ALTER TABLE shops ADD CONSTRAINT security_shops_client_id_fkey FOREIGN KEY (id) REFERENCES clients(id) ON DELETE RESTRICT NOT VALID`},
		{"wrong referenced relation", `ALTER TABLE shops DROP CONSTRAINT security_shops_client_id_fkey; ALTER TABLE shops ADD CONSTRAINT security_shops_client_id_fkey FOREIGN KEY (client_id) REFERENCES users(id) ON DELETE RESTRICT NOT VALID`},
		{"unexpected validation status", `ALTER TABLE shops VALIDATE CONSTRAINT security_shops_client_id_fkey`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dsn := newProtectedFixtureDatabase(t)
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(tc.mutate); err != nil {
				t.Fatal(err)
			}
			assertProtectedInspectionAmbiguous(t, dsn, protectedFixtureSpec(nil))
		})
	}
}

func TestProtectedMigrationPostgresAdversarialNullabilityEvidence(t *testing.T) {
	tests := []string{
		`ALTER TABLE shops ALTER COLUMN client_id SET NOT NULL`,
		`ALTER TABLE shops ALTER COLUMN client_id SET NOT NULL; ALTER TABLE devices ALTER COLUMN inventory_owner_client_id SET NOT NULL`,
		`ALTER TABLE shops ALTER COLUMN client_id SET NOT NULL; CREATE SCHEMA decoy_nullability; CREATE TABLE decoy_nullability.shops(client_id bigint)`,
	}
	for _, mutate := range tests {
		t.Run(mutate, func(t *testing.T) {
			dsn := newProtectedFixtureDatabase(t)
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(mutate); err != nil {
				t.Fatal(err)
			}
			assertProtectedInspectionAmbiguous(t, dsn, protectedFixtureSpec(nil))
		})
	}
}

func TestProtectedMigrationPostgresAdversarialTriggerEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate string
	}{
		{"missing trigger and decoy relation", `DROP TRIGGER admin_binding_audits_client_provenance ON admin_binding_audits; CREATE SCHEMA decoy_trigger; CREATE TABLE decoy_trigger.admin_binding_audits(id bigint); CREATE FUNCTION decoy_trigger.validate_admin_binding_audit_client_provenance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$; CREATE TRIGGER admin_binding_audits_client_provenance BEFORE INSERT ON decoy_trigger.admin_binding_audits FOR EACH ROW EXECUTE FUNCTION decoy_trigger.validate_admin_binding_audit_client_provenance()`},
		{"wrong trigger function", `CREATE OR REPLACE FUNCTION wrong_d3_trigger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$; DROP TRIGGER admin_binding_audits_client_provenance ON admin_binding_audits; CREATE TRIGGER admin_binding_audits_client_provenance BEFORE INSERT ON admin_binding_audits FOR EACH ROW EXECUTE FUNCTION wrong_d3_trigger()`},
		{"same-name provenance body replacement", `CREATE OR REPLACE FUNCTION validate_admin_binding_audit_client_provenance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`},
		{"same-name immutable body replacement", `CREATE OR REPLACE FUNCTION prevent_admin_binding_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dsn := newProtectedFixtureDatabase(t)
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(tc.mutate); err != nil {
				t.Fatal(err)
			}
			assertProtectedInspectionAmbiguous(t, dsn, protectedFixtureSpec(nil))
		})
	}
}

func TestProtectedMigrationPostgresAdversarialIndexEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate string
	}{
		{"missing index and decoy relation", `DROP INDEX security_shops_client_id_idx; CREATE SCHEMA decoy_index; CREATE TABLE decoy_index.shops(client_id bigint); CREATE INDEX security_shops_client_id_idx ON decoy_index.shops (client_id)`},
		{"wrong relation", `DROP INDEX security_shops_client_id_idx; CREATE INDEX security_shops_client_id_idx ON users (id)`},
		{"wrong column order", `DROP INDEX security_user_shop_relations_shop_user_idx; CREATE INDEX security_user_shop_relations_shop_user_idx ON user_shop_relations (user_id, shop_id)`},
		{"wrong sort direction", `DROP INDEX security_shops_client_id_idx; CREATE INDEX security_shops_client_id_idx ON shops (client_id DESC)`},
		{"expression substitute", `DROP INDEX security_shops_client_id_idx; CREATE INDEX security_shops_client_id_idx ON shops (client_id, (id))`},
		{"include substitute", `DROP INDEX security_shops_client_id_idx; CREATE INDEX security_shops_client_id_idx ON shops (client_id) INCLUDE (id)`},
		{"wrong partial predicate", `DROP INDEX security_admin_binding_operations_client_time_idx; CREATE INDEX security_admin_binding_operations_client_time_idx ON admin_binding_operations (client_id, created_at DESC) WHERE client_id > 0`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dsn := newProtectedFixtureDatabase(t)
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(tc.mutate); err != nil {
				t.Fatal(err)
			}
			assertProtectedInspectionAmbiguous(t, dsn, protectedFixtureSpec(nil))
		})
	}
}

func TestProtectedMigrationPostgresCustomMetadataCollisionAndRecoveryAuthority(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE SCHEMA metadata_fixture; CREATE SCHEMA metadata_decoy`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE metadata_fixture.d3_versions(version bigint NOT NULL, dirty boolean NOT NULL); CREATE TABLE metadata_decoy.d3_versions(version bigint NOT NULL, dirty boolean NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO metadata_fixture.d3_versions VALUES (5, false); INSERT INTO metadata_decoy.d3_versions VALUES (6, true); UPDATE schema_migrations SET version = 6, dirty = true`); err != nil {
		t.Fatal(err)
	}
	customDSN := withMigrationMetadataTable(t, dsn, `"metadata_fixture"."d3_versions"`)
	spec := protectedFixtureSpec(nil)
	report, err := InspectProtectedMigration(context.Background(), customDSN, spec)
	if err != nil || report.State != ProtectedStateCleanV5 {
		t.Fatalf("custom authority report=%+v err=%v", report, err)
	}
	if _, err := db.Exec(`INSERT INTO metadata_fixture.d3_versions VALUES (5, false)`); err != nil {
		t.Fatal(err)
	}
	assertProtectedInspectionAmbiguous(t, customDSN, spec)
	if _, err := db.Exec(`DELETE FROM metadata_fixture.d3_versions WHERE ctid IN (SELECT ctid FROM metadata_fixture.d3_versions LIMIT 1); UPDATE metadata_fixture.d3_versions SET version = 6, dirty = true`); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverProtectedMigration(context.Background(), customDSN, spec, ProtectedRecoveryRestoreCleanV5)
	if err != nil || recovered.Outcome != ProtectedCommittedAndVerified {
		t.Fatalf("custom recovery report=%+v err=%v", recovered, err)
	}
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM metadata_fixture.d3_versions`).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 5 || dirty {
		t.Fatalf("authoritative metadata was not recovered: version=%d dirty=%t", version, dirty)
	}
	if err := db.QueryRow(`SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 6 || !dirty {
		t.Fatalf("default decoy metadata changed: version=%d dirty=%t", version, dirty)
	}
}

func TestProtectedMigrationPostgresEmptyCatalogIsBootstrapBoundary(t *testing.T) {
	dsn := newProtectedEmptyDatabase(t)
	report, err := InspectProtectedMigration(context.Background(), dsn, protectedFixtureSpec(nil))
	if err == nil || !errors.Is(err, ErrProtectedMigrationState) || report.State != ProtectedStateBootstrap || report.Catalog != ProtectedCatalogEmpty {
		t.Fatalf("empty catalog report=%+v err=%v", report, err)
	}
}

func TestProtectedMigrationPostgresUnknownCommitAtEachTransition(t *testing.T) {
	tests := []struct {
		name           string
		unknownCommit  int
		wantPhase      ProtectedMigrationPhase
		wantApplyCalls int
		wantPostState  ProtectedMigrationState
		wantPostCat    ProtectedCatalogState
	}{
		{"dirty marker", 1, ProtectedPhaseDirtyMarker, 0, ProtectedStateTransitionV6, ProtectedCatalogExactV5},
		{"enforcement body", 2, ProtectedPhaseEnforcement, 1, ProtectedStateTransitionV6, ProtectedCatalogExactV6},
		{"final marker", 3, ProtectedPhaseFinalMarker, 1, ProtectedStateCleanV6, ProtectedCatalogExactV6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dsn := newProtectedFixtureDatabase(t)
			spec := protectedFixtureSpec(applyFinalFixture)
			applyCalls, commits := 0, 0
			spec.Apply = func(ctx context.Context, tx *sql.Tx) error { applyCalls++; return applyFinalFixture(ctx, tx) }
			report, err := runProtectedMigration(context.Background(), dsn, spec, protectedMigrationHooks{Commit: func(ctx context.Context, tx *sql.Tx) error {
				commits++
				if err := tx.Commit(); err != nil {
					return err
				}
				if commits == tc.unknownCommit {
					return errors.New("injected commit acknowledgement loss")
				}
				return nil
			}})
			if err == nil || report.Outcome != ProtectedCommitOutcomeUnknown || report.Phase != tc.wantPhase || report.PostCommitState != tc.wantPostState || report.PostCommitCatalog != tc.wantPostCat || applyCalls != tc.wantApplyCalls || commits != tc.unknownCommit {
				t.Fatalf("report=%+v err=%v apply_calls=%d commits=%d", report, err, applyCalls, commits)
			}
			if !errors.Is(err, ErrProtectedMigrationNoRetry) || !errors.Is(err, ErrProtectedMigrationUnknownCommit) {
				t.Fatalf("unknown commit provenance missing: %v", err)
			}
			if tc.unknownCommit == 3 {
				final, inspectErr := InspectProtectedMigration(context.Background(), dsn, spec)
				if inspectErr != nil || final.State != ProtectedStateCleanV6 || final.Catalog != ProtectedCatalogExactV6 {
					t.Fatalf("final-marker uncertainty was not resolved by independent proof: report=%+v err=%v", final, inspectErr)
				}
			}
		})
	}
}

func TestProtectedMigrationPostgresRecoveryUnknownCommitAndPostVerifyFailure(t *testing.T) {
	t.Run("explicit recovery unknown commit", func(t *testing.T) {
		dsn := newProtectedFixtureDatabase(t)
		spec := protectedFixtureSpec(applyFinalFixture)
		prepareDirtyV6(t, dsn, spec)
		commits := 0
		report, err := recoverProtectedMigration(context.Background(), dsn, spec, ProtectedRecoveryCompleteCleanV6, protectedMigrationHooks{Commit: func(ctx context.Context, tx *sql.Tx) error {
			commits++
			if err := tx.Commit(); err != nil {
				return err
			}
			return errors.New("injected recovery acknowledgement loss")
		}})
		if err == nil || report.Outcome != ProtectedCommitOutcomeUnknown || report.Phase != ProtectedPhaseRecovery || report.PostCommitState != ProtectedStateCleanV6 || commits != 1 {
			t.Fatalf("report=%+v err=%v commits=%d", report, err, commits)
		}
		if !errors.Is(err, ErrProtectedMigrationNoRetry) {
			t.Fatalf("recovery no-retry provenance missing: %v", err)
		}
	})

	t.Run("recovery post verification failure", func(t *testing.T) {
		dsn := newProtectedFixtureDatabase(t)
		spec := protectedFixtureSpec(applyFinalFixture)
		prepareDirtyV6(t, dsn, spec)
		verifications := 0
		spec.V6SemanticVerifier = func(context.Context, ProtectedMigrationQueryer) error {
			verifications++
			if verifications > 1 {
				return errors.New("injected recovery post-verification failure")
			}
			return nil
		}
		report, err := RecoverProtectedMigration(context.Background(), dsn, spec, ProtectedRecoveryCompleteCleanV6)
		if err == nil || report.Outcome != ProtectedCommittedPostVerifyFail || report.Phase != ProtectedPhasePostVerification || !report.Committed || report.PostCommitVerified || verifications != 2 {
			t.Fatalf("report=%+v err=%v verifications=%d", report, err, verifications)
		}
		if !errors.Is(err, ErrProtectedMigrationPostVerification) {
			t.Fatalf("post-verification classification missing: %v", err)
		}
	})
}

func TestProtectedMigrationPostgresEnforcementPostVerifyFailureDoesNotReplay(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	spec := protectedFixtureSpec(applyFinalFixture)
	applyCalls, verifications := 0, 0
	spec.Apply = func(ctx context.Context, tx *sql.Tx) error { applyCalls++; return applyFinalFixture(ctx, tx) }
	spec.V6SemanticVerifier = func(context.Context, ProtectedMigrationQueryer) error {
		verifications++
		return errors.New("injected enforcement post-verification failure")
	}
	commits := 0
	report, err := runProtectedMigration(context.Background(), dsn, spec, protectedMigrationHooks{Commit: func(ctx context.Context, tx *sql.Tx) error {
		commits++
		return tx.Commit()
	}})
	if err == nil || report.Outcome != ProtectedCommittedPostVerifyFail || report.Phase != ProtectedPhasePostVerification || !report.Committed || report.PostCommitVerified || applyCalls != 1 || commits != 2 || verifications == 0 {
		t.Fatalf("report=%+v err=%v apply_calls=%d commits=%d verifications=%d", report, err, applyCalls, commits, verifications)
	}
	if !errors.Is(err, ErrProtectedMigrationPostVerification) || !errors.Is(err, ErrProtectedMigrationNoRetry) {
		t.Fatalf("post-verification no-retry classification missing: %v", err)
	}
}

func TestProtectedMigrationPostgresAdvisoryLockStaysOnPinnedSession(t *testing.T) {
	dsn := newProtectedFixtureDatabase(t)
	parsed, err := parsePostgresDatabaseURL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := openExclusiveWriterFence(context.Background(), parsed)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquireMigrationAdvisoryLock(context.Background(), fence.Conn(), parsed)
	if err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	other, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		_ = lock.Close(context.Background())
		_ = fence.Close()
		t.Fatal(err)
	}
	defer other.Close()
	var available bool
	if err := other.QueryRow(`SELECT pg_try_advisory_lock($1::bigint)`, lock.key).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("second session acquired migration lock while pinned owner held it")
	}
	if err := lock.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
}
