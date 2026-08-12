package adminbinding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/core/iot"
	"power-iot-backend/internal/data/migrations"
)

type executorFixture struct {
	db      *gorm.DB
	actor   domain.ActorContext
	client  domain.Client
	user    domain.User
	shop    domain.Shop
	points  []domain.MeasurementPoint
	devices []domain.Device
}

func openExecutorDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; Admin Binding PostgreSQL integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func newExecutorFixture(t *testing.T, db *gorm.DB, pointCount, deviceCount int) executorFixture {
	t.Helper()
	suffix := uuid.NewString()[:8]
	fixture := executorFixture{db: db, client: domain.Client{Code: "exec-client-" + suffix, Name: "Executor Client"}, user: domain.User{Account: "exec-user-" + suffix, PasswordHash: "test-hash", Name: "Executor User"}, shop: domain.Shop{Code: "exec-shop-" + suffix, Name: "Executor Shop"}}
	if err := db.Create(&fixture.client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.user).Error; err != nil {
		t.Fatal(err)
	}
	fixture.shop.ClientID = fixture.client.ID
	if err := db.Create(&fixture.shop).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.UserShopRelation{UserID: fixture.user.ID, ShopID: fixture.shop.ID, ShopRole: "staff"}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pointCount; i++ {
		fixture.points = append(fixture.points, domain.MeasurementPoint{ID: uuid.New(), ShopID: fixture.shop.ID, Name: fmt.Sprintf("MP-%d", i)})
		if err := db.Create(&fixture.points[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < deviceCount; i++ {
		serial := fmt.Sprintf("SERIAL-%s-%d", suffix, i)
		mac := fmt.Sprintf("AA%010X", fixture.shop.ID*100+uint(i))
		fixture.devices = append(fixture.devices, domain.Device{ShopID: fixture.shop.ID, MacAddress: mac, SerialNumber: &serial, Name: fmt.Sprintf("Device-%d", i)})
		if err := db.Create(&fixture.devices[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	deviceIDs := make([]uint, 0, len(fixture.devices))
	for _, device := range fixture.devices {
		deviceIDs = append(deviceIDs, device.ID)
	}
	fixture.actor = domain.ActorContext{
		ActorID:  fixture.user.ID,
		ScopeKey: "executor-test:" + suffix,
		Scope: domain.ScopeSnapshot{
			TenantKey: "test-tenant",
			ShopIDs:   []uint{fixture.shop.ID}, DeviceIDs: deviceIDs,
			AllowedActions: []domain.BindingAction{domain.ActionCreateMeasurementPoint, domain.ActionBind, domain.ActionReplace, domain.ActionRelocate, domain.ActionUnbind},
		},
	}
	return fixture
}

func commandActor(f executorFixture) domain.ActorContext { return f.actor }
func idRef(id uint) domain.DeviceRef                     { return domain.DeviceRef{DeviceID: &id} }

func TestExecutorCreateMeasurementPointReplayAndRollback(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 1)
	e := NewExecutor(db)
	cmd := domain.CreateMeasurementPointCommand{ShopID: fixture.shop.ID, Name: "Created", RequestIdentity: "create-" + uuid.NewString(), Actor: commandActor(fixture)}
	first, err := e.CreateMeasurementPoint(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := e.CreateMeasurementPoint(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID != replay.OperationID || first.MeasurementPointID == nil || replay.MeasurementPointID == nil || *first.MeasurementPointID != *replay.MeasurementPointID {
		t.Fatalf("replay changed committed result: first=%+v replay=%+v", first, replay)
	}
	var points, audits, committed int64
	db.Model(&domain.MeasurementPoint{}).Where("id = ?", *first.MeasurementPointID).Count(&points)
	db.Model(&domain.AdminBindingAudit{}).Where("operation_id = ?", first.OperationID).Count(&audits)
	db.Model(&domain.AdminBindingOperation{}).Where("operation_id = ? AND committed_at IS NOT NULL", first.OperationID).Count(&committed)
	if points != 1 || audits != 1 || committed != 1 {
		t.Fatalf("create replay duplicated persistence: points=%d audits=%d committed=%d", points, audits, committed)
	}

	changed := cmd
	changed.Name = "Changed"
	if _, err := e.CreateMeasurementPoint(context.Background(), changed); domain.CodeOf(err) != domain.ErrIdempotencyKeyReused {
		t.Fatalf("changed same key code=%s err=%v", domain.CodeOf(err), err)
	}

	failureKey := "rollback-" + uuid.NewString()
	failing := NewExecutorWithHooks(db, ExecutionHooks{AfterMutation: func() error { return errors.New("deterministic failure after MP mutation") }})
	failedCmd := cmd
	failedCmd.RequestIdentity = failureKey
	failedCmd.Name = "Rolled Back"
	if _, err := failing.CreateMeasurementPoint(context.Background(), failedCmd); err == nil {
		t.Fatal("failure injection unexpectedly committed")
	}
	var rolledBack int64
	db.Model(&domain.MeasurementPoint{}).Where("shop_id = ? AND name = ?", fixture.shop.ID, failedCmd.Name).Count(&rolledBack)
	if rolledBack != 0 {
		t.Fatal("failed Create MP left a measurement point")
	}
	if _, err := e.CreateMeasurementPoint(context.Background(), failedCmd); err != nil {
		t.Fatalf("same idempotency key was not reclaimable: %v", err)
	}
}

func TestExecutorCanceledContextAfterAdmissionRollsBack(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewExecutorWithHooks(db, ExecutionHooks{AfterOperationClaim: func() error {
		cancel()
		return nil
	}})
	cmd := domain.CreateMeasurementPointCommand{ShopID: fixture.shop.ID, Name: "Canceled after admission", RequestIdentity: "canceled-" + uuid.NewString(), Actor: commandActor(fixture)}
	if _, err := e.CreateMeasurementPoint(ctx, cmd); err == nil {
		t.Fatal("canceled Admin transaction unexpectedly committed")
	}
	var count int64
	if err := db.Model(&domain.MeasurementPoint{}).Where("shop_id = ? AND name = ?", fixture.shop.ID, cmd.Name).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("canceled Admin transaction left a measurement point")
	}
}

func TestExecutorCallerOwnedTransactionDoesNotCommit(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 1)
	cmd := domain.CreateMeasurementPointCommand{ShopID: fixture.shop.ID, Name: "Caller Owned", RequestIdentity: "caller-owned-" + uuid.NewString(), Actor: commandActor(fixture)}
	tx := db.Begin()
	result, err := NewExecutor(db).CreateMeasurementPointInTransaction(context.Background(), tx, cmd)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if result.MeasurementPointID == nil {
		tx.Rollback()
		t.Fatal("missing created MP")
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&domain.MeasurementPoint{}).Where("id = ?", *result.MeasurementPointID).Count(&count)
	if count != 0 {
		t.Fatal("caller-owned transaction was committed by executor")
	}
	if _, err := NewExecutor(db).CreateMeasurementPoint(context.Background(), cmd); err != nil {
		t.Fatalf("rolled-back caller operation was not reclaimable: %v", err)
	}
}

func TestExecutorAllAssignmentTransitionsShareOneBoundaryAndReplay(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 2, 2)
	e := NewExecutor(db)
	bind, err := e.BindDevice(context.Background(), domain.BindDeviceCommand{DeviceRef: idRef(fixture.devices[0].ID), MeasurementPointID: fixture.points[0].ID, RequestIdentity: "bind-" + uuid.NewString(), Actor: commandActor(fixture)})
	if err != nil {
		t.Fatal(err)
	}
	if bind.EffectiveAt == nil || bind.NewAssignmentID == nil {
		t.Fatalf("incomplete bind result: %+v", bind)
	}
	var bindOperation domain.AdminBindingOperation
	var bindAudit domain.AdminBindingAudit
	if err := db.Where("operation_id = ?", bind.OperationID).First(&bindOperation).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("operation_id = ?", bind.OperationID).First(&bindAudit).Error; err != nil {
		t.Fatal(err)
	}
	if bindOperation.ClientID == nil || bindAudit.ClientID == nil || *bindOperation.ClientID != fixture.client.ID || *bindAudit.ClientID != *bindOperation.ClientID {
		t.Fatalf("bind provenance Client IDs operation=%v audit=%v want=%d", bindOperation.ClientID, bindAudit.ClientID, fixture.client.ID)
	}
	replaceCmd := domain.ReplaceDeviceCommand{CurrentAssignmentID: *bind.NewAssignmentID, ReplacementDeviceRef: idRef(fixture.devices[1].ID), RequestIdentity: "replace-" + uuid.NewString(), Actor: commandActor(fixture)}
	replace, err := e.ReplaceDevice(context.Background(), replaceCmd)
	if err != nil {
		t.Fatal(err)
	}
	if replace.EffectiveAt == nil || replace.OldAssignmentID == nil || replace.NewAssignmentID == nil || !replace.EffectiveAt.Equal(replace.EffectiveAt.UTC()) {
		t.Fatalf("incomplete replace result: %+v", replace)
	}
	if !replace.EffectiveAt.After(*bind.EffectiveAt) {
		t.Fatalf("replace T=%v did not advance bind T=%v", *replace.EffectiveAt, *bind.EffectiveAt)
	}

	relocateCmd := domain.RelocateDeviceCommand{CurrentAssignmentID: *replace.NewAssignmentID, TargetMeasurementPointID: fixture.points[1].ID, RequestIdentity: "relocate-" + uuid.NewString(), Actor: commandActor(fixture)}
	relocate, err := e.RelocateDevice(context.Background(), relocateCmd)
	if err != nil {
		t.Fatal(err)
	}
	unbindCmd := domain.UnbindDeviceCommand{CurrentAssignmentID: *relocate.NewAssignmentID, RequestIdentity: "unbind-" + uuid.NewString(), Actor: commandActor(fixture)}
	unbind, err := e.UnbindDevice(context.Background(), unbindCmd)
	if err != nil {
		t.Fatal(err)
	}
	if unbind.EffectiveAt == nil || !unbind.EffectiveAt.After(*relocate.EffectiveAt) {
		t.Fatalf("unbind boundary is not monotonic: %+v", unbind)
	}

	var history []domain.DeviceAssignment
	if err := db.Where("device_id = ?", fixture.devices[1].ID).Order("valid_from").Find(&history).Error; err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ValidTo == nil || !history[0].ValidTo.Equal(history[1].ValidFrom) || history[1].ValidTo == nil || !history[1].ValidTo.Equal(*unbind.EffectiveAt) {
		t.Fatalf("assignment history lost exact boundaries: %+v", history)
	}
	var oldHistory []domain.DeviceAssignment
	if err := db.Where("device_id = ?", fixture.devices[0].ID).Order("valid_from").Find(&oldHistory).Error; err != nil {
		t.Fatal(err)
	}
	var replaceAudit domain.AdminBindingAudit
	if err := db.Where("operation_id = ?", replace.OperationID).First(&replaceAudit).Error; err != nil {
		t.Fatal(err)
	}
	if replaceAudit.EffectiveAt == nil || len(oldHistory) != 1 || oldHistory[0].ValidTo == nil || !replaceAudit.EffectiveAt.Equal(*oldHistory[0].ValidTo) || !replaceAudit.EffectiveAt.Equal(history[0].ValidFrom) {
		t.Fatalf("audit effective_at did not equal exact transition T: audit=%v old_end=%v new_start=%v", replaceAudit.EffectiveAt, oldHistory[0].ValidTo, history[0].ValidFrom)
	}
	var audits int64
	db.Model(&domain.AdminBindingAudit{}).Where("operation_id IN (?, ?, ?)", replace.OperationID, relocate.OperationID, unbind.OperationID).Count(&audits)
	if audits != 3 {
		t.Fatalf("transition audit count=%d, want 3", audits)
	}
}

func TestExecutorCrossClientBindingTransitionsFailClosed(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 2, 3)
	otherClient := domain.Client{Code: "exec-cross-client-" + uuid.NewString()[:8], Name: "Other Executor Client"}
	if err := db.Create(&otherClient).Error; err != nil {
		t.Fatal(err)
	}
	otherShop := domain.Shop{ClientID: otherClient.ID, Code: "exec-cross-shop-" + uuid.NewString()[:8], Name: "Other Executor Shop"}
	if err := db.Create(&otherShop).Error; err != nil {
		t.Fatal(err)
	}
	otherPoint := domain.MeasurementPoint{ID: uuid.New(), ShopID: otherShop.ID, Name: "Other MP"}
	if err := db.Create(&otherPoint).Error; err != nil {
		t.Fatal(err)
	}
	actor := fixture.actor
	actor.Scope.ShopIDs = append(actor.Scope.ShopIDs, otherShop.ID)
	actor.Scope.DeviceIDs = append(actor.Scope.DeviceIDs, fixture.devices[2].ID)
	clientOne := fixture.client.ID
	clientTwo := otherClient.ID
	if err := db.Model(&domain.Device{}).Where("id = ?", fixture.devices[1].ID).Update("inventory_owner_client_id", clientOne).Error; err != nil {
		t.Fatal(err)
	}
	bindKey := "cross-client-bind-" + uuid.NewString()
	if _, err := NewExecutor(db).BindDevice(context.Background(), domain.BindDeviceCommand{DeviceRef: idRef(fixture.devices[1].ID), MeasurementPointID: otherPoint.ID, RequestIdentity: bindKey, Actor: actor}); domain.CodeOf(err) != domain.ErrTenantScopeDenied {
		t.Fatalf("cross-client bind code=%s err=%v", domain.CodeOf(err), err)
	}
	if err := db.Model(&domain.Device{}).Where("id = ?", fixture.devices[1].ID).Update("inventory_owner_client_id", clientTwo).Error; err != nil {
		t.Fatal(err)
	}
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: time.Now().UTC().Add(-time.Hour)}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	replaceKey := "cross-client-replace-" + uuid.NewString()
	if _, err := NewExecutor(db).ReplaceDevice(context.Background(), domain.ReplaceDeviceCommand{CurrentAssignmentID: assignment.ID, ReplacementDeviceRef: idRef(fixture.devices[1].ID), RequestIdentity: replaceKey, Actor: actor}); domain.CodeOf(err) != domain.ErrTenantScopeDenied {
		t.Fatalf("cross-client replace code=%s err=%v", domain.CodeOf(err), err)
	}
	if err := db.Model(&domain.Device{}).Where("id = ?", fixture.devices[0].ID).Update("inventory_owner_client_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	relocateKey := "cross-client-relocate-" + uuid.NewString()
	if _, err := NewExecutor(db).RelocateDevice(context.Background(), domain.RelocateDeviceCommand{CurrentAssignmentID: assignment.ID, TargetMeasurementPointID: otherPoint.ID, RequestIdentity: relocateKey, Actor: actor}); domain.CodeOf(err) != domain.ErrTenantScopeDenied {
		t.Fatalf("cross-client relocate code=%s err=%v", domain.CodeOf(err), err)
	}
	var operations, audits int64
	if err := db.Model(&domain.AdminBindingOperation{}).Where("idempotency_key IN ?", []string{bindKey, replaceKey, relocateKey}).Count(&operations).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.AdminBindingAudit{}).Where("request_identity IN ?", []string{bindKey, replaceKey, relocateKey}).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if operations != 0 || audits != 0 {
		t.Fatalf("cross-client rejection left provenance rows: operations=%d audits=%d", operations, audits)
	}
}

func TestBindSkipsTIME01ForNewAssignment(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 1)
	future := time.Now().UTC().Add(time.Hour)
	if err := db.Exec(`INSERT INTO power_readings (time, recorded_at, received_at, measurement_point_id, device_id, voltage, current, active_power) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, future, future, time.Now().UTC(), fixture.points[0].ID, fixture.devices[0].ID, 110, 1, 110).Error; err != nil {
		t.Fatal(err)
	}
	result, err := NewExecutor(db).BindDevice(context.Background(), domain.BindDeviceCommand{DeviceRef: idRef(fixture.devices[0].ID), MeasurementPointID: fixture.points[0].ID, RequestIdentity: "bind-no-time01-" + uuid.NewString(), Actor: commandActor(fixture)})
	if err != nil || result.NewAssignmentID == nil {
		t.Fatalf("Bind incorrectly applied TIME-01: result=%+v err=%v", result, err)
	}
}

func TestExecutorTransitionFailureRollsBackCloseAndOpenTogether(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 2)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: time.Now().UTC().Add(-time.Hour)}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	cmd := domain.ReplaceDeviceCommand{CurrentAssignmentID: assignment.ID, ReplacementDeviceRef: idRef(fixture.devices[1].ID), RequestIdentity: "replace-rollback-" + uuid.NewString(), Actor: commandActor(fixture)}
	failing := NewExecutorWithHooks(db, ExecutionHooks{AfterMutation: func() error { return errors.New("forced transition rollback") }})
	if _, err := failing.ReplaceDevice(context.Background(), cmd); err == nil {
		t.Fatal("failure injection unexpectedly committed replacement")
	}
	var old domain.DeviceAssignment
	if err := db.First(&old, assignment.ID).Error; err != nil {
		t.Fatal(err)
	}
	if old.ValidTo != nil {
		t.Fatal("failed replacement closed the old assignment")
	}
	var replacementCount int64
	db.Model(&domain.DeviceAssignment{}).Where("device_id = ? AND valid_to IS NULL", fixture.devices[1].ID).Count(&replacementCount)
	if replacementCount != 0 {
		t.Fatal("failed replacement left a new active assignment")
	}
	var failedOperations, failedAudits int64
	if err := db.Model(&domain.AdminBindingOperation{}).Where("idempotency_key = ?", cmd.RequestIdentity).Count(&failedOperations).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.AdminBindingAudit{}).Where("request_identity = ?", cmd.RequestIdentity).Count(&failedAudits).Error; err != nil {
		t.Fatal(err)
	}
	if failedOperations != 0 || failedAudits != 0 {
		t.Fatalf("failed replacement left provenance rows: operations=%d audits=%d", failedOperations, failedAudits)
	}
	if _, err := NewExecutor(db).ReplaceDevice(context.Background(), cmd); err != nil {
		t.Fatalf("replacement key was not reclaimable: %v", err)
	}
}

func TestExecutorRetriesTransientFailureWithBoundedAttempts(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 1)
	attempts := 0
	e := NewExecutorWithHooks(db, ExecutionHooks{AfterOperationClaim: func() error { attempts++; return &pgconn.PgError{Code: "40001"} }})
	cmd := domain.CreateMeasurementPointCommand{ShopID: fixture.shop.ID, Name: "Retry Exhausted", RequestIdentity: "retry-exhausted-" + uuid.NewString(), Actor: commandActor(fixture)}
	if _, err := e.CreateMeasurementPoint(context.Background(), cmd); domain.CodeOf(err) != domain.ErrConcurrentTransition {
		t.Fatalf("retry exhaustion code=%s err=%v", domain.CodeOf(err), err)
	}
	if attempts != 3 {
		t.Fatalf("transient retry attempts=%d, want 3", attempts)
	}
	var points, operations int64
	db.Model(&domain.MeasurementPoint{}).Where("shop_id = ? AND name = ?", fixture.shop.ID, cmd.Name).Count(&points)
	db.Model(&domain.AdminBindingOperation{}).Where("idempotency_key = ?", cmd.RequestIdentity).Count(&operations)
	if points != 0 || operations != 0 {
		t.Fatalf("retry exhaustion left rows: points=%d operations=%d", points, operations)
	}
}

func TestExecutorTIME01RollsBackAndLeavesCurrentAssignment(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 1)
	start := time.Now().UTC().Add(-time.Hour)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: start}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(time.Hour)
	if err := db.Exec(`INSERT INTO power_readings (time, recorded_at, received_at, measurement_point_id, device_id, voltage, current, active_power) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, future, future, time.Now().UTC(), fixture.points[0].ID, fixture.devices[0].ID, 110, 1, 110).Error; err != nil {
		t.Fatal(err)
	}
	cmd := domain.UnbindDeviceCommand{CurrentAssignmentID: assignment.ID, RequestIdentity: "time-conflict-" + uuid.NewString(), Actor: commandActor(fixture)}
	if _, err := NewExecutor(db).UnbindDevice(context.Background(), cmd); domain.CodeOf(err) != domain.ErrAssignmentTimeConflict {
		t.Fatalf("TIME-01 code=%s err=%v", domain.CodeOf(err), err)
	}
	var current domain.DeviceAssignment
	if err := db.First(&current, assignment.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.ValidTo != nil {
		t.Fatal("TIME-01 closed the current assignment")
	}
	var audits, operations int64
	db.Model(&domain.AdminBindingAudit{}).Where("request_identity = ?", cmd.RequestIdentity).Count(&audits)
	db.Model(&domain.AdminBindingOperation{}).Where("idempotency_key = ? AND committed_at IS NOT NULL", cmd.RequestIdentity).Count(&operations)
	if audits != 0 || operations != 0 {
		t.Fatalf("TIME-01 left success records: audits=%d operations=%d", audits, operations)
	}
}

func TestExecutorTIME01AppliesToReplaceAndRelocate(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 3, 3)
	start := time.Now().UTC().Add(-time.Hour)
	replaceAssignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: start}
	relocateAssignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[2].ID, MeasurementPointID: fixture.points[1].ID, ValidFrom: start}
	if err := db.Create(&replaceAssignment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&relocateAssignment).Error; err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(time.Hour)
	for _, device := range []domain.Device{fixture.devices[0], fixture.devices[2]} {
		point := fixture.points[0].ID
		if device.ID == fixture.devices[2].ID {
			point = fixture.points[1].ID
		}
		if err := db.Exec(`INSERT INTO power_readings (time, recorded_at, received_at, measurement_point_id, device_id, voltage, current, active_power) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, future, future, time.Now().UTC(), point, device.ID, 110, 1, 110).Error; err != nil {
			t.Fatal(err)
		}
	}
	replace := domain.ReplaceDeviceCommand{CurrentAssignmentID: replaceAssignment.ID, ReplacementDeviceRef: idRef(fixture.devices[1].ID), RequestIdentity: "time-replace-" + uuid.NewString(), Actor: commandActor(fixture)}
	if _, err := NewExecutor(db).ReplaceDevice(context.Background(), replace); domain.CodeOf(err) != domain.ErrAssignmentTimeConflict {
		t.Fatalf("Replace TIME-01 code=%s err=%v", domain.CodeOf(err), err)
	}
	// Use a separate current assignment for Relocate because the first conflict
	// intentionally leaves its old interval active.
	relocate := domain.RelocateDeviceCommand{CurrentAssignmentID: relocateAssignment.ID, TargetMeasurementPointID: fixture.points[2].ID, RequestIdentity: "time-relocate-" + uuid.NewString(), Actor: commandActor(fixture)}
	if _, err := NewExecutor(db).RelocateDevice(context.Background(), relocate); domain.CodeOf(err) != domain.ErrAssignmentTimeConflict {
		t.Fatalf("Relocate TIME-01 code=%s err=%v", domain.CodeOf(err), err)
	}
}

func TestExecutorConcurrentBindsHaveOneWinnerAndSameKeyReplays(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 2, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan struct {
		result domain.AdminBindingResult
		err    error
	}, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			results <- func() (value struct {
				result domain.AdminBindingResult
				err    error
			}) {
				value.result, value.err = NewExecutor(db).BindDevice(ctx, domain.BindDeviceCommand{DeviceRef: idRef(fixture.devices[i].ID), MeasurementPointID: fixture.points[0].ID, RequestIdentity: fmt.Sprintf("same-mp-%d-%s", i, uuid.NewString()), Actor: commandActor(fixture)})
				return value
			}()
		}()
	}
	wins := make([]domain.AdminBindingResult, 0, 2)
	for i := 0; i < 2; i++ {
		value := <-results
		if value.err == nil {
			wins = append(wins, value.result)
		}
	}
	if len(wins) != 1 {
		t.Fatalf("same MP concurrent binds winners=%d, want one", len(wins))
	}

	key := "same-key-" + uuid.NewString()
	cmd := domain.BindDeviceCommand{DeviceRef: idRef(fixture.devices[2].ID), MeasurementPointID: fixture.points[1].ID, RequestIdentity: key, Actor: commandActor(fixture)}
	results = make(chan struct {
		result domain.AdminBindingResult
		err    error
	}, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := NewExecutor(db).BindDevice(ctx, cmd)
			results <- struct {
				result domain.AdminBindingResult
				err    error
			}{result, err}
		}()
	}
	wg.Wait()
	close(results)
	var first domain.AdminBindingResult
	count := 0
	for value := range results {
		if value.err != nil {
			t.Fatal(value.err)
		}
		count++
		if first.OperationID == uuid.Nil {
			first = value.result
		} else if value.result.OperationID != first.OperationID || *value.result.NewAssignmentID != *first.NewAssignmentID || !value.result.EffectiveAt.Equal(*first.EffectiveAt) {
			t.Fatalf("same-key replay differed: first=%+v second=%+v", first, value.result)
		}
	}
	if count != 2 {
		t.Fatalf("same-key callers=%d", count)
	}
}

func TestAdminTelemetrySharedDeviceSerializationAndBoundary(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 3, 2)
	start := time.Now().UTC().Add(-time.Hour)
	firstAssignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: start}
	secondAssignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[1].ID, MeasurementPointID: fixture.points[1].ID, ValidFrom: start}
	if err := db.Create(&firstAssignment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&secondAssignment).Error; err != nil {
		t.Fatal(err)
	}

	// TelemetryIngestor obtains the Device row first and commits a future
	// recorded_at through the current assignment. Admin must reject closing it.
	future := time.Now().UTC().Add(time.Hour)
	telemetry := iot.NewTelemetryIngestor(db)
	payload := iot.MqttPayload{MacAddress: fixture.devices[0].MacAddress, Timestamp: future.Unix(), ProtocolVersion: 1, BootID: "test", BootCounter: 1, Sequence: 1, Voltage: 110, Current: 1, Power: 110, KwhTotal: 1}
	stored, err := telemetry.Ingest(payload, time.Now().UTC())
	if err != nil || stored.Status != iot.IngestStored {
		t.Fatalf("telemetry-first ingest=%+v err=%v", stored, err)
	}
	conflictCmd := domain.UnbindDeviceCommand{CurrentAssignmentID: firstAssignment.ID, RequestIdentity: "telemetry-first-" + uuid.NewString(), Actor: commandActor(fixture)}
	if _, err := NewExecutor(db).UnbindDevice(context.Background(), conflictCmd); domain.CodeOf(err) != domain.ErrAssignmentTimeConflict {
		t.Fatalf("telemetry-first TIME-01 code=%s err=%v", domain.CodeOf(err), err)
	}

	// Admin wins the Device lock and commits a relocation. A delayed telemetry
	// message is resolved by recorded_at, not by its later receive time.
	relocateCmd := domain.RelocateDeviceCommand{CurrentAssignmentID: secondAssignment.ID, TargetMeasurementPointID: fixture.points[2].ID, RequestIdentity: "admin-first-" + uuid.NewString(), Actor: commandActor(fixture)}
	relocated, err := NewExecutor(db).RelocateDevice(context.Background(), relocateCmd)
	if err != nil {
		t.Fatal(err)
	}
	delayed := iot.MqttPayload{MacAddress: fixture.devices[1].MacAddress, Timestamp: relocated.EffectiveAt.Add(-time.Minute).Unix(), ProtocolVersion: 1, BootID: "test", BootCounter: 2, Sequence: 1, Voltage: 110, Current: 1, Power: 110, KwhTotal: 2}
	stored, err = telemetry.Ingest(delayed, time.Now().UTC().Add(time.Minute))
	if err != nil || stored.Status != iot.IngestStored {
		t.Fatalf("delayed telemetry=%+v err=%v", stored, err)
	}
	var reading domain.PowerReading
	if err := db.Where("device_id = ? AND sequence = ?", fixture.devices[1].ID, 1).First(&reading).Error; err != nil {
		t.Fatal(err)
	}
	if reading.MeasurementPointID == nil || *reading.MeasurementPointID != fixture.points[1].ID {
		t.Fatalf("delayed telemetry crossed Admin boundary: %+v", reading.MeasurementPointID)
	}
}

func TestExecutorMutationBlocksAtSharedFenceBeforeDomainRowLock(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; Admin Binding PostgreSQL integration test not run")
	}
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 1)
	fence, err := migrations.OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	reachedDomainLock := make(chan struct{})
	done := make(chan error, 1)
	executor := NewExecutorWithHooks(db, ExecutionHooks{BeforeDeviceLock: func(*gorm.DB) error {
		close(reachedDomainLock)
		return nil
	}})
	go func() {
		_, err := executor.BindDevice(context.Background(), domain.BindDeviceCommand{
			DeviceRef: idRef(fixture.devices[0].ID), MeasurementPointID: fixture.points[0].ID,
			RequestIdentity: "fence-block-" + uuid.NewString(), Actor: commandActor(fixture),
		})
		done <- err
	}()
	select {
	case <-reachedDomainLock:
		t.Fatal("Admin reached domain planning/row-lock hook while exclusive fence was held")
	case <-time.After(150 * time.Millisecond):
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Admin mutation did not proceed after exclusive release")
	}
	select {
	case <-reachedDomainLock:
	default:
		t.Fatal("Admin mutation did not reach the existing domain lock seam after admission")
	}
}

func TestExecutorDeviceLockIsPostgreSQLFORUPDATE(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 1)
	tx := db.Begin()
	if err := persistence.LockDevicesForUpdate(tx, []uint{fixture.devices[0].ID}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	var locked domain.Device
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, fixture.devices[0].ID).Error; err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
}
