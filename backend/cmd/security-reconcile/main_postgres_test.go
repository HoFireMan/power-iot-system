//go:build securityintegration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/data/reconciliation"
	"power-iot-backend/internal/data/reconciliation/sourceowner"
	"power-iot-backend/internal/data/reconciliation/upstream"
)

func operatorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" || strings.Contains(strings.ToLower(dsn), "power_iot") {
		t.Fatal("dedicated TEST_DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepareD1LCLIArtifacts(t, db)
	return db
}

func prepareD1LCLIArtifacts(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	var provenancePresent bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('security_control.admission_provenance') IS NOT NULL`).Scan(&provenancePresent); err != nil {
		// A missing schema is the expected first-run state; PostgreSQL reports
		// that as a NULL regclass, not a query error. Any other error is unsafe.
		if !strings.Contains(err.Error(), `schema "security_control" does not exist`) {
			t.Fatalf("inspect D1 provenance table: %v", err)
		}
	}
	if !provenancePresent {
		_, caller, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("runtime.Caller could not locate D1 SQL artifacts")
		}
		artifactDir := filepath.Join(filepath.Dir(caller), "..", "..", "internal", "data", "migrations", "d1l")
		readArtifact := func(name string) []byte {
			artifact, err := os.ReadFile(filepath.Join(artifactDir, name))
			if err != nil {
				t.Fatalf("read accepted D1 SQL artifact %s: %v", name, err)
			}
			return artifact
		}
		v1 := readArtifact("install_v1.sql")
		v2 := readArtifact("install_v2.sql")
		v1Digest := sha256.Sum256(v1)
		v2Digest := sha256.Sum256(v2)
		if hex.EncodeToString(v1Digest[:]) != migrations.D1LInstallerDigestV1 {
			t.Fatalf("D1 v1 artifact digest mismatch: got=%x want=%s", v1Digest, migrations.D1LInstallerDigestV1)
		}
		if hex.EncodeToString(v2Digest[:]) != migrations.D1LInstallerDigestNext {
			t.Fatalf("D1 v2 artifact digest mismatch: got=%x want=%s", v2Digest, migrations.D1LInstallerDigestNext)
		}
		target := bytes.Repeat([]byte{0x5a}, 32)
		if _, err := db.ExecContext(ctx, string(v1)); err != nil {
			t.Fatalf("install accepted D1 v1 SQL artifact: %v", err)
		}
		if _, err := db.ExecContext(ctx, migrations.D1LManifestInsertSQL, target, v1Digest[:], uuid.New()); err != nil {
			t.Fatalf("install D1 v1 manifest: %v", err)
		}
		if _, err := db.ExecContext(ctx, string(v2)); err != nil {
			t.Fatalf("install accepted D1 v2 SQL artifact: %v", err)
		}
		if _, err := db.ExecContext(ctx, migrations.D1LManifestTransitionSQL, target, v2Digest[:], uuid.New()); err != nil {
			t.Fatalf("install D1 v2 manifest: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE TABLE security_control.admission_provenance, security_control.admission_boundaries, security_control.admission_leases RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate D1 lease/provenance/boundary tables: %v", err)
	}
}

type operatorFixture struct {
	client1 int64
	client2 int64
	shop1   int64
	shop2   int64
	device  int64
}

func createOperatorFixture(t *testing.T, db *sql.DB) operatorFixture {
	t.Helper()
	if _, err := db.Exec(`TRUNCATE TABLE clients, users, devices, device_types RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	var f operatorFixture
	var client1, client2, shop1, shop2, userID int64
	if err := db.QueryRow(`INSERT INTO clients (code, name) VALUES ('operator-client-1', 'Operator Client 1') RETURNING id`).Scan(&client1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO clients (code, name) VALUES ('operator-client-2', 'Operator Client 2') RETURNING id`).Scan(&client2); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, 'operator-shop-1', 'Operator Shop 1') RETURNING id`, client1).Scan(&shop1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, 'operator-shop-2', 'Operator Shop 2') RETURNING id`, client2).Scan(&shop2); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (account, password_hash, name, current_shop_id, auth_enabled) VALUES ('operator-user', 'test-hash', 'Operator User', $1, true) RETURNING id`, shop1).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2)`, userID, shop1); err != nil {
		t.Fatal(err)
	}
	var device int64
	if err := db.QueryRow(`INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, 'AABBCCDDEEFF', 'Operator Device') RETURNING id`, shop2).Scan(&device); err != nil {
		t.Fatal(err)
	}
	var point string
	if err := db.QueryRow(`INSERT INTO measurement_points (shop_id, name) VALUES ($1, 'Operator Point') RETURNING id`, shop1).Scan(&point); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES ($1, $2, $3)`, device, point, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.client1, f.client2, f.shop1, f.shop2, f.device = client1, client2, shop1, shop2, device
	return f
}

