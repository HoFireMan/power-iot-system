//go:build securityintegration

package reconciliation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"power-iot-backend/internal/data/migrations"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type protectedFixture struct {
	client1    int64
	client2    int64
	shop1      int64
	shop2      int64
	user       int64
	device     int64
	point      uuid.UUID
	assignment uuid.UUID
	operation  uuid.UUID
	audit      uuid.UUID
}

func protectedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}
	if strings.Contains(strings.ToLower(dsn), "power_iot") {
		t.Fatal("protected reconciliation tests must not use power_iot")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func resetProtectedFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	// This is a package-local disposable database created by TestMain. The
	// source and baseline security databases are never truncated.
	if _, err := db.Exec(`TRUNCATE TABLE clients, users, devices, device_types RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func createProtectedFixture(t *testing.T, db *sql.DB, withAdmin bool) protectedFixture {
	t.Helper()
	resetProtectedFixture(t, db)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	var f protectedFixture
	if err := db.QueryRow(`INSERT INTO clients (code, name) VALUES ($1, $2) RETURNING id`, "a22-client-"+suffix[:12], "A22 Client").Scan(&f.client1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO clients (code, name) VALUES ($1, $2) RETURNING id`, "a22-other-"+suffix[:12], "A22 Other Client").Scan(&f.client2); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, $2, $3) RETURNING id`, f.client1, "a22-shop-"+suffix[:12], "A22 Shop").Scan(&f.shop1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, $2, $3) RETURNING id`, f.client2, "a22-other-shop-"+suffix[:12], "A22 Other Shop").Scan(&f.shop2); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (account, password_hash, name, current_shop_id, auth_enabled) VALUES ($1, $2, $3, $4, true) RETURNING id`, "a22-user-"+suffix[:12], "test-hash", "A22 User", f.shop1).Scan(&f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2)`, f.user, f.shop1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, $2, $3) RETURNING id`, f.shop2, strings.ToUpper(suffix[12:24]), "A22 Device").Scan(&f.device); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO measurement_points (shop_id, name) VALUES ($1, $2) RETURNING id`, f.shop1, "A22 Point").Scan(&f.point); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES ($1, $2, $3) RETURNING id`, f.device, f.point, time.Now().UTC().Add(-time.Hour)).Scan(&f.assignment); err != nil {
		t.Fatal(err)
	}
	if withAdmin {
		f.operation = uuid.New()
		hash := sha256.Sum256([]byte("a22-operation-" + suffix))
		if _, err := db.Exec(`INSERT INTO admin_binding_operations (operation_id, idempotency_key, operation, scope_key, actor_id, canonical_request_hash) VALUES ($1, $2, 'bind', $3, $4, $5)`, f.operation, "a22-idempotency-"+suffix[:12], "a22-scope-"+suffix[:12], f.user, hash[:]); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`INSERT INTO admin_binding_audits (operation_id, request_identity, actor_id, scope_key, action, shop_id, device_id, new_measurement_point_id, new_assignment_id) VALUES ($1, $2, $3, $4, 'bind', $5, $6, $7, $8) RETURNING id`, f.operation, "a22-request-"+suffix[:12], f.user, "a22-scope-"+suffix[:12], f.shop1, f.device, f.point, f.assignment).Scan(&f.audit); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func artifactResolverFor(operation uuid.UUID, client uint) MappingResolver {
	return func(_ context.Context, facts FactSet) (*MappingArtifact, error) {
		_, digest, err := CanonicalSourceFacts(facts)
		if err != nil {
			return nil, err
		}
		mappings := []MappingEntry(nil)
		if operation != uuid.Nil {
			mappings = []MappingEntry{{Category: MappingAdminProvenance, OperationID: operation, ClientID: client}}
		}
		return &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(digest), Mappings: mappings}, nil
	}
}

func TestProtectedExecutionReconcilesDeviceAndAdminAtomically(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, true)
	executor := NewProtectedExecutor(nil)
	var transactionPID, postCommitPID int64
	var transactionIsolation, transactionReadOnly string
	var fenceAvailableAfterCommit bool
	executor.hooks.BeforeWrite = func(ctx context.Context, tx *sql.Tx, _ FactSet, _ Plan) error {
		if err := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&transactionIsolation); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SHOW transaction_read_only`).Scan(&transactionReadOnly); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&transactionPID)
	}
	executor.hooks.AfterCommit = func(ctx context.Context, conn *sql.Conn) error {
		if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&postCommitPID); err != nil {
			return err
		}
		probeDB, err := sql.Open("postgres", os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			return err
		}
		defer probeDB.Close()
		return probeDB.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, migrations.WriterFenceKey).Scan(&fenceAvailableAfterCommit)
	}
	report, err := executor.ExecuteWithMappingResolver(context.Background(), os.Getenv("TEST_DATABASE_URL"), artifactResolverFor(fixture.operation, uint(fixture.client1)))
	if err != nil {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if report.Outcome != ExecutionCommittedAndVerified || !report.Committed || !report.PostCommitVerified || report.BackendPID == 0 {
		t.Fatalf("unexpected execution report=%+v", report)
	}
	if transactionPID != report.BackendPID || postCommitPID != report.BackendPID || transactionIsolation != "repeatable read" || transactionReadOnly != "off" || fenceAvailableAfterCommit {
		t.Fatalf("pinned lifecycle pid=%d/%d/%d isolation=%q read_only=%q fence_available=%t", report.BackendPID, transactionPID, postCommitPID, transactionIsolation, transactionReadOnly, fenceAvailableAfterCommit)
	}
	if report.AppliedAffectedCounts[ExpectedCountInventoryOwnerUpdates] != 1 || report.AppliedAffectedCounts[ExpectedCountAdminClientUpdates] != 2 {
		t.Fatalf("affected counts=%v", report.AppliedAffectedCounts)
	}
	var owner, operationClient, auditClient int64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id = $1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT client_id FROM admin_binding_operations WHERE operation_id = $1`, fixture.operation).Scan(&operationClient); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT client_id FROM admin_binding_audits WHERE id = $1`, fixture.audit).Scan(&auditClient); err != nil {
		t.Fatal(err)
	}
	if owner != fixture.client1 || operationClient != fixture.client1 || auditClient != fixture.client1 {
		t.Fatalf("enrichment owner/op/audit=%d/%d/%d want %d", owner, operationClient, auditClient, fixture.client1)
	}
	var enabled, functionName string
	if err := db.QueryRow(`SELECT t.tgenabled, p.proname FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_proc p ON p.oid=t.tgfoid WHERE c.relname='admin_binding_audits' AND t.tgname='admin_binding_audits_immutable'`).Scan(&enabled, &functionName); err != nil {
		t.Fatal(err)
	}
	if enabled != "O" || functionName != "prevent_admin_binding_audit_mutation" {
		t.Fatalf("trigger state=%q function=%q", enabled, functionName)
	}
	if _, err := db.Exec(`UPDATE admin_binding_audits SET client_id = NULL WHERE id = $1`, fixture.audit); err == nil {
		t.Fatal("ordinary audit update bypassed restored immutable trigger")
	}
	var authEnabled bool
	var currentShop int64
	if err := db.QueryRow(`SELECT auth_enabled, current_shop_id FROM users WHERE id = $1`, fixture.user).Scan(&authEnabled, &currentShop); err != nil {
		t.Fatal(err)
	}
	if !authEnabled || currentShop != fixture.shop1 {
		t.Fatal("user authorization evidence changed")
	}
	var deviceShop int64
	if err := db.QueryRow(`SELECT shop_id FROM devices WHERE id = $1`, fixture.device).Scan(&deviceShop); err != nil {
		t.Fatal(err)
	}
	if deviceShop != fixture.shop2 {
		t.Fatal("Device.ShopID was changed or used as ownership authority")
	}
}

