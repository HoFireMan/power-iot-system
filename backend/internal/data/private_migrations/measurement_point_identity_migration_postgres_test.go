package migrations

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
)

func openIdentityMigrationDB(t *testing.T, databaseURL string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestRunMeasurementPointIdentityMigrationRequiresProtectedAdmission(t *testing.T) {
	err := RunMeasurementPointIdentityMigration(context.Background(), "unused", ExternalWriterAdmission{})
	if !errors.Is(err, ErrExternalWriterAdmissionRequired) {
		t.Fatalf("error=%v, want protected admission failure", err)
	}
}

func TestMeasurementPointIdentityMigrationBackfillsExactAlertsAndQuarantinesDailyUsage(t *testing.T) {
	database := newB02Database(t)
	migrateB02ForTest(t, database.DSN())
	db := openIdentityMigrationDB(t, database.DSN())
	suffix := strings.ToUpper(uuid.NewString()[:8])
	client := domain.Client{Code: "ident-client-" + suffix, Name: "IDENT Client"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	shop := domain.Shop{ClientID: client.ID, Code: "ident-shop-" + suffix, Name: "IDENT Shop"}
	if err := db.Create(&shop).Error; err != nil {
		t.Fatal(err)
	}
	point := domain.MeasurementPoint{ID: uuid.New(), ShopID: shop.ID, Name: "IDENT MP"}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	ownerClientID := client.ID
	device := domain.Device{ShopID: shop.ID, InventoryOwnerClientID: &ownerClientID, MacAddress: "AABBCC" + suffix[:6], Name: "ident-device"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	unassigned := domain.Device{ShopID: shop.ID, InventoryOwnerClientID: &ownerClientID, MacAddress: "DDEEFF" + suffix[:6], Name: "ident-unassigned"}
	if err := db.Create(&unassigned).Error; err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	to := from.Add(2 * time.Hour)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: device.ID, MeasurementPointID: point.ID, ValidFrom: from, ValidTo: &to}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	exactAlert := domain.AlertLog{
		DeviceID: device.ID, Type: "CURFEW_USAGE", CreatedAt: from.Add(time.Hour),
		LegacyUnresolved: true, Message: "legacy exact",
	}
	if err := db.Create(&exactAlert).Error; err != nil {
		t.Fatal(err)
	}
	unattributableAlert := domain.AlertLog{
		DeviceID: unassigned.ID, Type: "CURFEW_USAGE", CreatedAt: from.Add(time.Hour),
		LegacyUnresolved: true, Message: "legacy unknown",
	}
	if err := db.Create(&unattributableAlert).Error; err != nil {
		t.Fatal(err)
	}
	deviceID := device.ID
	legacyUsage := domain.DailyUsage{
		Date: "2026-08-08", DeviceID: &deviceID, KwhUsage: 12.5,
		LegacyUnresolved: true,
	}
	if err := db.Create(&legacyUsage).Error; err != nil {
		t.Fatal(err)
	}

	admission := b02TestAdmission()
	if err := RunMeasurementPointIdentityMigration(context.Background(), database.DSN(), admission); err != nil {
		t.Fatal(err)
	}
	if err := RunMeasurementPointIdentityMigration(context.Background(), database.DSN(), admission); err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	var gotAlert domain.AlertLog
	if err := db.First(&gotAlert, exactAlert.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotAlert.MeasurementPointID == nil || *gotAlert.MeasurementPointID != point.ID || gotAlert.LegacyUnresolved {
		t.Fatalf("exact alert identity=%v unresolved=%t", gotAlert.MeasurementPointID, gotAlert.LegacyUnresolved)
	}
	var gotUnattributed domain.AlertLog
	if err := db.First(&gotUnattributed, unattributableAlert.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotUnattributed.MeasurementPointID != nil || !gotUnattributed.LegacyUnresolved {
		t.Fatalf("unattributed alert was fabricated: MP=%v unresolved=%t", gotUnattributed.MeasurementPointID, gotUnattributed.LegacyUnresolved)
	}
	var gotUsage domain.DailyUsage
	if err := db.First(&gotUsage, legacyUsage.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotUsage.MeasurementPointID != nil || !gotUsage.LegacyUnresolved || gotUsage.DeviceID == nil || *gotUsage.DeviceID != device.ID || gotUsage.KwhUsage != 12.5 {
		t.Fatalf("legacy usage changed: %+v", gotUsage)
	}

	authoritative := domain.DailyUsage{Date: "2026-08-08", MeasurementPointID: &point.ID, LegacyUnresolved: false, KwhUsage: 1}
	if err := db.Create(&authoritative).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := domain.DailyUsage{Date: authoritative.Date, MeasurementPointID: &point.ID, LegacyUnresolved: false, KwhUsage: 2}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate MP-day authoritative row was accepted")
	}
	legacyDuplicate := domain.DailyUsage{Date: authoritative.Date, DeviceID: &deviceID, LegacyUnresolved: true, KwhUsage: 3}
	if err := db.Create(&legacyDuplicate).Error; err != nil {
		t.Fatalf("unresolved legacy duplicate rejected: %v", err)
	}

	beforeCount := int64(0)
	if err := db.Model(&domain.AlertLog{}).Count(&beforeCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(identityMigrationSQL(t, "000010_measurement_point_identity.down.sql")).Error; err == nil {
		t.Fatal("guarded identity DOWN unexpectedly succeeded")
	}
	var afterCount int64
	if err := db.Model(&domain.AlertLog{}).Count(&afterCount).Error; err != nil {
		t.Fatal(err)
	}
	if afterCount != beforeCount {
		t.Fatalf("guarded DOWN changed alert count before=%d after=%d", beforeCount, afterCount)
	}
}

func identityMigrationSQL(t *testing.T, name string) string {
	t.Helper()
	body, err := fs.ReadFile(Files, "sql/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestPreIdentityB02RequiresRepairBeforeRuntimeAdmission(t *testing.T) {
	database := newB02Database(t)
	migrateB02ForTest(t, database.DSN())
	db := openIdentityMigrationDB(t, database.DSN())
	for _, statement := range []string{
		"DROP INDEX IF EXISTS daily_usages_measurement_point_date_key",
		"DROP INDEX IF EXISTS idx_daily_usages_measurement_point_date",
		"ALTER TABLE daily_usages DROP CONSTRAINT IF EXISTS daily_usages_identity_state_check",
		"ALTER TABLE daily_usages DROP CONSTRAINT IF EXISTS daily_usages_measurement_point_fk",
		"ALTER TABLE daily_usages ALTER COLUMN device_id SET NOT NULL",
		"ALTER TABLE daily_usages DROP COLUMN IF EXISTS legacy_unresolved",
		"ALTER TABLE daily_usages DROP COLUMN IF EXISTS measurement_point_id",
		"ALTER TABLE alert_logs DROP CONSTRAINT IF EXISTS alert_logs_identity_state_check",
		"ALTER TABLE alert_logs DROP CONSTRAINT IF EXISTS alert_logs_measurement_point_fk",
		"ALTER TABLE alert_logs DROP COLUMN IF EXISTS legacy_unresolved",
		"ALTER TABLE alert_logs DROP COLUMN IF EXISTS measurement_point_id",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("remove IDENT-002 artifact %q: %v", statement, err)
		}
	}
	if _, err := BootstrapAndAdmit(context.Background(), database.DSN()); err == nil {
		t.Fatal("pre-IDENT-002 B-02 database was admitted")
	}
	if _, err := RunB02Migration(context.Background(), database.DSN(), b02TestAdmission()); err != nil {
		t.Fatalf("protected repair failed: %v", err)
	}
	inspection, err := InspectProtectedMigration(context.Background(), database.DSN(), D5MigrationSpec(b02TestAdmission()))
	if err != nil {
		t.Fatal(err)
	}
	if inspection.State != ProtectedStateCleanB02 {
		t.Fatalf("post-repair state=%s catalog=%s", inspection.State, inspection.Catalog)
	}
}

func TestMeasurementPointIdentityMigrationAmbiguousAssignmentIsNotFabricated(t *testing.T) {
	// Current admitted schema prevents overlapping assignments with an
	// exclusion constraint. Keep this proof explicit so a future schema change
	// cannot silently turn ambiguous historical evidence into a chosen MP.
	database := newB02Database(t)
	migrateB02ForTest(t, database.DSN())
	db := openIdentityMigrationDB(t, database.DSN())
	var constraint int
	if err := db.Raw(`SELECT count(*) FROM pg_constraint WHERE conname = 'device_assignments_device_no_overlap'`).Scan(&constraint).Error; err != nil {
		t.Fatal(err)
	}
	if constraint != 1 {
		t.Fatalf("device assignment overlap protection count=%d", constraint)
	}
	var tableName string
	if err := db.Raw(`SELECT to_regclass('daily_usages')`).Scan(&tableName).Error; err != nil {
		t.Fatal(err)
	}
	if tableName != "daily_usages" {
		t.Fatalf("daily_usages table=%q", tableName)
	}
}