func createOwnedTargetDevice(t *testing.T, db *sql.DB, shopID, ownerClientID int64) int64 {
	t.Helper()
	var deviceID int64
	if err := db.QueryRow(`INSERT INTO devices (shop_id, mac_address, name, inventory_owner_client_id) VALUES ($1, '112233445566', 'Owned Target Device', $2) RETURNING id`, shopID, ownerClientID).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	var pointID string
	if err := db.QueryRow(`INSERT INTO measurement_points (shop_id, name) VALUES ($1, 'Owned Target Point') RETURNING id`, shopID).Scan(&pointID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES ($1, $2, $3)`, deviceID, pointID, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	return deviceID
}

func collectOperatorEvidence(t *testing.T, dsn string, operationID, attemptID uuid.UUID) sourceowner.Evidence {
	t.Helper()
	ownerDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := ownerDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	evidence, err := sourceowner.NewPostgresSourceOwner(ownerDB).CollectTrustedV5(context.Background(), sourceowner.NewInvocationBinding(operationID, attemptID))
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestOperatorBoundaryDiagnosticThenExplicitMappingExecuteAcrossSnapshotTimes(t *testing.T) {
	db := operatorTestDB(t)
	fixture := createOperatorFixture(t, db)
	targetDevice := createOwnedTargetDevice(t, db, fixture.shop2, fixture.client2)
	if _, err := db.Exec(`UPDATE shops SET client_id = NULL WHERE id = $1`, fixture.shop1); err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	var diagnosticOut, diagnosticErr bytes.Buffer
	if code := run([]string{"-database-url", dsn}, &diagnosticOut, &diagnosticErr); code != 0 {
		t.Fatalf("diagnostic code=%d out=%s err=%s", code, diagnosticOut.String(), diagnosticErr.String())
	}
	var diagnostic reconciliation.OperatorReport
	if err := json.Unmarshal(diagnosticOut.Bytes(), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Mode != reconciliation.OperatorDiagnostic || diagnostic.MappingBasisDigest == "" || len(diagnostic.RequiredExplicitMappings) == 0 {
		t.Fatalf("diagnostic report=%+v", diagnostic)
	}
	artifact := reconciliation.MappingArtifact{
		SchemaVersion: reconciliation.MappingSchema, Version: 5, SourceFactsDigest: diagnostic.MappingBasisDigest,
		Mappings: []reconciliation.MappingEntry{
			{Category: reconciliation.MappingShop, ShopID: uint(fixture.shop1), ClientID: uint(fixture.client1)},
			{Category: reconciliation.MappingDevice, DeviceID: uint(fixture.device), ClientID: uint(fixture.client1)},
		},
	}
	artifactBytes, err := artifact.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	mappingPath := filepath.Join(t.TempDir(), "mapping.json")
	if err := os.WriteFile(mappingPath, artifactBytes, 0600); err != nil {
		t.Fatal(err)
	}

	operationID, attemptID := uuid.New(), uuid.New()
	evidenceA := collectOperatorEvidence(t, dsn, operationID, attemptID)
	var target [32]byte
	target[0] = 1
	provenance, err := upstream.Produce(evidenceA, upstream.Binding{
		OperationID: operationID, AttemptID: attemptID, TargetFingerprint: target,
		RouteIntent: upstream.D1OwnerIssueRoute,
	}, "owner-v1")
	if err != nil {
		t.Fatal(err)
	}
	ledgerDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledgerDB.Close() })
	ledger, err := migrations.NewD1LProvenanceLedger(ledgerDB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := migrations.NewD1LOwnerService(ledger)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := owner.IssueAndActivate(context.Background(), provenance)
	if err != nil {
		t.Fatal(err)
	}
	evidenceADigest := evidenceA.Digest()
	if issued.Status != "ACTIVE" || issued.Identity.LeaseID == uuid.Nil || !bytes.Equal(issued.EvidenceDigest, evidenceADigest[:]) {
		t.Fatalf("issued D1 lease=%+v evidenceA=%x", issued, evidenceADigest)
	}
	args := []string{
		"-database-url", dsn, "-mapping-file", mappingPath, "-execute",
		"-d1-operation-id", issued.Identity.OperationID.String(),
		"-d1-attempt-id", issued.Identity.AttemptID.String(),
		"-d1-lease-id", issued.Identity.LeaseID.String(),
		"-d1-generation", strconv.FormatInt(issued.Identity.Generation, 10),
		"-d1-target-fingerprint", hex.EncodeToString(issued.Identity.TargetFingerprint),
		"-d1-evidence-digest", hex.EncodeToString(issued.Identity.EvidenceDigest),
		"-target-id", strconv.FormatUint(uint64(targetDevice), 10),
	}
	var executeOut, executeErr bytes.Buffer
	if code := run(args, &executeOut, &executeErr); code != 0 {
		t.Fatalf("execute code=%d out=%s err=%s", code, executeOut.String(), executeErr.String())
	}
	var executed reconciliation.OperatorReport
	if err := json.Unmarshal(executeOut.Bytes(), &executed); err != nil {
		t.Fatal(err)
	}
	if executed.Mode != reconciliation.OperatorExecute || executed.Outcome != reconciliation.ExecutionCommittedAndVerified || !executed.PostCommitVerified {
		t.Fatalf("execute report=%+v", executed)
	}
	var evidenceB sourceowner.Evidence
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Millisecond)
		}
		evidenceB = collectOperatorEvidence(t, dsn, operationID, attemptID)
		if evidenceB.ObservedAt().After(evidenceA.ObservedAt()) && evidenceB.Digest() != evidenceA.Digest() {
			break
		}
	}
	if !evidenceB.ObservedAt().After(evidenceA.ObservedAt()) || evidenceB.Digest() == evidenceA.Digest() {
		t.Fatalf("CLI source evidence is not fresh: evidenceA_observed=%s evidenceB_observed=%s evidenceA_digest=%x evidenceB_digest=%x", evidenceA.ObservedAt(), evidenceB.ObservedAt(), evidenceA.Digest(), evidenceB.Digest())
	}
	var leaseStatus, provenanceState string
	if err := db.QueryRow(`SELECT status FROM security_control.admission_leases WHERE lease_id=$1`, issued.Identity.LeaseID).Scan(&leaseStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM security_control.admission_provenance WHERE attempt_id=$1`, issued.Identity.AttemptID).Scan(&provenanceState); err != nil {
		t.Fatal(err)
	}
	if leaseStatus != "CONSUMED" || provenanceState != "CONSUMED" {
		t.Fatalf("D1 terminal state lease=%s provenance=%s", leaseStatus, provenanceState)
	}
}

