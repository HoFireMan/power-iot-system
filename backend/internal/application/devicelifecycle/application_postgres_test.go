package devicelifecycle_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/application/devicelifecycle"
	"power-iot-backend/internal/core/domain"
	privatemigrations "power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/testsupport"
)

func newLifecycleDatabase(t *testing.T) *testsupport.Database {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	database, err := testsupport.New(context.Background(), os.Getenv("TEST_DATABASE_URL"), func(dsn string) error {
		if err := privatemigrations.Up(dsn); err != nil {
			return err
		}
		// The test-only admission helper is intentionally unavailable outside the
		// private migration package; use the public protected operator contract.
		if _, err := privatemigrations.RunD6ProtectedMigrationOperator(context.Background(), dsn, func(context.Context) error { return nil }); err != nil {
			return err
		}
		_, err := privatemigrations.RunB02ProtectedMigrationOperator(context.Background(), dsn, func(context.Context) error { return nil })
		return err
	})
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

func TestLifecycleTransitionIdempotencyTerminalStateAndAssignmentSafety(t *testing.T) {
	database := newLifecycleDatabase(t)
	db, err := gorm.Open(postgres.Open(database.DSN()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}

	client := domain.Client{Name: "Lifecycle Client", Code: "lifecycle-client"}
	shop := domain.Shop{ClientID: client.ID, Code: "lifecycle-shop", Name: "Lifecycle Shop", IsActive: true}
	user := domain.User{Account: "lifecycle-admin", PasswordHash: "test", Name: "Lifecycle Admin", IsAdmin: true, AuthEnabled: true}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	shop.ClientID = client.ID
	if err := db.Create(&shop).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.UserShopRelation{UserID: user.ID, ShopID: shop.ID}).Error; err != nil {
		t.Fatal(err)
	}
	owner := client.ID
	device := domain.Device{InventoryOwnerClientID: &owner, MacAddress: "AABBCCDDEEFF", Name: "Lifecycle Device"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	actor := domain.ActorContext{
		ActorID:  user.ID,
		ScopeKey: "admin-binding:client:" + formatUint(client.ID),
		Scope: domain.ScopeSnapshot{
			TenantKey: "client:" + formatUint(client.ID), ShopIDs: []uint{shop.ID}, DeviceIDs: []uint{device.ID},
			AllowedActions: []domain.BindingAction{domain.ActionDisableDevice, domain.ActionEnableDevice, domain.ActionRetireDevice},
		},
	}
	app := devicelifecycle.New(db)
	command := func(identity string, reason string) devicelifecycle.Command {
		return devicelifecycle.Command{DeviceID: device.ID, Reason: reason, RequestIdentity: identity, Actor: actor}
	}

	first, err := app.Disable(context.Background(), command("disable-1", "maintenance"))
	if err != nil || first.LifecycleStatus != domain.DeviceLifecycleDisabled {
		t.Fatalf("disable result=%+v err=%v", first, err)
	}
	replay, err := app.Disable(context.Background(), command("disable-1", "maintenance"))
	if err != nil || replay.OperationID != first.OperationID {
		t.Fatalf("idempotent replay=%+v err=%v", replay, err)
	}
	if _, err := app.Disable(context.Background(), command("disable-1", "different reason")); domain.CodeOf(err) != domain.ErrIdempotencyKeyReused {
		t.Fatalf("idempotency mismatch code=%s err=%v", domain.CodeOf(err), err)
	}
	if _, err := app.Enable(context.Background(), command("enable-1", "repair complete")); err != nil {
		t.Fatal(err)
	}

	point := domain.MeasurementPoint{ShopID: shop.ID, Name: "Lifecycle Point"}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: device.ID, MeasurementPointID: point.ID, ValidFrom: time.Now().UTC()}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.Retire(context.Background(), command("retire-assigned", "retirement")); domain.CodeOf(err) != domain.ErrDeviceAlreadyAssigned {
		t.Fatalf("assigned retire code=%s err=%v", domain.CodeOf(err), err)
	}
	if err := db.Model(&domain.DeviceAssignment{}).Where("id = ?", assignment.ID).Update("valid_to", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	retired, err := app.Retire(context.Background(), command("retire-1", "retirement"))
	if err != nil || retired.LifecycleStatus != domain.DeviceLifecycleRetired {
		t.Fatalf("retire result=%+v err=%v", retired, err)
	}
	if _, err := app.Enable(context.Background(), command("enable-2", "must fail")); domain.CodeOf(err) != domain.ErrDeviceRetired {
		t.Fatalf("terminal enable code=%s err=%v", domain.CodeOf(err), err)
	}
	var status string
	if err := db.Raw("SELECT lifecycle_status FROM devices WHERE id = ?", device.ID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != string(domain.DeviceLifecycleRetired) {
		t.Fatalf("status=%q", status)
	}
	var auditCount int64
	if err := db.Raw("SELECT count(*) FROM admin_binding_audits WHERE device_id = ?", device.ID).Scan(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 0 {
		t.Fatalf("lifecycle unexpectedly changed binding audit count=%d", auditCount)
	}
}

func formatUint(value uint) string {
	if value == 0 {
		return "0"
	}
	var buf [32]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
