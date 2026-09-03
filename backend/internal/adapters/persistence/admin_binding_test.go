package persistence

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	privatemigrations "power-iot-backend/internal/data/private_migrations"
)

type persistenceFixture struct {
	db           *gorm.DB
	shopID       uint
	actorID      uint
	deviceID     uint
	otherDevice  uint
	pointID      uuid.UUID
	otherPointID uuid.UUID
}

func openPersistenceDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; persistence integration test not run")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, migrationFile := range []string{
		"sql/000007_b02_coverage_foundation.up.sql",
		"sql/000008_dashboard_carbon_summary.up.sql",
		"sql/000012_device_retirement_lifecycle.up.sql",
	} {
		body, err := fs.ReadFile(privatemigrations.Files, migrationFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(string(body)).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func newPersistenceFixture(t *testing.T, db *gorm.DB) persistenceFixture {
	t.Helper()
	suffix := uuid.NewString()
	hex := strings.ReplaceAll(suffix, "-", "")
	var fixture persistenceFixture
	if err := db.Raw(`INSERT INTO shops (code, name) VALUES (?, ?) RETURNING id`, "persist-"+suffix[:8], "Persistence Test Shop").Scan(&fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO users (account, password_hash, name) VALUES (?, ?, ?) RETURNING id`, "actor-"+suffix[:8], "test-hash", "Test Actor").Scan(&fixture.actorID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO devices (shop_id, mac_address, serial_number, name) VALUES (?, ?, ?, ?) RETURNING id`, fixture.shopID, "AA"+strings.ToUpper(hex[0:10]), "SERIAL-"+suffix, "device-a").Scan(&fixture.deviceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO devices (shop_id, mac_address, serial_number, name) VALUES (?, ?, ?, ?) RETURNING id`, fixture.shopID, "BB"+strings.ToUpper(hex[10:20]), "SERIAL-B-"+suffix, "device-b").Scan(&fixture.otherDevice).Error; err != nil {
		t.Fatal(err)
	}
	var pointText, otherPointText string
	if err := db.Raw(`INSERT INTO measurement_points (shop_id, name) VALUES (?, ?) RETURNING id`, fixture.shopID, "MP-A").Scan(&pointText).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO measurement_points (shop_id, name) VALUES (?, ?) RETURNING id`, fixture.shopID, "MP-B").Scan(&otherPointText).Error; err != nil {
		t.Fatal(err)
	}
	var err error
	fixture.pointID, err = uuid.Parse(pointText)
	if err != nil {
		t.Fatal(err)
	}
	fixture.otherPointID, err = uuid.Parse(otherPointText)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func insertAssignment(t *testing.T, db *gorm.DB, deviceID uint, pointID uuid.UUID, from time.Time, to *time.Time) uuid.UUID {
	t.Helper()
	var idText string
	if err := db.Raw(`
		INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to)
		VALUES (?, ?, ?, ?) RETURNING id`, deviceID, pointID, from, to).Scan(&idText).Error; err != nil {
		t.Fatal(err)
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertReading(t *testing.T, db *gorm.DB, deviceID uint, pointID uuid.UUID, recordedAt time.Time) {
	t.Helper()
	if err := db.Exec(`
		INSERT INTO power_readings
		(time, recorded_at, received_at, measurement_point_id, device_id, voltage, current, active_power)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, recordedAt, recordedAt, recordedAt.Add(time.Minute), pointID, deviceID, 110, 1.2, 132).Error; err != nil {
		t.Fatal(err)
	}
}

func uintPtr(value uint) *uint           { return &value }
func uuidPtr(value uuid.UUID) *uuid.UUID { return &value }

func insertCommittedOperation(t *testing.T, db *gorm.DB, actorID uint, scopeKey, operation string) uuid.UUID {
	t.Helper()
	operationID := uuid.New()
	hash := sha256.Sum256([]byte(operationID.String()))
	if err := db.Exec(`
		INSERT INTO admin_binding_operations
		(operation_id, idempotency_key, operation, scope_key, actor_id, canonical_request_hash, committed_response, committed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?)`, operationID, "audit-key-"+uuid.NewString(), operation, scopeKey, actorID, hash[:], `{}`, time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	return operationID
}

func TestAuditPersistenceSupportsCreateAndAssignmentActions(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	assignmentID := insertAssignment(t, db, fixture.deviceID, fixture.pointID, start, nil)
	scope := fmt.Sprintf("shop:%d", fixture.shopID)

	createAudit := &domain.AdminBindingAudit{
		OperationID:        insertCommittedOperation(t, db, fixture.actorID, scope, "create_measurement_point"),
		RequestIdentity:    "create-" + uuid.NewString(),
		ActorID:            fixture.actorID,
		ScopeKey:           scope,
		Action:             "create_measurement_point",
		ShopID:             uintPtr(fixture.shopID),
		MeasurementPointID: uuidPtr(fixture.otherPointID),
		ScopeSnapshot:      json.RawMessage(`{"shop_id":1}`),
		Metadata:           json.RawMessage(`{"outcome":"committed"}`),
	}
	if err := AppendAdminBindingAudit(db, createAudit); err != nil {
		t.Fatal(err)
	}

	effective := start.Add(time.Hour)
	assignmentAudit := &domain.AdminBindingAudit{
		OperationID:           insertCommittedOperation(t, db, fixture.actorID, scope, "bind"),
		RequestIdentity:       "bind-" + uuid.NewString(),
		ActorID:               fixture.actorID,
		ScopeKey:              scope,
		Action:                "bind",
		EffectiveAt:           &effective,
		DeviceID:              uintPtr(fixture.deviceID),
		NewMeasurementPointID: uuidPtr(fixture.pointID),
		NewAssignmentID:       uuidPtr(assignmentID),
		DeviceMAC:             func() *string { value := "AABBCCDDEEFF"; return &value }(),
	}
	if err := AppendAdminBindingAudit(db, assignmentAudit); err != nil {
		t.Fatal(err)
	}

	var count int64
	if err := db.Model(&domain.AdminBindingAudit{}).Where("id IN (?, ?)", createAudit.ID, assignmentAudit.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected two audit rows, got %d", count)
	}
}

func TestAuditOperationProvenanceIntegrity(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	scope := fmt.Sprintf("shop:%d", fixture.shopID)

	operationID := insertCommittedOperation(t, db, fixture.actorID, scope, "bind")
	valid := &domain.AdminBindingAudit{
		OperationID:     operationID,
		RequestIdentity: "provenance-valid-" + uuid.NewString(),
		ActorID:         fixture.actorID,
		ScopeKey:        scope,
		Action:          "bind",
	}
	if err := AppendAdminBindingAudit(db, valid); err != nil {
		t.Fatal("matching operation/audit was rejected:", err)
	}

	duplicate := *valid
	duplicate.ID = uuid.New()
	duplicate.RequestIdentity = "provenance-duplicate-" + uuid.NewString()
	if err := AppendAdminBindingAudit(db, &duplicate); err == nil {
		t.Fatal("one operation accepted multiple success audits")
	}

	mismatchedAction := *valid
	mismatchedAction.ID = uuid.New()
	mismatchedAction.OperationID = insertCommittedOperation(t, db, fixture.actorID, scope, "bind")
	mismatchedAction.RequestIdentity = "provenance-action-" + uuid.NewString()
	mismatchedAction.Action = "relocate"
	if err := AppendAdminBindingAudit(db, &mismatchedAction); err == nil {
		t.Fatal("audit action did not match parent operation")
	}

	var otherActor uint
	if err := db.Raw(`INSERT INTO users (account, password_hash, name) VALUES (?, ?, ?) RETURNING id`, "actor-"+uuid.NewString()[:8], "test-hash", "Other Actor").Scan(&otherActor).Error; err != nil {
		t.Fatal(err)
	}
	mismatchedActor := *valid
	mismatchedActor.ID = uuid.New()
	mismatchedActor.OperationID = insertCommittedOperation(t, db, fixture.actorID, scope, "bind")
	mismatchedActor.RequestIdentity = "provenance-actor-" + uuid.NewString()
	mismatchedActor.ActorID = otherActor
	if err := AppendAdminBindingAudit(db, &mismatchedActor); err == nil {
		t.Fatal("audit actor did not match parent operation")
	}

	mismatchedScope := *valid
	mismatchedScope.ID = uuid.New()
	mismatchedScope.OperationID = insertCommittedOperation(t, db, fixture.actorID, scope, "bind")
	mismatchedScope.RequestIdentity = "provenance-scope-" + uuid.NewString()
	mismatchedScope.ScopeKey = "shop:other"
	if err := AppendAdminBindingAudit(db, &mismatchedScope); err == nil {
		t.Fatal("audit scope did not match parent operation")
	}
}

func TestAuditPersistenceRejectsInvalidReferencesAndMutation(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	scope := fmt.Sprintf("shop:%d", fixture.shopID)
	audit := &domain.AdminBindingAudit{
		OperationID:     insertCommittedOperation(t, db, fixture.actorID, scope, "unbind"),
		RequestIdentity: "immutable-" + uuid.NewString(),
		ActorID:         fixture.actorID,
		ScopeKey:        scope,
		Action:          "unbind",
		DeviceID:        uintPtr(fixture.deviceID),
	}
	if err := AppendAdminBindingAudit(db, audit); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.AdminBindingAudit{}).Where("id = ?", audit.ID).Update("reason", "changed").Error; err == nil {
		t.Fatal("audit UPDATE was accepted")
	}
	if err := db.Delete(&domain.AdminBindingAudit{}, "id = ?", audit.ID).Error; err == nil {
		t.Fatal("audit DELETE was accepted")
	}

	invalid := *audit
	invalid.ID = uuid.New()
	invalid.OperationID = insertCommittedOperation(t, db, fixture.actorID, scope, "unbind")
	invalid.RequestIdentity = "invalid-" + uuid.NewString()
	invalid.DeviceID = uintPtr(999999999)
	if err := AppendAdminBindingAudit(db, &invalid); err == nil {
		t.Fatal("audit with invalid device reference was accepted")
	}
}

func TestAdminBindingTransactionRollbackIsAtomic(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	scope := fmt.Sprintf("shop:%d", fixture.shopID)
	hash := sha256.Sum256([]byte(`{"shop_id":1,"name":"atomic rollback"}`))
	operation := &domain.AdminBindingOperation{
		OperationID:          uuid.New(),
		IdempotencyKey:       "atomic-rollback-" + uuid.NewString(),
		Operation:            "create_measurement_point",
		ScopeKey:             scope,
		ActorID:              fixture.actorID,
		CanonicalRequestHash: hash[:],
	}

	tx := db.Begin()
	claimed, didClaim, err := ClaimAdminBindingOperation(tx, operation)
	if err != nil || !didClaim {
		tx.Rollback()
		t.Fatalf("claim in atomic transaction: claimed=%t err=%v", didClaim, err)
	}
	point := &domain.MeasurementPoint{ID: uuid.New(), ShopID: fixture.shopID, Name: "Atomic Rollback MP"}
	if err := tx.Create(point).Error; err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	audit := &domain.AdminBindingAudit{
		OperationID:        claimed.OperationID,
		RequestIdentity:    operation.IdempotencyKey,
		ActorID:            fixture.actorID,
		ScopeKey:           scope,
		Action:             "create_measurement_point",
		ShopID:             uintPtr(fixture.shopID),
		MeasurementPointID: uuidPtr(point.ID),
	}
	if err := AppendAdminBindingAudit(tx, audit); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := CommitAdminBindingOperation(tx, claimed.OperationID, json.RawMessage(`{"result":"created"}`), time.Now().UTC()); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}

	var pointCount, auditCount, operationCount int64
	if err := db.Model(&domain.MeasurementPoint{}).Where("id = ?", point.ID).Count(&pointCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.AdminBindingAudit{}).Where("id = ?", audit.ID).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.AdminBindingOperation{}).Where("operation_id = ?", claimed.OperationID).Count(&operationCount).Error; err != nil {
		t.Fatal(err)
	}
	if pointCount != 0 || auditCount != 0 || operationCount != 0 {
		t.Fatalf("rollback left rows: point=%d audit=%d operation=%d", pointCount, auditCount, operationCount)
	}

	reclaimedOperation := operation
	reclaimTx := db.Begin()
	if _, didClaim, err := ClaimAdminBindingOperation(reclaimTx, reclaimedOperation); err != nil || !didClaim {
		reclaimTx.Rollback()
		t.Fatalf("rolled-back idempotency key was not reclaimable: claimed=%t err=%v", didClaim, err)
	}
	if err := reclaimTx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
}

func TestIdempotencyLedgerScopedUniquenessAndReplayPayload(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	hash := sha256.Sum256([]byte(`{"shop_id":1,"name":"MP-A"}`))
	operation := &domain.AdminBindingOperation{
		OperationID:          uuid.New(),
		IdempotencyKey:       "same-key-" + uuid.NewString(),
		Operation:            "create_measurement_point",
		ScopeKey:             fmt.Sprintf("shop:%d", fixture.shopID),
		ActorID:              fixture.actorID,
		CanonicalRequestHash: hash[:],
	}
	tx := db.Begin()
	first, claimed, err := ClaimAdminBindingOperation(tx, operation)
	if err != nil || !claimed {
		tx.Rollback()
		t.Fatalf("first claim: claimed=%t err=%v", claimed, err)
	}
	response := json.RawMessage(fmt.Sprintf(`{"operation_id":"%s","measurement_point_id":"%s"}`, first.OperationID, fixture.pointID))
	if err := CommitAdminBindingOperation(tx, first.OperationID, response, time.Now().UTC()); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}

	replayed, claimed, err := ClaimAdminBindingOperation(db, operation)
	if err != nil || claimed {
		t.Fatalf("same scoped key did not replay: claimed=%t err=%v", claimed, err)
	}
	var expectedResponse, actualResponse interface{}
	if err := json.Unmarshal(response, &expectedResponse); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replayed.CommittedResponse, &actualResponse); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualResponse, expectedResponse) || replayed.OperationID != first.OperationID {
		t.Fatalf("replay payload mismatch: operation=%s response=%s", replayed.OperationID, replayed.CommittedResponse)
	}

	otherScope := *operation
	otherScope.ID = uuid.Nil
	otherScope.OperationID = uuid.New()
	otherScope.ScopeKey = fmt.Sprintf("shop:%d", fixture.shopID+1)
	other, claimed, err := ClaimAdminBindingOperation(db, &otherScope)
	if err != nil || !claimed || other.OperationID != otherScope.OperationID {
		t.Fatalf("same key in different scope was rejected: claimed=%t err=%v", claimed, err)
	}
	if err := db.Where("operation_id = ?", other.OperationID).Delete(&domain.AdminBindingOperation{}).Error; err != nil {
		t.Fatal(err)
	}

	changed := *operation
	changedHash := sha256.Sum256([]byte(`{"shop_id":1,"name":"different"}`))
	changed.CanonicalRequestHash = changedHash[:]
	if _, _, err := ClaimAdminBindingOperation(db, &changed); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("changed request returned %v, want ErrIdempotencyKeyReused", err)
	}
}

func TestIdempotencyLedgerConcurrentGuardAndRollback(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	hash := sha256.Sum256([]byte(`{"shop_id":1,"name":"concurrent"}`))
	base := domain.AdminBindingOperation{
		IdempotencyKey:       "concurrent-" + uuid.NewString(),
		Operation:            "bind",
		ScopeKey:             fmt.Sprintf("shop:%d", fixture.shopID),
		ActorID:              fixture.actorID,
		CanonicalRequestHash: hash[:],
	}
	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx := db.Begin()
			operation := base
			operation.ID = uuid.New()
			operation.OperationID = uuid.New()
			<-start
			existing, claimed, err := ClaimAdminBindingOperation(tx, &operation)
			if err == nil && claimed {
				err = CommitAdminBindingOperation(tx, existing.OperationID, json.RawMessage(`{"action":"bind"}`), time.Now().UTC())
			}
			if err != nil {
				tx.Rollback()
			} else {
				err = tx.Commit().Error
			}
			results <- claimed
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	claimedCount := 0
	for claimed := range results {
		if claimed {
			claimedCount++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if claimedCount != 1 {
		t.Fatalf("concurrent claims=%d, want exactly one", claimedCount)
	}

	rollbackKey := "rollback-" + uuid.NewString()
	rolledBack := base
	rolledBack.IdempotencyKey = rollbackKey
	rolledBack.ID = uuid.New()
	rolledBack.OperationID = uuid.New()
	tx := db.Begin()
	if _, claimed, err := ClaimAdminBindingOperation(tx, &rolledBack); err != nil || !claimed {
		tx.Rollback()
		t.Fatalf("rollback setup claim: claimed=%t err=%v", claimed, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	tx = db.Begin()
	if _, claimed, err := ClaimAdminBindingOperation(tx, &rolledBack); err != nil || !claimed {
		tx.Rollback()
		t.Fatalf("rolled-back key was not reclaimable: claimed=%t err=%v", claimed, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
}

func TestTime01PersistenceQueryUsesCurrentAssignmentInterval(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	oldStart := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	currentStart := oldStart.Add(time.Hour)
	oldEnd := currentStart
	insertAssignment(t, db, fixture.deviceID, fixture.pointID, oldStart, &oldEnd)
	currentAssignment := insertAssignment(t, db, fixture.deviceID, fixture.pointID, currentStart, nil)
	candidate := currentStart.Add(30 * time.Minute)

	conflict, err := HasCommittedTelemetryConflict(db, currentAssignment, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("empty current assignment reported a TIME-01 conflict")
	}

	otherAssignment := insertAssignment(t, db, fixture.otherDevice, fixture.otherPointID, currentStart, nil)
	insertReading(t, db, fixture.otherDevice, fixture.otherPointID, candidate)
	conflict, err = HasCommittedTelemetryConflict(db, currentAssignment, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("reading for another Device/MP produced a false TIME-01 conflict")
	}

	oldReading := oldStart.Add(15 * time.Minute)
	insertReading(t, db, fixture.deviceID, fixture.pointID, oldReading)
	conflict, err = HasCommittedTelemetryConflict(db, currentAssignment, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("reading before current assignment.valid_from produced a false conflict")
	}

	insertReading(t, db, fixture.deviceID, fixture.pointID, candidate)
	conflict, err = HasCommittedTelemetryConflict(db, currentAssignment, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !conflict {
		t.Fatal("committed current-assignment reading did not produce a TIME-01 conflict")
	}

	conflict, err = HasCommittedTelemetryConflict(db, otherAssignment, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !conflict {
		t.Fatal("the other assignment's committed reading was not detected")
	}
}
