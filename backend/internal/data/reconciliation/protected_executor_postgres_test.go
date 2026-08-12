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
		digest, err := MappingSourceFactsDigest(facts)
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

func TestProtectedExecutionLocksEntityPrefixBeforeFrozenCollection(t *testing.T) {
	db := protectedTestDB(t)
	createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	prefixReached := false
	executor.hooks.AfterEntityLockPrefix = func(ctx context.Context, tx *sql.Tx) error {
		var locked bool
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) = 3
			  FROM pg_locks AS lock
			  JOIN pg_class AS relation ON relation.oid = lock.relation
			 WHERE lock.pid = pg_backend_pid()
			   AND lock.mode = 'RowShareLock'
			   AND lock.granted
			   AND relation.relname IN ('devices', 'measurement_points', 'device_assignments')`).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return errors.New("entity lock prefix did not hold all three table locks")
		}
		prefixReached = true
		return nil
	}
	executor.hooks.AfterFrozenTime = func(context.Context, *sql.Tx, time.Time) error {
		if !prefixReached {
			return errors.New("frozen time sampled before entity lock prefix")
		}
		return nil
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err != nil || report.Outcome != ExecutionCommittedAndVerified {
		t.Fatalf("report=%+v err=%v", report, err)
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
		digest, err := MappingSourceFactsDigest(facts)
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

func waitProtectedSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
func waitForBlockedSharedLock(t *testing.T, db *sql.DB, pid int64) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("writer pid %d never became an observable blocked advisory waiter", pid)
		case <-ticker.C:
		}
	}
}

func waitForExclusiveFenceWaiter(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query ILIKE '%pg_advisory_lock%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("exclusive fence request was not observable as waiting")
		case <-ticker.C:
		}
	}
}
func assertWriterFenceAvailable(t *testing.T, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var acquired bool
	if err := conn.QueryRowContext(context.Background(), `SELECT pg_try_advisory_lock($1::bigint)`, migrations.WriterFenceKey).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("writer fence remained held")
	}
	var unlocked bool
	if err := conn.QueryRowContext(context.Background(), `SELECT pg_advisory_unlock($1::bigint)`, migrations.WriterFenceKey).Scan(&unlocked); err != nil || !unlocked {
		t.Fatalf("unlock=%t err=%v", unlocked, err)
	}
}
func assertAuditTriggerRestored(t *testing.T, db *sql.DB, auditID uuid.UUID) {
	t.Helper()
	var enabled string
	if err := db.QueryRow(`SELECT tgenabled FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid WHERE c.relname='admin_binding_audits' AND t.tgname='admin_binding_audits_immutable'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != "O" {
		t.Fatalf("trigger enabled=%q", enabled)
	}
	if _, err := db.Exec(`UPDATE admin_binding_audits SET client_id = NULL WHERE id = $1`, auditID); err == nil {
		t.Fatal("immutable audit trigger was not restored")
	}
}

