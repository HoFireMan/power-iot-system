package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/logger"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
)

func TestDevseedDatabaseUsesDiscardLogger(t *testing.T) {
	if got := devseedGORMConfig().Logger; got != logger.Discard {
		t.Fatal("devseed database logger must discard SQL and parameter output")
	}
}

func TestDevseedGuardFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, appEnv, enabled string
		wantErr               bool
	}{
		{name: "missing environment", wantErr: true},
		{name: "production", appEnv: "production", enabled: "true", wantErr: true},
		{name: "missing enable", appEnv: "development", wantErr: true},
		{name: "wrong enable", appEnv: "development", enabled: "TRUE", wantErr: true},
		{name: "development enabled", appEnv: "development", enabled: "true"},
		{name: "test enabled", appEnv: "test", enabled: "true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSeedGuard(test.appEnv, test.enabled); (err != nil) != test.wantErr {
				t.Fatalf("validateSeedGuard() error=%v, wantErr=%t", err, test.wantErr)
			}
		})
	}
}

func TestDevseedAdmissionAcceptedOnlyServingSchemas(t *testing.T) {
	for _, test := range []struct {
		name        string
		disposition migrations.RuntimeAdmissionDisposition
		want        bool
	}{
		{name: "clean v6", disposition: migrations.RuntimeServeV6, want: true},
		{name: "clean b02", disposition: migrations.RuntimeServeB02, want: true},
		{name: "protected migration required", disposition: migrations.RuntimeProtectedMigrationNeeded, want: false},
		{name: "empty", disposition: "", want: false},
		{name: "unknown", disposition: migrations.RuntimeAdmissionDisposition("UNKNOWN"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := devseedAdmissionAccepted(test.disposition); got != test.want {
				t.Fatalf("devseedAdmissionAccepted(%q)=%t, want %t", test.disposition, got, test.want)
			}
		})
	}
}

func TestParseCoverageMaxIntervalValue(t *testing.T) {
	for _, test := range []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{name: "five seconds", raw: "5000", want: 5000},
		{name: "minimum", raw: "1000", want: 1000},
		{name: "below minimum", raw: "999", wantErr: true},
		{name: "malformed duration", raw: "5s", wantErr: true},
		{name: "malformed decimal", raw: "1.5", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "zero", raw: "0", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCoverageMaxIntervalValue(test.raw)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseCoverageMaxIntervalValue(%q) error=%v, wantErr=%t", test.raw, err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("parseCoverageMaxIntervalValue(%q)=%d, want %d", test.raw, got, test.want)
			}
		})
	}
}

func TestOptionalCoverageMaxIntervalValue(t *testing.T) {
	if got, err := parseOptionalCoverageMaxIntervalValue("", false); err != nil || got != nil {
		t.Fatalf("absent coverage configuration got=%v err=%v, want nil/nil", got, err)
	}
	got, err := parseOptionalCoverageMaxIntervalValue("5000", true)
	if err != nil || got == nil || *got != 5000 {
		t.Fatalf("provided coverage configuration got=%v err=%v, want 5000", got, err)
	}
	if value, provided := resolveCoverageMaxIntervalInput("5000", false, "1000", true); !provided || value != "1000" {
		t.Fatalf("environment fallback value=%q provided=%t, want 1000/true", value, provided)
	}
	if value, provided := resolveCoverageMaxIntervalInput("5000", true, "1000", true); !provided || value != "5000" {
		t.Fatalf("CLI precedence value=%q provided=%t, want 5000/true", value, provided)
	}
	if value, provided := resolveCoverageMaxIntervalInput("", false, "", false); provided || value != "" {
		t.Fatalf("absent input value=%q provided=%t, want empty/false", value, provided)
	}
}

