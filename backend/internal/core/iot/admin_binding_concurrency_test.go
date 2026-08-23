package iot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"power-iot-backend/internal/application/adminbinding"
	"power-iot-backend/internal/core/domain"
)

func TestAdminFirstDeviceLockBlocksTelemetryUntilCommit(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	start := time.Now().UTC().Add(-time.Hour)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.first.ID, MeasurementPointID: fixture.point.ID, ValidFrom: start}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	user := domain.User{Account: "admin-first-" + uuid.NewString()[:8], PasswordHash: "test-hash", Name: "Admin First"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.UserShopRelation{UserID: user.ID, ShopID: fixture.shop.ID, ShopRole: "staff"}).Error; err != nil {
		t.Fatal(err)
	}
	actor := domain.ActorContext{ActorID: user.ID, ScopeKey: "admin-first:" + uuid.NewString(), Scope: domain.ScopeSnapshot{TenantKey: "test-tenant", ShopIDs: []uint{fixture.shop.ID}, DeviceIDs: []uint{fixture.first.ID}, AllowedActions: []domain.BindingAction{domain.ActionRelocate}}}

	adminLocked := make(chan struct{})
	releaseAdmin := make(chan struct{})
	adminDone := make(chan error, 1)
	executor := adminbinding.NewExecutorWithHooks(db, adminbinding.ExecutionHooks{AfterLocks: func() error { close(adminLocked); <-releaseAdmin; return nil }})
	go func() {
		_, err := executor.RelocateDevice(context.Background(), domain.RelocateDeviceCommand{CurrentAssignmentID: assignment.ID, TargetMeasurementPointID: fixture.other.ID, RequestIdentity: "admin-first-relocate-" + uuid.NewString(), Actor: actor})
		adminDone <- err
	}()
	select {
	case <-adminLocked:
	case <-time.After(10 * time.Second):
		t.Fatal("Admin did not acquire the Device lock")
	}

	telemetryAttempt := make(chan int)
	allowTelemetryAttempt := make(chan struct{})
	telemetryDeviceLocked := make(chan struct{})
	ingestor := NewTelemetryIngestor(db)
	ingestor.beforeDeviceLock = func(tx *gorm.DB) error {
		var pid int
		if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			return err
		}
		telemetryAttempt <- pid
		<-allowTelemetryAttempt
		return nil
	}
	ingestor.afterDeviceLock = func() error { close(telemetryDeviceLocked); return nil }
	telemetryDone := make(chan error, 1)
	energyDelta := 0.001
	go func() {
		result, err := ingestor.Ingest(MqttPayload{MacAddress: fixture.first.MacAddress, Timestamp: start.Add(10 * time.Minute).Unix(), ProtocolVersion: 1, BootID: "admin-first", BootCounter: 11, Sequence: 1, Voltage: 110, Current: 1, Power: 110, KwhTotal: 1, EnergyDeltaKwh: &energyDelta}, time.Now().UTC().Add(time.Minute))
		if err != nil || result.Status != IngestStored {
			telemetryDone <- fmt.Errorf("telemetry result=%+v err=%v", result, err)
			return
		}
		telemetryDone <- nil
	}()
	var telemetryPID int
	select {
	case telemetryPID = <-telemetryAttempt:
	case <-time.After(10 * time.Second):
		t.Fatal("Telemetry did not reach the shared Device lock")
	}
	close(allowTelemetryAttempt)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waitEvent *string
		if err := db.Raw("SELECT wait_event_type FROM pg_stat_activity WHERE pid = ?", telemetryPID).Scan(&waitEvent).Error; err != nil {
			t.Fatal(err)
		}
		if waitEvent != nil && *waitEvent == "Lock" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Telemetry FOR UPDATE request was not observed waiting on Admin's Device lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-telemetryDeviceLocked:
		t.Fatal("Telemetry reached assignment work before Admin committed its Device lock holder")
	default:
	}
	close(releaseAdmin)

	select {
	case err := <-adminDone:
		if err != nil {
			t.Fatalf("Admin transition failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Admin transition did not commit")
	}
	select {
	case err := <-telemetryDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Telemetry did not commit after Admin released the Device lock")
	}
	var reading domain.PowerReading
	if err := db.Where("device_id = ? AND sequence = ?", fixture.first.ID, 1).First(&reading).Error; err != nil {
		t.Fatal(err)
	}
	if reading.MeasurementPointID == nil || *reading.MeasurementPointID != fixture.point.ID {
		t.Fatalf("delayed telemetry crossed committed Admin boundary: %v", reading.MeasurementPointID)
	}
}

func TestTelemetryFirstDeviceLockSerializesAdminTransition(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	start := time.Now().UTC().Add(-time.Hour)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.first.ID, MeasurementPointID: fixture.point.ID, ValidFrom: start}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	user := domain.User{Account: "telemetry-admin-" + uuid.NewString()[:8], PasswordHash: "test-hash", Name: "Telemetry Admin"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.UserShopRelation{UserID: user.ID, ShopID: fixture.shop.ID, ShopRole: "staff"}).Error; err != nil {
		t.Fatal(err)
	}
	actor := domain.ActorContext{
		ActorID:  user.ID,
		ScopeKey: "telemetry-admin:" + uuid.NewString(),
		Scope: domain.ScopeSnapshot{
			TenantKey: "test-tenant", ShopIDs: []uint{fixture.shop.ID}, DeviceIDs: []uint{fixture.first.ID},
			AllowedActions: []domain.BindingAction{domain.ActionUnbind},
		},
	}

	deviceLocked := make(chan struct{})
	releaseTelemetry := make(chan struct{})
	ingestor := NewTelemetryIngestor(db)
	ingestor.afterDeviceLock = func() error {
		close(deviceLocked)
		<-releaseTelemetry
		return nil
	}
	telemetryDone := make(chan error, 1)
	future := time.Now().UTC().Add(time.Hour)
	energyDelta := 0.001
	go func() {
		result, err := ingestor.Ingest(MqttPayload{MacAddress: fixture.first.MacAddress, Timestamp: future.Unix(), ProtocolVersion: 1, BootID: "lock-test", BootCounter: 9, Sequence: 1, Voltage: 110, Current: 1, Power: 110, KwhTotal: 1, EnergyDeltaKwh: &energyDelta}, time.Now().UTC())
		if err != nil || result.Status != IngestStored {
			telemetryDone <- fmt.Errorf("telemetry result=%+v err=%v", result, err)
			return
		}
		telemetryDone <- nil
	}()

	select {
	case <-deviceLocked:
	case <-time.After(10 * time.Second):
		t.Fatal("TelemetryIngestor did not acquire the Device lock")
	}

	adminDone := make(chan error, 1)
	go func() {
		_, err := adminbinding.NewExecutor(db).UnbindDevice(context.Background(), domain.UnbindDeviceCommand{CurrentAssignmentID: assignment.ID, RequestIdentity: "telemetry-first-admin-" + uuid.NewString(), Actor: actor})
		adminDone <- err
	}()
	close(releaseTelemetry)

	select {
	case err := <-telemetryDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TelemetryIngestor did not commit after releasing the Device lock")
	}
	select {
	case err := <-adminDone:
		if domain.CodeOf(err) != domain.ErrAssignmentTimeConflict {
			t.Fatalf("Admin transition code=%s err=%v, want assignment_time_conflict", domain.CodeOf(err), err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Admin transition did not resolve after telemetry commit")
	}
}
