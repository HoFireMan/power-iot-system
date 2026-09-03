package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/testsupport"
)

func TestDevseedAdminFixtureIdentityAndAuthorization(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; admin fixture PostgreSQL integration test not run")
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
	dsn := database.DSN()
	ctx := context.Background()
	if report, err := migrations.RunD6ProtectedMigrationOperator(ctx, dsn, func(context.Context) error { return nil }); err != nil || report.PostCommitState != migrations.ProtectedStateCleanV6 {
		t.Fatalf("D6 report=%+v err=%v", report, err)
	}
	if report, err := migrations.RunB02ProtectedMigrationOperator(ctx, dsn, func(context.Context) error { return nil }); err != nil || report.PostCommitState != migrations.ProtectedStateCleanB02 {
		t.Fatalf("B-02 report=%+v err=%v", report, err)
	}
	db, err := openDevseedDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}

	const (
		mac            = "AABBCCDDEEFF"
		devseedPass    = "admin-fixture-normal-password"
		adminPass      = "admin-fixture-separate-password"
		wrongAdminPass = "admin-fixture-wrong-password"
	)
	cleanupDevseedAdminFixture(t, db, mac)

	runDevseedCommand(t, dsn, devseedPass, "--device-mac", mac)
	var adminCount int64
	if err := db.Model(&domain.User{}).Where("account = ?", devSeedAdminAccount).Count(&adminCount).Error; err != nil {
		t.Fatal(err)
	}
	if adminCount != 0 {
		t.Fatalf("admin fixture created without --admin-fixture: count=%d", adminCount)
	}

	runDevseedCommand(t, dsn, devseedPass, "--device-mac", mac, "--admin-fixture")
	var shop domain.Shop
	if err := db.Where("code = ?", devSeedShopCode).First(&shop).Error; err != nil {
		t.Fatal(err)
	}
	var normal domain.User
	if err := db.Where("account = ?", devSeedAccount).First(&normal).Error; err != nil {
		t.Fatal(err)
	}
	if normal.IsAdmin {
		t.Fatal("normal devseed account was promoted")
	}

	var admin domain.User
	if err := db.Where("account = ?", devSeedAdminAccount).First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	assertAdminFixtureUser(t, db, admin, shop.ID)

	if err := seedDevelopmentAdminIdentity(context.Background(), db, shop.ID, adminPass); err != nil {
		t.Fatalf("same admin fixture was not idempotent: %v", err)
	}
	var originalHash string
	if err := db.Model(&domain.User{}).Where("id = ?", admin.ID).Pluck("password_hash", &originalHash).Error; err != nil {
		t.Fatal(err)
	}
	if err := seedDevelopmentAdminIdentity(context.Background(), db, shop.ID, wrongAdminPass); err == nil || strings.Contains(err.Error(), wrongAdminPass) {
		t.Fatalf("wrong admin password result=%v, want fail closed without secret", err)
	}
	var unchangedHash string
	if err := db.Model(&domain.User{}).Where("id = ?", admin.ID).Pluck("password_hash", &unchangedHash).Error; err != nil {
		t.Fatal(err)
	}
	if unchangedHash != originalHash {
		t.Fatal("wrong admin password changed the persisted password hash")
	}

	otherClient := domain.Client{Name: "admin-fixture-other-client", Code: "admin-fixture-other-client"}
	if err := db.Create(&otherClient).Error; err != nil {
		t.Fatal(err)
	}
	otherShop := domain.Shop{ClientID: otherClient.ID, Name: "admin-fixture-other-shop", Code: "admin-fixture-other-shop", IsActive: true}
	if err := db.Create(&otherShop).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.User{}).Where("id = ?", admin.ID).Update("is_admin", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := seedDevelopmentAdminIdentity(context.Background(), db, shop.ID, adminPass); err == nil {
		t.Fatal("existing non-admin devseed-admin account was accepted")
	}
	if err := db.Model(&domain.User{}).Where("id = ?", admin.ID).Updates(map[string]any{"is_admin": true, "current_shop_id": otherShop.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := seedDevelopmentAdminIdentity(context.Background(), db, shop.ID, adminPass); err == nil {
		t.Fatal("admin fixture with wrong CurrentShopID was accepted")
	}
	if err := db.Model(&domain.User{}).Where("id = ?", admin.ID).Update("current_shop_id", shop.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.UserShopRelation{UserID: admin.ID, ShopID: otherShop.ID, ShopRole: "staff"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := seedDevelopmentAdminIdentity(context.Background(), db, shop.ID, adminPass); err == nil {
		t.Fatal("admin fixture with unrelated Shop relation was accepted")
	}
	if err := db.Where("user_id = ? AND shop_id = ?", admin.ID, otherShop.ID).Delete(&domain.UserShopRelation{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := seedDevelopmentAdminIdentity(context.Background(), db, shop.ID, adminPass); err != nil {
		t.Fatalf("restored canonical admin fixture was rejected: %v", err)
	}

	mutation := persistence.NewShopMutationRepository(db)
	if err := mutation.UpdateShopTariff(context.Background(), normal.ID, shop.ID, "LIGHTING_COMMERCIAL"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("normal devseed mutation error=%v, want authorization rejection", err)
	}
	if err := mutation.UpdateShopTariff(context.Background(), admin.ID, shop.ID, "LIGHTING_COMMERCIAL"); err != nil {
		t.Fatalf("scoped admin mutation failed: %v", err)
	}
	if err := mutation.UpdateShopTariff(context.Background(), admin.ID, otherShop.ID, "LIGHTING_COMMERCIAL"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("unrelated Shop mutation error=%v, want authorization rejection", err)
	}
}

func runDevseedCommand(t *testing.T, dsn, password string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", append([]string{"run", "."}, args...)...)
	command.Dir = "."
	command.Env = devseedAdminTestEnvironment(dsn, password, "admin-fixture-separate-password")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("canonical devseed command args=%v failed: %v output=%s", args, err, output)
	}
	if strings.Contains(string(output), password) || strings.Contains(string(output), "admin-fixture-separate-password") {
		t.Fatalf("canonical devseed output leaked a password: %s", output)
	}
}

func devseedAdminTestEnvironment(dsn, password, adminPassword string) []string {
	env := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "DATABASE_URL=") || strings.HasPrefix(entry, "APP_ENV=") || strings.HasPrefix(entry, "DEVSEED_ENABLE=") || strings.HasPrefix(entry, "DEVSEED_PASSWORD=") || strings.HasPrefix(entry, "DEVSEED_ADMIN_PASSWORD=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"DATABASE_URL="+dsn,
		"APP_ENV=development",
		"DEVSEED_ENABLE=true",
		"DEVSEED_PASSWORD="+password,
		"DEVSEED_ADMIN_PASSWORD="+adminPassword,
	)
}

func assertAdminFixtureUser(t *testing.T, db *gorm.DB, user domain.User, shopID uint) {
	t.Helper()
	if !user.AuthEnabled || !user.IsAdmin || user.CurrentShopID == nil || *user.CurrentShopID != shopID {
		t.Fatalf("admin fixture user=%+v, want enabled admin with current development Shop", user)
	}
	var relations []domain.UserShopRelation
	if err := db.Where("user_id = ?", user.ID).Find(&relations).Error; err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].ShopID != shopID || relations[0].ShopRole != "staff" {
		t.Fatalf("admin fixture relations=%+v, want only development Shop staff relation", relations)
	}
}

func cleanupDevseedAdminFixture(t *testing.T, db *gorm.DB, mac string) {
	t.Helper()
	var client domain.Client
	if err := db.Where("code = ?", devSeedClientCode).First(&client).Error; err == nil {
		var shop domain.Shop
		if err := db.Where("code = ?", devSeedShopCode).First(&shop).Error; err == nil {
			var users []domain.User
			if err := db.Where("account IN ?", []string{devSeedAccount, devSeedAdminAccount}).Find(&users).Error; err != nil {
				t.Fatal(err)
			}
			for _, user := range users {
				if err := db.Where("user_id = ?", user.ID).Delete(&domain.UserShopRelation{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Delete(&domain.User{}, user.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Where("mac_address = ?", mac).Delete(&domain.Device{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Delete(&domain.Shop{}, shop.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Delete(&domain.Client{}, client.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
}
