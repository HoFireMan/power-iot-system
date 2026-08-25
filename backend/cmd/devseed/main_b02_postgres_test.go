package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/testsupport"
)

func TestDevseedSeedsCleanB02Database(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; B-02 devseed PostgreSQL integration test not run")
	}
	database, err := testsupport.New(context.Background(), source, migrations.Up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})

	ctx := context.Background()
	if report, err := migrations.RunD6ProtectedMigrationOperator(ctx, database.DSN(), func(context.Context) error { return nil }); err != nil || report.PostCommitState != migrations.ProtectedStateCleanV6 {
		t.Fatalf("D6 report=%+v err=%v", report, err)
	}
	if report, err := migrations.RunB02ProtectedMigrationOperator(ctx, database.DSN(), func(context.Context) error { return nil }); err != nil || report.PostCommitState != migrations.ProtectedStateCleanB02 {
		t.Fatalf("B-02 report=%+v err=%v", report, err)
	}
	admission, err := migrations.BootstrapAndAdmit(ctx, database.DSN())
	if err != nil || admission.Disposition != migrations.RuntimeServeB02 || !devseedAdmissionAccepted(admission.Disposition) {
		t.Fatalf("B-02 admission=%+v err=%v", admission, err)
	}
	const mac = "AABBCCDDEEFF"
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(commandCtx, "go", "run", ".", "--device-mac", mac, "--measurement-point-name", "UI Test Meter")
	command.Env = filteredDevseedTestEnvironment(database.DSN(), "b02-integration-development-password")
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatalf("canonical devseed command failed: %v", err)
	}

	db, err := openDevseedDatabase(database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	var coverageConfigCount int64
	if err := db.Model(&domain.SystemConfig{}).Where("key = ?", coverageMaxIntervalConfigKey).Count(&coverageConfigCount).Error; err != nil {
		t.Fatal(err)
	}
	if coverageConfigCount != 0 {
		t.Fatalf("omitted coverage configuration created %d rows", coverageConfigCount)
	}
	configuredCommand := exec.CommandContext(commandCtx, "go", "run", ".", "--device-mac", mac, "--measurement-point-name", "UI Test Meter", "--coverage-max-interval-ms", "5000")
	configuredCommand.Env = filteredDevseedTestEnvironment(database.DSN(), "b02-integration-development-password")
	if _, err := configuredCommand.CombinedOutput(); err != nil {
		t.Fatalf("canonical devseed coverage configuration failed: %v", err)
	}
	var coverageConfig domain.SystemConfig
	if err := db.Where("key = ?", coverageMaxIntervalConfigKey).First(&coverageConfig).Error; err != nil {
		t.Fatal(err)
	}
	if coverageConfig.Value != "5000" || coverageConfig.Description != coverageMaxIntervalConfigDescription {
		t.Fatalf("coverage config=%+v, want canonical 5000 configuration", coverageConfig)
	}
	var user domain.User
	if err := db.Where("account = ?", devSeedAccount).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	var shop domain.Shop
	if err := db.Where("code = ?", devSeedShopCode).First(&shop).Error; err != nil {
		t.Fatal(err)
	}
	shopID := shop.ID
	var relationCount, shopCount, deviceCount, pointCount, assignmentCount int64
	var device domain.Device
	if err := db.Where("mac_address = ?", mac).First(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.UserShopRelation{}).Where("user_id = ? AND shop_id = ?", user.ID, shopID).Count(&relationCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.Shop{}).Where("id = ? AND code = ? AND is_active = true", shopID, devSeedShopCode).Count(&shopCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.Device{}).Where("mac_address = ? AND shop_id = ?", mac, shopID).Count(&deviceCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.MeasurementPoint{}).Where("shop_id = ? AND name = ?", shopID, "UI Test Meter").Count(&pointCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.DeviceAssignment{}).Where("device_id = ? AND valid_to IS NULL", device.ID).Count(&assignmentCount).Error; err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 || shopCount != 1 || deviceCount != 1 || pointCount != 1 || assignmentCount != 1 {
		t.Fatalf("seed counts relation=%d shop=%d device=%d point=%d assignment=%d", relationCount, shopCount, deviceCount, pointCount, assignmentCount)
	}
}

func filteredDevseedTestEnvironment(databaseURL, password string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "DATABASE_URL=") || strings.HasPrefix(entry, "APP_ENV=") || strings.HasPrefix(entry, "DEVSEED_ENABLE=") || strings.HasPrefix(entry, "DEVSEED_PASSWORD=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"DATABASE_URL="+databaseURL,
		"APP_ENV=development",
		"DEVSEED_ENABLE=true",
		"DEVSEED_PASSWORD="+password,
	)
}