func TestOperatorBoundaryDiagnosticThenMissingD1IdentityRejectedBeforeMutation(t *testing.T) {
	db := operatorTestDB(t)
	f := createOperatorFixture(t, db)
	if _, err := db.Exec(`UPDATE shops SET client_id = NULL WHERE id = $1`, f.shop1); err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	var diagnosticOut, diagnosticErr bytes.Buffer
	if code := run([]string{"-database-url", dsn}, &diagnosticOut, &diagnosticErr); code != 0 {
		t.Fatalf("diagnostic code=%d out=%s err=%s", code, diagnosticOut.String(), diagnosticErr.String())
	}
	var diagnostic reconciliation.OperatorReport
	if err := json.Unmarshal(diagnosticOut.Bytes(), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Mode != reconciliation.OperatorDiagnostic || diagnostic.MappingBasisDigest == "" || len(diagnostic.RequiredExplicitMappings) == 0 {
		t.Fatalf("diagnostic report=%+v", diagnostic)
	}
	artifact := reconciliation.MappingArtifact{
		SchemaVersion: reconciliation.MappingSchema, Version: 5, SourceFactsDigest: diagnostic.MappingBasisDigest,
		Mappings: []reconciliation.MappingEntry{
			{Category: reconciliation.MappingShop, ShopID: uint(f.shop1), ClientID: uint(f.client1)},
			{Category: reconciliation.MappingDevice, DeviceID: uint(f.device), ClientID: uint(f.client1)},
		},
	}
	artifactBytes, err := artifact.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	mappingPath := filepath.Join(t.TempDir(), "mapping.json")
	if err := os.WriteFile(mappingPath, artifactBytes, 0600); err != nil {
		t.Fatal(err)
	}
	// A diagnostic artifact is evidence for review, not authorization. The
	// legacy execute invocation deliberately omits the owner-issued D1 lease
	// identity and must fail before opening or mutating the database.
	var executeOut, executeErr bytes.Buffer
	if code := run([]string{"-database-url", dsn, "-mapping-file", mappingPath, "-execute"}, &executeOut, &executeErr); code != 2 {
		t.Fatalf("execute without D1 identity code=%d out=%s err=%s", code, executeOut.String(), executeErr.String())
	}
	if executeErr.String() != "invalid D1 lease identity\n" || executeOut.Len() != 0 {
		t.Fatalf("missing D1 identity output=%q stderr=%q", executeOut.String(), executeErr.String())
	}
	var shopClient, owner sql.NullInt64
	if err := db.QueryRow(`SELECT client_id FROM shops WHERE id=$1`, f.shop1).Scan(&shopClient); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, f.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if shopClient.Valid || owner.Valid {
		t.Fatalf("missing D1 identity mutated durable state shop/device=%+v/%+v", shopClient, owner)
	}
}

func TestOperatorBoundaryDiagnosticThenMissingD1TargetRejectedBeforeMutation(t *testing.T) {
	db := operatorTestDB(t)
	createOperatorFixture(t, db)
	dsn := os.Getenv("TEST_DATABASE_URL")
	var diagnosticOut, diagnosticErr bytes.Buffer
	if code := run([]string{"-database-url", dsn}, &diagnosticOut, &diagnosticErr); code != 0 {
		t.Fatalf("diagnostic code=%d out=%s err=%s", code, diagnosticOut.String(), diagnosticErr.String())
	}
	var diagnostic reconciliation.OperatorReport
	if err := json.Unmarshal(diagnosticOut.Bytes(), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Mode != reconciliation.OperatorDiagnostic || diagnostic.Outcome != "" {
		t.Fatalf("diagnostic report=%+v", diagnostic)
	}
	var nullOwners int
	if err := db.QueryRow(`SELECT count(*) FROM devices WHERE inventory_owner_client_id IS NULL`).Scan(&nullOwners); err != nil {
		t.Fatal(err)
	}
	if nullOwners != 1 {
		t.Fatalf("diagnostic mutated device provenance: null owners=%d", nullOwners)
	}

	// Complete D1 lookup identity is still insufficient without the protected
	// target. The caller flags never authorize execution and validation must
	// reject this request before any database access or mutation.
	var executeOut, executeErr bytes.Buffer
	args := []string{
		"-database-url", dsn, "-execute",
		"-d1-operation-id", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c101",
		"-d1-attempt-id", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c102",
		"-d1-lease-id", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c103",
		"-d1-generation", "1",
		"-d1-target-fingerprint", strings.Repeat("ab", 32),
		"-d1-evidence-digest", strings.Repeat("cd", 32),
	}
	if code := run(args, &executeOut, &executeErr); code != 2 {
		t.Fatalf("execute without target code=%d out=%s err=%s", code, executeOut.String(), executeErr.String())
	}
	if executeErr.String() != "target-id is required\n" || executeOut.Len() != 0 {
		t.Fatalf("missing target output=%q stderr=%q", executeOut.String(), executeErr.String())
	}
	var remainingNullOwners int
	if err := db.QueryRow(`SELECT count(*) FROM devices WHERE inventory_owner_client_id IS NULL`).Scan(&remainingNullOwners); err != nil {
		t.Fatal(err)
	}
	if remainingNullOwners != nullOwners {
		t.Fatalf("missing target mutated device provenance: before=%d after=%d", nullOwners, remainingNullOwners)
	}
}