func TestProtectedExecutionAlreadyConsistentIsAtomicNoOp(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	if _, err := db.Exec(`UPDATE devices SET inventory_owner_client_id = $1 WHERE id = $2`, fixture.client1, fixture.device); err != nil {
		t.Fatal(err)
	}
	report, err := NewProtectedExecutor(nil).Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err != nil {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if report.Outcome != ExecutionCommittedAndVerified || report.AppliedAffectedCounts[ExpectedCountInventoryOwnerUpdates] != 0 {
		t.Fatalf("unexpected no-op report=%+v", report)
	}
}

func TestProtectedExecutionStaleMappingAndBlockerDoZeroWrites(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	stale := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: strings.Repeat("0", 64), Mappings: []MappingEntry{{Category: MappingDevice, DeviceID: uint(fixture.device), ClientID: uint(fixture.client1)}}}
	report, err := NewProtectedExecutor(nil).Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), stale)
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("stale mapping report=%+v err=%v", report, err)
	}
	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id = $1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("stale mapping wrote owner=%d", owner.Int64)
	}
	var point2 uuid.UUID
	if err := db.QueryRow(`INSERT INTO measurement_points (shop_id, name) VALUES ($1, 'A22 Future Point') RETURNING id`, fixture.shop2).Scan(&point2); err != nil {
		t.Fatal(err)
	}
	futureStart := time.Now().UTC().Add(time.Hour)
	if _, err := db.Exec(`UPDATE device_assignments SET valid_to = $1 WHERE id = $2`, futureStart, fixture.assignment); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES ($1, $2, $3)`, fixture.device, point2, futureStart); err != nil {
		t.Fatal(err)
	}
	report, err = NewProtectedExecutor(nil).Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("blocking report=%+v err=%v", report, err)
	}
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id = $1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("blocking plan wrote owner=%d", owner.Int64)
	}
}

func TestProtectedExecutionCASMismatchRollsBack(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	executor.hooks.BeforeWrite = func(_ context.Context, tx *sql.Tx, _ FactSet, _ Plan) error {
		_, err := tx.Exec(`UPDATE devices SET inventory_owner_client_id = $1 WHERE id = $2`, fixture.client2, fixture.device)
		return err
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionNotCommitted || !errors.Is(err, ErrProtectedCASConflict) {
		t.Fatalf("CAS report=%+v err=%v", report, err)
	}
	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id = $1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("CAS rollback left owner=%d", owner.Int64)
	}
}

func TestProtectedExecutionPostCommitVerificationFailureIsNotSuccess(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	executor.hooks.AfterCommit = func(ctx context.Context, conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE devices SET inventory_owner_client_id = $1 WHERE id = $2`, fixture.client2, fixture.device)
		return err
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionCommittedPostVerifyFailed || !report.Committed || report.PostCommitVerified {
		t.Fatalf("postverify report=%+v err=%v", report, err)
	}
	var owner int64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id = $1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != fixture.client2 {
		t.Fatalf("postverify fixture owner=%d want %d", owner, fixture.client2)
	}
}

func TestProtectedExecutionUsesFreshMappingDigest(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	resolverCalls := 0
	resolver := func(_ context.Context, facts FactSet) (*MappingArtifact, error) {
		resolverCalls++
		_, digest, err := CanonicalSourceFacts(facts)
		if err != nil {
			return nil, err
		}
		return &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(digest)}, nil
	}
	report, err := NewProtectedExecutor(nil).ExecuteWithMappingResolver(context.Background(), os.Getenv("TEST_DATABASE_URL"), resolver)
	if err != nil {
		t.Fatalf("fresh resolver report=%+v err=%v", report, err)
	}
	if resolverCalls != 1 || report.SourceFactsDigest == "" {
		t.Fatalf("resolver calls/report=%d/%+v", resolverCalls, report)
	}
	var owner int64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id = $1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != fixture.client1 {
		t.Fatalf("fresh execution owner=%d want %d", owner, fixture.client1)
	}
}