func TestDevseedPasswordRequiresExplicitConfiguration(t *testing.T) {
	secret := "development-only-password"
	if got, err := readDevelopmentPassword(secret); err != nil || got != secret {
		t.Fatalf("readDevelopmentPassword() returned an unexpected password or error: %v", err)
	}
	for _, value := range []string{"", " ", "\t"} {
		if _, err := readDevelopmentPassword(value); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("readDevelopmentPassword(%q) error=%v, want explicit configuration failure", value, err)
		}
	}
	if _, err := seedDevelopmentIdentity(context.Background(), nil, secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("identity hashing error leaked password: %v", err)
	}
}

func TestAdminFixturePasswordIsOptionalOnlyWhenFixtureIsDisabled(t *testing.T) {
	if got, err := resolveAdminFixturePassword(false, ""); err != nil || got != "" {
		t.Fatalf("disabled admin fixture got password=%q err=%v, want empty/nil", got, err)
	}
	for _, value := range []string{"", " ", "\t"} {
		result, err := resolveAdminFixturePassword(true, value)
		if err == nil || result != "" || (strings.TrimSpace(value) != "" && strings.Contains(err.Error(), value)) {
			t.Fatalf("missing admin password=%q result=%q err=%v, want fail-closed error without secret", value, result, err)
		}
	}
	secret := "separate-admin-password"
	if got, err := resolveAdminFixturePassword(true, secret); err != nil || got != secret {
		t.Fatalf("enabled admin fixture got password=%q err=%v, want supplied runtime secret", got, err)
	}
}

func TestDevseedIdentityIsIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; devseed PostgreSQL integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := openDevseedDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password := "devseed-idempotence-password"
	shopID, err := seedDevelopmentIdentity(context.Background(), db, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedDevelopmentIdentity(context.Background(), db, password); err != nil {
		t.Fatal(err)
	}
	var user domain.User
	if err := db.Where("account = ?", devSeedAccount).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	originalPasswordHash := user.PasswordHash
	wrongPassword := password + "-wrong"
	var capturedLogs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&capturedLogs)
	mismatchErr := func() error {
		_, err := seedDevelopmentIdentity(context.Background(), db, wrongPassword)
		return err
	}()
	log.SetOutput(previousLogWriter)
	if mismatchErr == nil {
		t.Fatal("seed identity accepted a different password")
	}
	if strings.Contains(mismatchErr.Error(), wrongPassword) || strings.Contains(mismatchErr.Error(), originalPasswordHash) {
		t.Fatalf("password mismatch error leaked a secret: %v", mismatchErr)
	}
	if strings.Contains(capturedLogs.String(), wrongPassword) || strings.Contains(capturedLogs.String(), originalPasswordHash) {
		t.Fatalf("password mismatch logs leaked a secret: %s", capturedLogs.String())
	}
	var unchangedUser domain.User
	if err := db.Where("account = ?", devSeedAccount).First(&unchangedUser).Error; err != nil {
		t.Fatal(err)
	}
	if unchangedUser.PasswordHash != originalPasswordHash {
		t.Fatal("password mismatch rewrote the persisted password hash")
	}
	if !user.AuthEnabled || user.CurrentShopID == nil || *user.CurrentShopID != shopID {
		t.Fatalf("seed user metadata is incomplete: auth_enabled=%t current_shop_id_set=%t", user.AuthEnabled, user.CurrentShopID != nil)
	}
	if valid, err := security.VerifyPassword([]byte(password), user.PasswordHash); err != nil || !valid {
		t.Fatalf("seed password verification valid=%t err=%v", valid, err)
	}
	var relationCount, shopCount, clientCount int64
	if err := db.Model(&domain.UserShopRelation{}).Where("user_id = ? AND shop_id = ?", user.ID, shopID).Count(&relationCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.Shop{}).Where("id = ? AND code = ? AND is_active = true", shopID, devSeedShopCode).Count(&shopCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.Client{}).Where("code = ?", devSeedClientCode).Count(&clientCount).Error; err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 || shopCount != 1 || clientCount != 1 {
		t.Fatalf("seed counts relation=%d shop=%d client=%d", relationCount, shopCount, clientCount)
	}
	if err := db.Model(&domain.UserShopRelation{}).Where("user_id = ? AND shop_id = ?", user.ID, shopID).Update("shop_role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := seedDevelopmentIdentity(context.Background(), db, password); err == nil {
		t.Fatal("seed identity accepted a conflicting relation role")
	}
	if err := db.Model(&domain.UserShopRelation{}).Where("user_id = ? AND shop_id = ?", user.ID, shopID).Update("shop_role", "staff").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ?", user.ID).Delete(&domain.UserShopRelation{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&domain.User{}, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&domain.Shop{}, shopID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("code = ?", devSeedClientCode).Delete(&domain.Client{}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestDevseedFixtureRejectsCrossShopDevice(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; devseed PostgreSQL integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := openDevseedDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	client := domain.Client{Name: "fixture-client-" + suffix, Code: "fixture-client-" + suffix}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	ownerClient := domain.Client{Name: "fixture-owner-" + suffix, Code: "fixture-owner-" + suffix}
	if err := db.Create(&ownerClient).Error; err != nil {
		t.Fatal(err)
	}
	shopA := domain.Shop{ClientID: client.ID, Name: "fixture-shop-a-" + suffix, Code: "fixture-shop-a-" + suffix, IsActive: true}
	shopB := domain.Shop{ClientID: client.ID, Name: "fixture-shop-b-" + suffix, Code: "fixture-shop-b-" + suffix, IsActive: true}
	if err := db.Create(&shopA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&shopB).Error; err != nil {
		t.Fatal(err)
	}
	legacyOwner := client.ID
	deviceA := domain.Device{ShopID: shopA.ID, InventoryOwnerClientID: &legacyOwner, MacAddress: strings.ToUpper(suffix[:12]), Name: "cross-shop-legacy"}
	inventoryOwner := ownerClient.ID
	deviceB := domain.Device{InventoryOwnerClientID: &inventoryOwner, MacAddress: strings.ToUpper(suffix[12:24]), Name: "cross-shop-owner"}
	if err := db.Create(&deviceA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&deviceB).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Where("device_id IN ?", []uint{deviceA.ID, deviceB.ID}).Delete(&domain.DeviceAssignment{})
		db.Where("shop_id IN ?", []uint{shopA.ID, shopB.ID}).Delete(&domain.MeasurementPoint{})
		db.Delete(&domain.Device{}, deviceA.ID)
		db.Delete(&domain.Device{}, deviceB.ID)
		db.Delete(&domain.Shop{}, shopA.ID)
		db.Delete(&domain.Shop{}, shopB.ID)
		db.Delete(&domain.Client{}, ownerClient.ID)
		db.Delete(&domain.Client{}, client.ID)
	}()

	for _, test := range []struct {
		name, mac, point string
	}{
		{name: "legacy shop", mac: deviceA.MacAddress, point: "legacy-cross-shop"},
		{name: "inventory owner", mac: deviceB.MacAddress, point: "owner-cross-shop"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := seedFixtures(context.Background(), db, test.mac, "ignored", shopB.ID, test.point, ""); err == nil {
				t.Fatal("cross-shop fixture assignment was accepted")
			}
		})
	}
}

func TestDevseedFixturePhaseParticipatesInSharedWriterFence(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; devseed PostgreSQL integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := openDevseedDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	mac := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:12]
	fence, err := migrations.OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan error, 1)
	go func() {
		_, _, err := seedFixtures(context.Background(), db, mac, "fenced-devseed", 0, "", "")
		resultCh <- err
	}()
	select {
	case err := <-resultCh:
		t.Fatalf("devseed fixture phase crossed exclusive fence: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	var count int64
	if err := db.Model(&domain.Device{}).Where("mac_address = ?", mac).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("devseed fixture wrote before shared admission")
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("devseed fixture phase did not proceed after exclusive release")
	}
	if err := db.Where("mac_address = ?", mac).Delete(&domain.Device{}).Error; err != nil {
		t.Fatal(err)
	}
}