func TestProtectedExecutionDrainsExistingCooperativeWriter(t *testing.T) {
	db := protectedTestDB(t)
	createProtectedFixture(t, db, false)
	writerTx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.AcquireSharedWriterFence(context.Background(), writerTx); err != nil {
		_ = writerTx.Rollback()
		t.Fatal(err)
	}
	admitted := make(chan struct{})
	executor := NewProtectedExecutor(nil)
	executor.hooks.AfterFence = func(context.Context, *migrations.ExclusiveWriterFence) error { close(admitted); return nil }
	started := make(chan struct{})
	result := make(chan struct {
		report ExecutionReport
		err    error
	}, 1)
	go func() {
		close(started)
		r, e := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
		result <- struct {
			report ExecutionReport
			err    error
		}{r, e}
	}()
	waitProtectedSignal(t, started, "executor start")
	waitForExclusiveFenceWaiter(t, db)
	if err := writerTx.Commit(); err != nil {
		t.Fatal(err)
	}
	waitProtectedSignal(t, admitted, "exclusive admission after shared writer drain")
	got := <-result
	if got.err != nil || got.report.Outcome != ExecutionCommittedAndVerified {
		t.Fatalf("report=%+v err=%v", got.report, got.err)
	}
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionBlocksNewCooperativeWriterUntilRelease(t *testing.T) {
	db := protectedTestDB(t)
	createProtectedFixture(t, db, false)
	release, admitted := make(chan struct{}), make(chan struct{})
	executor := NewProtectedExecutor(nil)
	executor.hooks.AfterFreshPlan = func(context.Context, *sql.Tx, FactSet, Plan) error { close(admitted); <-release; return nil }
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
		result <- err
	}()
	waitProtectedSignal(t, started, "executor start")
	waitProtectedSignal(t, admitted, "protected execution")
	writerTx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var writerPID int64
	if err := writerTx.QueryRow(`SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	entered, writerErr := make(chan struct{}), make(chan error, 1)
	go func() {
		err := migrations.AcquireSharedWriterFence(context.Background(), writerTx)
		if err == nil {
			close(entered)
		}
		writerErr <- err
	}()
	waitForBlockedSharedLock(t, db, writerPID)
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	waitProtectedSignal(t, entered, "new cooperative writer after release")
	if err := <-writerErr; err != nil {
		t.Fatal(err)
	}
	_ = writerTx.Rollback()
	assertWriterFenceAvailable(t, db)
}

func TestSecondProtectedExecutionWaitsAndFreshlyConverges(t *testing.T) {
	db := protectedTestDB(t)
	createProtectedFixture(t, db, false)
	release, firstAdmitted, secondAdmitted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	first, second := NewProtectedExecutor(nil), NewProtectedExecutor(nil)
	first.hooks.AfterFreshPlan = func(context.Context, *sql.Tx, FactSet, Plan) error { close(firstAdmitted); <-release; return nil }
	second.hooks.AfterFence = func(context.Context, *migrations.ExclusiveWriterFence) error { close(secondAdmitted); return nil }
	type result struct {
		report ExecutionReport
		err    error
	}
	firstResult, secondResult := make(chan result, 1), make(chan result, 1)
	firstStarted, secondStarted := make(chan struct{}), make(chan struct{})
	go func() {
		close(firstStarted)
		r, e := first.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
		firstResult <- result{r, e}
	}()
	waitProtectedSignal(t, firstStarted, "first executor start")
	waitProtectedSignal(t, firstAdmitted, "first protected execution")
	go func() {
		close(secondStarted)
		r, e := second.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
		secondResult <- result{r, e}
	}()
	waitProtectedSignal(t, secondStarted, "second executor start")
	waitForExclusiveFenceWaiter(t, db)
	close(release)
	one := <-firstResult
	if one.err != nil || one.report.Outcome != ExecutionCommittedAndVerified {
		t.Fatalf("first=%+v err=%v", one.report, one.err)
	}
	waitProtectedSignal(t, secondAdmitted, "second reconciler admission")
	two := <-secondResult
	if two.err != nil || two.report.Outcome != ExecutionCommittedAndVerified {
		t.Fatalf("second=%+v err=%v", two.report, two.err)
	}
	if two.report.AppliedAffectedCounts[ExpectedCountInventoryOwnerUpdates] != 0 {
		t.Fatalf("second run mutated: %+v", two.report.AppliedAffectedCounts)
	}
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionInjectedFailureAfterWriteRollsBackDurably(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	executor.hooks.AfterWrite = func(context.Context, *sql.Tx, FactSet, Plan, PlanItem, int) error {
		return errors.New("injected after-write failure")
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionNotCommitted || report.Committed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("rollback left owner=%d", owner.Int64)
	}
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionAdminFailureRestoresTriggerAndRollsBack(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, true)
	executor := NewProtectedExecutor(nil)
	executor.hooks.AfterWrite = func(_ context.Context, _ *sql.Tx, _ FactSet, _ Plan, item PlanItem, _ int) error {
		if item.AuditID != uuid.Nil {
			return errors.New("injected audit failure")
		}
		return nil
	}
	report, err := executor.ExecuteWithMappingResolver(context.Background(), os.Getenv("TEST_DATABASE_URL"), artifactResolverFor(fixture.operation, uint(fixture.client1)))
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	var op, audit sql.NullInt64
	if err := db.QueryRow(`SELECT client_id FROM admin_binding_operations WHERE operation_id=$1`, fixture.operation).Scan(&op); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT client_id FROM admin_binding_audits WHERE id=$1`, fixture.audit).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if op.Valid || audit.Valid {
		t.Fatalf("admin rollback op/audit=%v/%v", op, audit)
	}
	assertAuditTriggerRestored(t, db, fixture.audit)
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionPreCommitFailureRollsBack(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	executor.hooks.BeforeCommit = func(context.Context, *sql.Tx, FactSet, Plan) error { return errors.New("injected pre-commit failure") }
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("precommit left owner=%d", owner.Int64)
	}
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionTriggerRestoreFailureRollsBackAndRestores(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, true)
	executor := NewProtectedExecutor(nil)
	executor.hooks.BeforeTriggerRestore = func(context.Context, *sql.Tx) error { return errors.New("injected trigger restore failure") }
	report, err := executor.ExecuteWithMappingResolver(context.Background(), os.Getenv("TEST_DATABASE_URL"), artifactResolverFor(fixture.operation, uint(fixture.client1)))
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	var audit sql.NullInt64
	if err := db.QueryRow(`SELECT client_id FROM admin_binding_audits WHERE id=$1`, fixture.audit).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if audit.Valid {
		t.Fatalf("trigger failure left audit=%d", audit.Int64)
	}
	assertAuditTriggerRestored(t, db, fixture.audit)
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionCommitAmbiguityDoesNotAutoRetry(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	executor.hooks.Commit = func(_ context.Context, tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("commit acknowledgement lost")
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionCommitOutcomeUnknown || report.Committed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	var owner int64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != fixture.client1 {
		t.Fatalf("ambiguous durable owner=%d want=%d", owner, fixture.client1)
	}
	fresh, err := NewProtectedExecutor(nil).Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err != nil || fresh.Outcome != ExecutionCommittedAndVerified || fresh.AppliedAffectedCounts[ExpectedCountInventoryOwnerUpdates] != 0 {
		t.Fatalf("fresh recovery report=%+v err=%v", fresh, err)
	}
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionCleanupFailurePreservesCommittedOutcome(t *testing.T) {
	db := protectedTestDB(t)
	createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	executor.hooks.CloseFence = func(f *migrations.ExclusiveWriterFence) error {
		if err := f.Close(); err != nil {
			return err
		}
		return errors.New("injected cleanup report failure")
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionCommittedAndVerified || report.CleanupError == "" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionConnectionLossBeforeCommitRollsBack(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	executor.hooks.AfterWrite = func(ctx context.Context, tx *sql.Tx, _ FactSet, _ Plan, _ PlanItem, _ int) error {
		var pid int64
		if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		var terminated bool
		if err := db.QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil {
			return err
		}
		if !terminated {
			return errors.New("backend termination rejected")
		}
		return nil
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("connection-loss left owner=%d", owner.Int64)
	}
	assertWriterFenceAvailable(t, db)
}

func TestProtectedExecutionConnectionLossAfterCommitIsPostverifyFailure(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	executor := NewProtectedExecutor(nil)
	executor.hooks.AfterCommit = func(ctx context.Context, conn *sql.Conn) error {
		var pid int64
		if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		var terminated bool
		if err := db.QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil {
			return err
		}
		if !terminated {
			return errors.New("post-commit termination rejected")
		}
		return nil
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err == nil || report.Outcome != ExecutionCommittedPostVerifyFailed || !report.Committed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	var owner int64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != fixture.client1 {
		t.Fatalf("postcommit owner=%d want=%d", owner, fixture.client1)
	}
}

func TestProtectedExecutionFreshMappingRaceIsRejected(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	diagnostic, err := DiagnoseV5(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: diagnostic.MappingBasisDigest, Mappings: []MappingEntry{{Category: MappingDevice, DeviceID: uint(fixture.device), ClientID: uint(fixture.client1)}}}
	if _, err := db.Exec(`UPDATE devices SET inventory_owner_client_id=$1 WHERE id=$2`, fixture.client2, fixture.device); err != nil {
		t.Fatal(err)
	}
	report, err := NewProtectedExecutor(nil).Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), artifact)
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("stale report=%+v err=%v", report, err)
	}
	var owner int64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != fixture.client2 {
		t.Fatalf("stale mapping changed owner=%d", owner)
	}
}

func TestProtectedExecutionIdempotentRerunAndPartialAdminRecovery(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, true)
	resolver := artifactResolverFor(fixture.operation, uint(fixture.client1))
	first, err := NewProtectedExecutor(nil).ExecuteWithMappingResolver(context.Background(), os.Getenv("TEST_DATABASE_URL"), resolver)
	if err != nil || first.Outcome != ExecutionCommittedAndVerified {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := NewProtectedExecutor(nil).Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err != nil || second.Outcome != ExecutionCommittedAndVerified {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if second.AppliedAffectedCounts[ExpectedCountInventoryOwnerUpdates] != 0 || second.AppliedAffectedCounts[ExpectedCountAdminClientUpdates] != 0 {
		t.Fatalf("rerun mutated=%v", second.AppliedAffectedCounts)
	}
	fixture = createProtectedFixture(t, db, true)
	if _, err := db.Exec(`UPDATE admin_binding_operations SET client_id=$1 WHERE operation_id=$2`, fixture.client1, fixture.operation); err != nil {
		t.Fatal(err)
	}
	partial, err := NewProtectedExecutor(nil).ExecuteWithMappingResolver(context.Background(), os.Getenv("TEST_DATABASE_URL"), artifactResolverFor(fixture.operation, uint(fixture.client1)))
	if err != nil || partial.Outcome != ExecutionCommittedAndVerified || partial.AppliedAffectedCounts[ExpectedCountAdminClientUpdates] != 1 {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
}

func TestProtectedExecutionContradictoryAdminProvenanceBlocks(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, true)
	if _, err := db.Exec(`UPDATE admin_binding_operations SET client_id=$1 WHERE operation_id=$2`, fixture.client2, fixture.operation); err != nil {
		t.Fatal(err)
	}
	report, err := NewProtectedExecutor(nil).ExecuteWithMappingResolver(context.Background(), os.Getenv("TEST_DATABASE_URL"), artifactResolverFor(fixture.operation, uint(fixture.client1)))
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("contradictory=%+v err=%v", report, err)
	}
	var audit sql.NullInt64
	if err := db.QueryRow(`SELECT client_id FROM admin_binding_audits WHERE id=$1`, fixture.audit).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if audit.Valid {
		t.Fatalf("contradictory changed audit=%d", audit.Int64)
	}
}
