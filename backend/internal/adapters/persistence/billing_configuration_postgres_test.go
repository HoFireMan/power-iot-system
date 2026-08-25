package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corebilling "power-iot-backend/internal/core/billing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestBillingConfigurationUsesHistoryCompatibilityAndEffectiveMonths(t *testing.T) {
	db := openPersistenceDB(t)
	suffix := "billing-config-" + uuid.NewString()[:8]
	clientID := insertQueryClient(t, db, suffix)
	commercial := insertQueryShop(t, db, clientID, suffix+"-commercial", true)
	noncommercial := insertQueryShop(t, db, clientID, suffix+"-noncommercial", true)
	unsupported := insertQueryShop(t, db, clientID, suffix+"-unsupported", true)
	if err := db.Exec("UPDATE shops SET electricity_tariff = ? WHERE id = ?", "LIGHTING_COMMERCIAL", commercial).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE shops SET electricity_tariff = ? WHERE id = ?", "LIGHTING_NONCOMMERCIAL", noncommercial).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE shops SET electricity_tariff = ? WHERE id = ?", "LOW_VOLTAGE", unsupported).Error; err != nil {
		t.Fatal(err)
	}
	admin := insertQueryUser(t, db, suffix+"-admin", &commercial, true, "", "")
	normal := insertQueryUser(t, db, suffix+"-normal", &commercial, false, "", "")
	if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?), (?, ?), (?, ?)", admin, commercial, admin, noncommercial, admin, unsupported).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", normal, commercial).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM shop_billing_assignments WHERE shop_id IN (?, ?, ?)", commercial, noncommercial, unsupported)
		db.Exec("DELETE FROM user_shop_relations WHERE user_id IN (?, ?)", admin, normal)
		db.Exec("DELETE FROM users WHERE id IN (?, ?)", admin, normal)
		db.Exec("DELETE FROM shops WHERE id IN (?, ?, ?)", commercial, noncommercial, unsupported)
		db.Exec("DELETE FROM clients WHERE id = ?", clientID)
	})

	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	repository := NewBillingConfigurationRepository(db)
	configuration, err := repository.FindBillingConfiguration(context.Background(), admin, commercial, now)
	if err != nil {
		t.Fatal(err)
	}
	if !configuration.Supported || configuration.Current != nil || len(configuration.Plans) != 1 {
		t.Fatalf("initial configuration=%+v", configuration)
	}
	if err := repository.SetBillingPlan(context.Background(), admin, commercial, corebilling.PlanCommercialNonTOU, now); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM shop_billing_assignments WHERE shop_id = ?", commercial).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("initial assignment count=%d", count)
	}
	var from string
	if err := db.Raw("SELECT valid_from::text FROM shop_billing_assignments WHERE shop_id = ?", commercial).Scan(&from).Error; err != nil {
		t.Fatal(err)
	}
	if from != "2026-08-01" {
		t.Fatalf("initial valid_from=%s", from)
	}
	if err := repository.SetBillingPlan(context.Background(), admin, commercial, corebilling.PlanCommercialNonTOU, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT count(*) FROM shop_billing_assignments WHERE shop_id = ?", commercial).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotent assignment count=%d", count)
	}
	if err := repository.SetBillingPlan(context.Background(), admin, noncommercial, corebilling.PlanNoncommercialResidentialNonTOU, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetBillingPlan(context.Background(), admin, noncommercial, corebilling.PlanNoncommercialNonresidentialNonTOU, now.AddDate(0, 1, 0)); err != nil {
		t.Fatal(err)
	}
	var validTo, nextFrom string
	if err := db.Raw("SELECT valid_to::text, (SELECT min(valid_from)::text FROM shop_billing_assignments WHERE shop_id = ? AND valid_from >= DATE '2026-09-01') FROM shop_billing_assignments WHERE shop_id = ? AND valid_to IS NOT NULL", noncommercial, noncommercial).Row().Scan(&validTo, &nextFrom); err != nil {
		t.Fatal(err)
	}
	if validTo != "2026-09-01" || nextFrom != "2026-09-01" {
		t.Fatalf("adjacent assignments=%s/%s", validTo, nextFrom)
	}
	if err := repository.SetBillingPlan(context.Background(), admin, commercial, corebilling.PlanNoncommercialResidentialNonTOU, now); !errors.Is(err, corebilling.ErrBillingTariffMismatch) {
		t.Fatalf("commercial mismatch=%v", err)
	}
	unsupportedConfig, err := repository.FindBillingConfiguration(context.Background(), admin, unsupported, now)
	if err != nil {
		t.Fatal(err)
	}
	if unsupportedConfig.Supported || len(unsupportedConfig.Plans) != 0 {
		t.Fatalf("unsupported configuration=%+v", unsupportedConfig)
	}
	if err := repository.SetBillingPlan(context.Background(), admin, unsupported, corebilling.PlanCommercialNonTOU, now); !errors.Is(err, corebilling.ErrBillingTariffMismatch) {
		t.Fatalf("unsupported mutation=%v", err)
	}
	if err := repository.SetBillingPlan(context.Background(), normal, commercial, corebilling.PlanCommercialNonTOU, now); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("normal mutation=%v", err)
	}

	mutation := NewShopMutationRepository(db)
	if err := mutation.UpdateShopTariff(context.Background(), admin, commercial, "LIGHTING_COMMERCIAL"); err != nil {
		t.Fatal(err)
	}
	if err := mutation.UpdateShopTariff(context.Background(), admin, commercial, "LIGHTING_NONCOMMERCIAL"); !errors.Is(err, corebilling.ErrBillingHistoryConflict) {
		t.Fatalf("history tariff guard=%v", err)
	}
	if err := mutation.UpdateShopTariff(context.Background(), normal, commercial, "LIGHTING_NONCOMMERCIAL"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("unauthorized history guard leaked=%v", err)
	}
}

func TestBillingConfigurationConcurrentInitialMutationHasOneAssignment(t *testing.T) {
	db := openPersistenceDB(t)
	suffix := "billing-concurrent-" + uuid.NewString()[:8]
	clientID := insertQueryClient(t, db, suffix)
	shopID := insertQueryShop(t, db, clientID, suffix+"-shop", true)
	if err := db.Exec("UPDATE shops SET electricity_tariff = 'LIGHTING_COMMERCIAL' WHERE id = ?", shopID).Error; err != nil {
		t.Fatal(err)
	}
	admin := insertQueryUser(t, db, suffix+"-admin", &shopID, true, "", "")
	if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", admin, shopID).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM shop_billing_assignments WHERE shop_id = ?", shopID)
		db.Exec("DELETE FROM user_shop_relations WHERE user_id = ?", admin)
		db.Exec("DELETE FROM users WHERE id = ?", admin)
		db.Exec("DELETE FROM shops WHERE id = ?", shopID)
		db.Exec("DELETE FROM clients WHERE id = ?", clientID)
	})
	repository := NewBillingConfigurationRepository(db)
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- repository.SetBillingPlan(context.Background(), admin, shopID, corebilling.PlanCommercialNonTOU, now)
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM shop_billing_assignments WHERE shop_id = ?", shopID).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent assignment count=%d", count)
	}
}
